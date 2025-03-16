package database

import (
	"context"
	"os"

	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/utils"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func New(ctx context.Context) (*gorm.DB, error) {
	dbPath := ".opt/clair.db"
	if os.Getenv("DB_PATH") != "" {
		dbPath = os.Getenv("DB_PATH")
	}

	logger := ZapToGormLogger(utils.NewLogger("database"))
	logger.SetAsDefault() // configure gorm to use our logger

	// TODO: configure gorm logger level based on env variable

	config := &gorm.Config{
		PrepareStmt: true,
		Logger:      logger.LogMode(gormlogger.Info),
	}

	db, err := gorm.Open(sqlite.Open(dbPath), config)
	if err != nil {
		panic("failed to connect database")
	}

	// register models here
	db.AutoMigrate(&models.User{})

	return db, err
}
