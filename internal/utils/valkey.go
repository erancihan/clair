package utils

import (
	"context"
	"os"

	"github.com/valkey-io/valkey-go"
	"go.uber.org/zap"
)

func NewValKeyClient(ctx context.Context) valkey.Client {
	valkeyPort := "6379"
	valkeyHost := "127.0.0.1"
	if os.Getenv("VALKEY_PORT") != "" {
		valkeyPort = os.Getenv("VALKEY_PORT")
	}
	if os.Getenv("VALKEY_HOST") != "" {
		valkeyHost = os.Getenv("VALKEY_HOST")
	}

	options := valkey.ClientOption{
		InitAddress: []string{valkeyHost + ":" + valkeyPort},
	}

	logger := NewLogger("valkey")
	defer func() { _ = logger.Sync() }()

	client, err := valkey.NewClient(options)
	if err != nil {
		logger.Warn("failed to connect to valkey", zap.Error(err))
		return nil
	}

	logger.Info("Connected to valkey")

	return client
}
