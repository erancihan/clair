package utils

import (
	"context"
	"os"

	"github.com/valkey-io/valkey-go"
	"go.uber.org/zap"
)

func NewValKeyClient(ctx context.Context) valkey.Client {
	logger := NewLogger("valkey")

	valkeyPort := os.Getenv("VALKEY_PORT") // "6379"
	valkeyHost := os.Getenv("VALKEY_HOST") // "127.0.0.1"

	// If both VALKEY_PORT and VALKEY_HOST are not set, return nil
	if valkeyPort == "" && valkeyHost == "" {
		logger.Info("VALKEY_PORT and VALKEY_HOST are not set, skipping Valkey client creation")
		return nil
	}

	if valkeyPort == "" && valkeyHost != "" {
		valkeyPort = "6379"
	}

	options := valkey.ClientOption{
		InitAddress: []string{valkeyHost + ":" + valkeyPort},
	}

	defer func() { _ = logger.Sync() }()

	client, err := valkey.NewClient(options)
	if err != nil {
		logger.Warn("failed to connect to valkey", zap.Error(err))
		return nil
	}

	logger.Info("Connected to valkey")

	return client
}
