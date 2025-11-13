package utils

import (
	"os"

	"go.uber.org/zap"
)

func NewLogger(service string) *zap.Logger {
	env := os.Getenv("APP_ENV")

	zapOptions := zap.Fields(
		zap.String("env", env),
		zap.String("service", service),
	)

	logger, _ := zap.NewProduction(zapOptions)

	if env == "" || env == "development" {
		logger, _ = zap.NewDevelopment()
	}

	return logger
}
