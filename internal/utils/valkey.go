package utils

import (
	"context"
	"os"
	"time"

	"github.com/valkey-io/valkey-go"
	"go.uber.org/zap"
)

// Valkey health status values reported by ValKeyStatus and surfaced on the
// health endpoint.
const (
	ValKeyStatusOK       = "ok"
	ValKeyStatusDegraded = "degraded"
	ValKeyStatusDisabled = "disabled"
)

// valkeyPingTimeout bounds how long we wait for a Valkey PING before treating
// the server as unreachable. Kept short so startup and health checks fail open
// quickly instead of hanging on a dead address.
const valkeyPingTimeout = 3 * time.Second

// PingValKey issues a bounded PING against the given Valkey client to verify
// connectivity. It returns nil when the server responds within the timeout.
// The caller owns the nil-client contract: PingValKey assumes a non-nil client.
func PingValKey(ctx context.Context, client valkey.Client) error {
	pingCtx, cancel := context.WithTimeout(ctx, valkeyPingTimeout)
	defer cancel()

	return client.Do(pingCtx, client.B().Ping().Build()).Error()
}

// valKeyConfigured reports whether Valkey is configured via the environment.
// It mirrors NewValKeyClient: a host must be present (an empty host is treated
// as unconfigured even when a port is set).
func valKeyConfigured() bool {
	return os.Getenv("VALKEY_HOST") != ""
}

// ValKeyStatus reports the health of the Valkey dependency without failing the
// caller. Valkey is optional and fails open, so the client may be nil:
//   - non-nil client, successful PING -> "ok"
//   - non-nil client, failed PING     -> "degraded" (up at startup, now unreachable)
//   - nil client, Valkey configured   -> "degraded" (configured but unavailable)
//   - nil client, not configured      -> "disabled"
func ValKeyStatus(ctx context.Context, client valkey.Client) string {
	if client == nil {
		if valKeyConfigured() {
			return ValKeyStatusDegraded
		}
		return ValKeyStatusDisabled
	}

	if err := PingValKey(ctx, client); err != nil {
		return ValKeyStatusDegraded
	}

	return ValKeyStatusOK
}

func NewValKeyClient(ctx context.Context) valkey.Client {
	logger := NewLogger("valkey")
	defer func() { _ = logger.Sync() }()

	valkeyPort := os.Getenv("VALKEY_PORT") // "6379"
	valkeyHost := os.Getenv("VALKEY_HOST") // "127.0.0.1"

	// Valkey is optional: without a host we run in degraded mode (nil client).
	// Treat an empty host as unconfigured even when a port is set, otherwise
	// InitAddress would become ":6379" (an empty host).
	if valkeyHost == "" {
		logger.Info("VALKEY_HOST is not set, skipping Valkey client creation")
		return nil
	}

	// The host is set (guarded above); only the port is defaulted.
	if valkeyPort == "" {
		valkeyPort = "6379"
	}

	options := valkey.ClientOption{
		InitAddress: []string{valkeyHost + ":" + valkeyPort},
	}

	client, err := valkey.NewClient(options)
	if err != nil {
		logger.Warn("failed to create valkey client", zap.Error(err))
		return nil
	}

	// valkey.NewClient can succeed without the server being reachable, so
	// verify connectivity with a bounded PING before declaring success.
	// Fail open: on error, close the client and continue without Valkey.
	if err := PingValKey(ctx, client); err != nil {
		logger.Warn("valkey unreachable, continuing without Valkey", zap.Error(err))
		client.Close()
		return nil
	}

	logger.Info("connected to valkey")

	return client
}
