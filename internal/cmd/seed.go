package cmd

import (
	"context"
	"os"
	"time"

	"github.com/erancihan/clair/internal/aurenor"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/utils"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// SeedCmd populates the database with the Aurenor booking-simulation catalog. It
// is idempotent: re-running changes no row counts.
func SeedCmd(ctx context.Context) *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: "Seed the Aurenor booking-simulation data (idempotent)",
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := utils.NewLogger("seed")
			defer func() { _ = logger.Sync() }()

			db, err := database.New(ctx)
			if err != nil {
				logger.Error("failed to connect database", zap.Error(err))
				return err
			}

			email := os.Getenv("SEED_HOST_EMAIL")
			if email == "" {
				email = aurenor.DefaultHostEmail
			}

			rep, err := aurenor.Seed(db, time.Now(), email)
			if err != nil {
				logger.Error("seed failed", zap.Error(err))
				return err
			}

			logger.Info("seed complete",
				zap.String("host", email),
				zap.Bool("host_created", rep.HostCreated),
				zap.Int("schedules_created", rep.SchedulesCreated),
				zap.Int("schedules_skipped", rep.SchedulesSkipped),
				zap.Int("rules_created", rep.RulesCreated),
				zap.Int("events_created", rep.EventsCreated),
				zap.Int("events_skipped", rep.EventsSkipped),
			)
			return nil
		},
	}
}
