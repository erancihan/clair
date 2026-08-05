package database

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	kernel "github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database/models"
	"github.com/erancihan/clair/internal/server/booking"
	"github.com/erancihan/clair/internal/server/games"
	"github.com/erancihan/clair/internal/utils"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// New opens the connection pool and nothing else. It runs no DDL: schema work is
// a deliberate step (`clair migrate`), not a side effect of booting, so a rebuild
// or a restart never rewrites tables underneath a running deployment.
//
// Set DATABASE_AUTO_MIGRATE=true to fold Migrate back into startup. That is a
// convenience for a throwaway development database and a mistake for a shared
// one, which is why it is off unless asked for.
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

	if AutoMigrateEnabled() {
		zapLogger.Info("DATABASE_AUTO_MIGRATE is set, migrating on startup")

		if err := Migrate(db); err != nil {
			return nil, err
		}
	}

	return db, nil
}

// AutoMigrateEnabled reports whether startup should migrate. Anything strconv
// reads as true turns it on; an unset or unparsable value leaves it off, so a
// typo fails closed rather than silently altering a schema.
func AutoMigrateEnabled() bool {
	on, err := strconv.ParseBool(os.Getenv("DATABASE_AUTO_MIGRATE"))
	return err == nil && on
}

// Migrate brings the schema up to the shape the code expects: the model tables,
// then the booking notify triggers. Both halves are idempotent, so re-running
// costs nothing and changes nothing.
func Migrate(db *gorm.DB) error {
	// register models here
	if err := db.AutoMigrate(MigrationModels()...); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	// Inventory changes announce themselves on a Postgres channel, which is what
	// lets a seat map stream instead of poll. It lives in the database because
	// more than one process writes inventory.
	if err := kernel.InstallNotifyTriggers(db); err != nil {
		return fmt.Errorf("failed to install booking notify triggers: %w", err)
	}

	return nil
}

// MigrationModels is the full set of models AutoMigrate manages: the shared
// identity model, plus one Models() call per domain.
//
// Domains own their own list, so putting a table under migration is a change in
// the domain package and a single line here - never an edit to the AutoMigrate
// call itself, which is what would otherwise make three domains contend over one
// statement.
//
// The dependency runs database -> domains -> authentication. Domain packages may
// import the authentication layer; nothing imports internal/database, which is
// what keeps this direction free of cycles. Domain models themselves live in
// internal/database/models, one file per model, domain-prefixed.
func MigrationModels() []any {
	all := []any{
		// Owned by the shared authentication layer.
		&models.User{},
	}

	// ---- domains ----------------------------------------------------------
	// One line per domain.
	all = append(all, games.Models()...)
	all = append(all, booking.Models()...)

	return all
}
