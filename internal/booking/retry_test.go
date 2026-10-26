package booking

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"serialization", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"wrapped deadlock", fmt.Errorf("commit: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"digits in message only", errors.New("row 40001 not found"), false},
		{"plain error", errors.New("boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryable(c.err); got != c.want {
				t.Fatalf("isRetryable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestNewTokenIs32Hex(t *testing.T) {
	a, b := NewToken(), NewToken()
	if len(a) != 32 || a == b {
		t.Fatalf("NewToken() = %q, %q; want two distinct 32-char tokens", a, b)
	}
}
