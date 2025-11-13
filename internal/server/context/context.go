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
}
