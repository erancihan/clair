package booking

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	kernel "github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	api_auth "github.com/erancihan/clair/internal/server/authentication"
	server_context "github.com/erancihan/clair/internal/server/context"
	"go.uber.org/zap"
)

const (
	// PaymentWebhookSecretEnv names the environment variable holding the shared
	// secret the payment provider signs its deliveries with. There is no default:
	// an unset secret disables the webhook rather than falling back to something
	// guessable, because this endpoint is what turns a hold into a paid ticket.
	//
	// It is exported because it is deployment configuration — whoever runs the
	// application has to set it, and tests have to sign with it.
	PaymentWebhookSecretEnv = "BOOKING_PAYMENT_WEBHOOK_SECRET"

	// paymentSignatureHeader carries the HMAC-SHA256 digest of the raw body.
	paymentSignatureHeader = "X-Payment-Signature"

	// maxWebhookBody caps how much of a delivery is read. The body has to be
	// buffered whole to verify the signature over it, so without a cap an
	// unauthenticated caller could make the server hold an arbitrary amount.
	maxWebhookBody = 64 << 10
)

// paymentEvent is the provider delivery this domain understands.
//
// DeliveryID is the provider's own id for the delivery and becomes the payment's
// idempotency key, which is what makes a retried delivery a no-op instead of a
// second capture.
type paymentEvent struct {
	DeliveryID  string `json:"delivery_id"`
	OrderID     uint   `json:"order_id"`
	Kind        string `json:"kind"` // capture|charge
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
	ProviderRef string `json:"provider_ref"`
}

// paymentWebhook captures a pending order on a signed delivery from a payment
// provider.
//
// This is the only route that turns money into fulfilment, and it is a machine
// endpoint: no session, no CSRF token, and therefore no user to authorize. The
// signature over the raw body is the whole of its authentication, which is why
// the body is buffered and verified before it is even decoded — a payload that
// cannot be trusted must not be parsed into a capture.
//
// It answers 200 for a delivery it has already seen. Providers retry until they
// get a success, and reporting an error for a duplicate would have them retry a
// capture that has already happened.
func paymentWebhook(ctx server_context.BackEndContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret := []byte(os.Getenv(PaymentWebhookSecretEnv))
		if len(secret) == 0 {
			ctx.Logger.Error("payment webhook received with no signing secret configured",
				zap.String("env", PaymentWebhookSecretEnv))
			http.Error(w, "payment webhook not configured", http.StatusServiceUnavailable)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			http.Error(w, "unreadable request body", http.StatusBadRequest)
			return
		}

		if !api_auth.VerifyProviderSignature(secret, body, r.Header.Get(paymentSignatureHeader)) {
			// The provider is not named in the response, and the reason is not
			// distinguished from a malformed one: an attacker probing this
			// endpoint learns nothing beyond "rejected".
			ctx.Logger.Warn("rejected payment webhook with invalid signature",
				zap.String("provider", r.PathValue("provider")))
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var event paymentEvent
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "invalid request payload", http.StatusBadRequest)
			return
		}
		if event.DeliveryID == "" || event.OrderID == 0 {
			http.Error(w, "delivery_id and order_id are required", http.StatusBadRequest)
			return
		}

		conn := db(ctx, r)
		var order models.BookingOrder
		if res := conn.Limit(1).Find(&order, event.OrderID); res.RowsAffected == 0 {
			http.Error(w, "order not found", http.StatusNotFound)
			return
		}

		// The order is what the amount is taken from, not the delivery. A payload
		// that has passed the signature check is authentic, but the price of an
		// order is this application's to decide.
		kind := event.Kind
		if kind == "" {
			kind = "capture"
		}
		payment := models.BookingPayment{
			Provider:       r.PathValue("provider"),
			ProviderRef:    event.ProviderRef,
			Kind:           kind,
			Status:         "succeeded",
			AmountCents:    order.TotalCents,
			Currency:       order.Currency,
			IdempotencyKey: event.DeliveryID,
		}

		if err := kernel.CaptureOrder(conn, event.OrderID, payment, time.Now()); err != nil {
			ctx.Logger.Error("capture order from payment webhook",
				zap.Uint("order_id", event.OrderID), zap.Error(err))
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"order_id": event.OrderID, "status": "captured",
		})
	}
}
