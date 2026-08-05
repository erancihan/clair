package cmd

import (
	"context"

	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/utils"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// MigrateCmd applies the schema: the model tables plus the booking notify
// triggers. It is the only place that changes structure by default, which is
// what keeps a rebuild or a restart from touching a live database on its own.
// It is idempotent, so running it twice is harmless.
func MigrateCmd(ctx context.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply the database schema (idempotent)",
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := utils.NewLogger("migrate")
			defer func() { _ = logger.Sync() }()

			db, err := database.New(ctx)
			if err != nil {
				logger.Error("failed to connect database", zap.Error(err))
				return err
			}

			if err := database.Migrate(db); err != nil {
				logger.Error("migration failed", zap.Error(err))
				return err
			}

			logger.Info("migration complete")
			return nil
		},
	}
}
