// Package booking is the HTTP layer for the booking domain: appointments and
// ticketing, both sitting on the shared reservation kernel in internal/booking.
//
// Handlers here stay thin. They decode a request, resolve who is asking, call
// into the domain packages and encode the answer; every rule about capacity,
// expiry and money lives below them, where it can be tested without a server.
package booking

import (
	"encoding/json"
	"net/http"
	"strconv"

	server_context "github.com/erancihan/clair/internal/server/context"
	"gorm.io/gorm"
)

// db returns a GORM session bound to the request's context, so a client that
// disconnects mid-request cancels the query it was waiting on.
func db(ctx server_context.BackEndContext, r *http.Request) *gorm.DB {
	return ctx.DBConn.Session(&gorm.Session{Context: r.Context()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// parseID reads a positive integer path parameter. Zero is rejected along with
// unparseable values: it is never a real row id, and letting it through turns a
// malformed request into a query that quietly matches nothing.
func parseID(r *http.Request, name string) (uint, bool) {
	v, err := strconv.ParseUint(r.PathValue(name), 10, 64)
	if err != nil || v == 0 {
		return 0, false
	}
	return uint(v), true
}
