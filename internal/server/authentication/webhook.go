package authentication

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// VerifyProviderSignature reports whether sigHeader is a valid HMAC-SHA256
// signature of the raw request body using the shared secret. The comparison is
// constant-time (hmac.Equal). The header may be a bare hex digest or carry a
// "sha256=" prefix, matching the convention used by several webhook providers.
//
// This is only the verifier; wiring it to a concrete webhook handler belongs to
// the domain that owns that webhook (e.g. the booking/payment domain), not to
// this shared authentication layer.
func VerifyProviderSignature(secret, body []byte, sigHeader string) bool {
	if len(secret) == 0 || sigHeader == "" {
		return false
	}

	provided, err := hex.DecodeString(strings.TrimPrefix(sigHeader, "sha256="))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	expected := mac.Sum(nil)

	return hmac.Equal(provided, expected)
}
