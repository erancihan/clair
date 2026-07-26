package test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	authentication "github.com/erancihan/clair/internal/server/authentication"
)

func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyProviderSignature(t *testing.T) {
	secret := []byte("provider-shared-secret")
	body := []byte(`{"event":"payment.succeeded","id":"evt_123"}`)
	valid := sign(secret, body)

	tests := []struct {
		name   string
		secret []byte
		body   []byte
		header string
		want   bool
	}{
		{
			name:   "valid bare hex signature is accepted",
			secret: secret,
			body:   body,
			header: valid,
			want:   true,
		},
		{
			name:   "valid sha256-prefixed signature is accepted",
			secret: secret,
			body:   body,
			header: "sha256=" + valid,
			want:   true,
		},
		{
			name:   "tampered body is rejected",
			secret: secret,
			body:   append([]byte("x"), body...),
			header: valid,
			want:   false,
		},
		{
			name:   "tampered signature is rejected",
			secret: secret,
			body:   body,
			header: valid[:len(valid)-1] + "0",
			want:   false,
		},
		{
			name:   "wrong secret is rejected",
			secret: []byte("not-the-secret"),
			body:   body,
			header: valid,
			want:   false,
		},
		{
			name:   "empty signature header is rejected",
			secret: secret,
			body:   body,
			header: "",
			want:   false,
		},
		{
			name:   "non-hex signature is rejected",
			secret: secret,
			body:   body,
			header: "not-hex-!!",
			want:   false,
		},
		{
			name:   "empty secret is rejected",
			secret: nil,
			body:   body,
			header: valid,
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := authentication.VerifyProviderSignature(tc.secret, tc.body, tc.header); got != tc.want {
				t.Errorf("VerifyProviderSignature = %v, want %v", got, tc.want)
			}
		})
	}
}
