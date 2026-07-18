package database

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/utils"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func New(ctx context.Context) (*gorm.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is not set")
	}

	zapLogger := utils.NewLogger("database")
	defer func() { _ = zapLogger.Sync() }()

	zapLogger.Info("Connecting to PostgreSQL")

	logger := ZapToGormLogger(zapLogger)
	logger.SetAsDefault() // configure gorm to use our logger

	// TODO: configure gorm logger level based on env variable

	config := &gorm.Config{
		PrepareStmt: true,
		Logger:      logger.LogMode(gormlogger.Info),
	}

	db, err := gorm.Open(postgres.Open(dsn), config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to access database handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(time.Hour)

	zapLogger.Info("Connected to PostgreSQL database")

	// register models here
	if err := db.AutoMigrate(&models.User{}); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return db, nil
}
