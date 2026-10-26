package server_context

import (
	"github.com/valkey-io/valkey-go"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type BackEndContext struct {
	DBConn *gorm.DB
	Logger *zap.Logger
	ValKey valkey.Client

	// Closing is closed when the server is shutting down. A handler that would
	// otherwise run for the life of the connection — a stream — selects on it,
	// so a graceful shutdown drains instead of waiting out its timeout.
	Closing <-chan struct{}
}
