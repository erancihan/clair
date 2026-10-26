package cmd

import (
	"context"
	"time"

	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/utils"
	"github.com/go-co-op/gocron"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// SchedulerCmd runs the background jobs that keep the booking data honest.
//
// It is a separate process from the server because these jobs are about time
// passing, not about a request arriving: a hold that nobody paid for has to be
// released whether or not anyone is on the site.
func SchedulerCmd(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "scheduler",
		Args: cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := utils.NewLogger("scheduler")
			defer func() { _ = logger.Sync() }()

			// database configuration
			db, err := database.New(ctx)
			if err != nil {
				logger.Error("failed to connect database", zap.Error(err))
				return err
			}

			// scheduler configuration
			scheduler := gocron.NewScheduler(time.UTC)

			// The reaper is what makes a hold temporary. Without it an abandoned
			// checkout keeps a seat or a slot out of circulation forever, and the
			// per-buyer limit counts holds nobody is going to pay for.
			//
			// SingletonMode keeps a slow pass from overlapping the next tick.
			if _, err := scheduler.Every(1).Minute().SingletonMode().Do(func() {
				released, err := booking.ReapExpiredHolds(db, time.Now())
				if err != nil {
					logger.Error("reap expired holds", zap.Error(err))
				}
				if released > 0 {
					logger.Info("released expired holds", zap.Int("count", released))
				}
			}); err != nil {
				return err
			}

			// start scheduler
			scheduler.StartAsync()

			logger.Info("Scheduler started")

			// The scheduler has no HTTP surface. It runs until asked to stop.
			<-ctx.Done()
			scheduler.Stop()
			return nil
		},
	}

	return cmd
}
