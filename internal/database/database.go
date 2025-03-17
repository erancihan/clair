package database

import (
	"context"
	"os"

	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/utils"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newSQLiteConn(ctx context.Context) (*gorm.DB, error) {
	dbPath := ".opt/clair.db"
	if os.Getenv("DB_FOLDER") != "" {
		dbPath = os.Getenv("DB_FOLDER") + "/clair.db"
	}

	zapLogger := utils.NewLogger("database")
	defer func() { _ = zapLogger.Sync() }()

	zapLogger.Info("Connecting to SQLite", zap.String("db_path", dbPath))

	logger := ZapToGormLogger(zapLogger)
	logger.SetAsDefault() // configure gorm to use our logger

	// TODO: configure gorm logger level based on env variable

	config := &gorm.Config{
		PrepareStmt: true,
		Logger:      logger.LogMode(gormlogger.Info),
	}

	db, err := gorm.Open(sqlite.Open(dbPath), config)
	if err != nil {
		zapLogger.Fatal("failed to connect database", zap.Error(err))
	}

	zapLogger.Info("Connected to SQLite database")

	// register models here
	db.AutoMigrate(&models.User{})

	return db, err
}

func New(ctx context.Context) (*gorm.DB, error) {
	connectionDriver := os.Getenv("DB_DRIVER")

	switch connectionDriver {
	//
	case "postgres":
		// return newPostgresConn(ctx)

	case "sqlite":
	default:
		return newSQLiteConn(ctx)
	}

	return nil, nil
}
