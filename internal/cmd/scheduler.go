package cmd

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/erancihan/clair/internal/aurenor"
	"github.com/erancihan/clair/internal/booking"
	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/simulator"
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

			// The simulator's guard runs before anything connects, so a scheduler
			// pointed at the wrong database for it never opens a connection.
			simEnabled := os.Getenv("SIM_ENABLED") == "true"
			if simEnabled {
				if err := simulator.Guard(os.Getenv("DATABASE_URL"), os.Getenv("APP_ENV"),
					os.Getenv("SIM_ALLOW_PRODUCTION") == "true"); err != nil {
					logger.Error("simulator refused", zap.Error(err))
					return err
				}
			}

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

			// The theater calendar rolls: every hour, create the showings that have
			// come into the window. The first run is immediate, so a scheduler
			// started on a stale database fills the board without waiting.
			if _, err := scheduler.Every(1).Hour().SingletonMode().Do(func() {
				created, err := aurenor.ExtendCalendar(db, time.Now())
				if err != nil {
					logger.Error("extend theater calendar", zap.Error(err))
				}
				if created > 0 {
					logger.Info("created showings", zap.Int("count", created))
				}
			}); err != nil {
				return err
			}

			// Retention deletes booking history past its business time. It is off
			// unless RETENTION_ENABLED=true, because it is the one job that
			// destroys data: whoever runs the system decides to turn it on.
			if retentionEnabled() {
				keep := retentionPeriod()
				if _, err := scheduler.Every(1).Day().At("03:30").SingletonMode().Do(func() {
					now := time.Now()
					rep, err := booking.PurgeBefore(db, now.Add(-keep), now)
					if err != nil {
						logger.Error("purge booking history", zap.Error(err))
					}
					if rep.Total() > 0 || rep.Events > 0 {
						logger.Info("purged booking history",
							zap.Int("events", rep.Events), zap.Int("slots", rep.Slots),
							zap.Int("orders", rep.Orders), zap.Int("rows", rep.Total()))
					}
				}); err != nil {
					return err
				}
				logger.Info("retention enabled", zap.Duration("keep", keep))
			}

			// The simulator books the demo showings and slots so the pages look
			// lived in. Off unless SIM_ENABLED=true, and refused outright on a
			// database that looks like production unless SIM_ALLOW_PRODUCTION=true:
			// a scheduler asked to simulate against the wrong database exits
			// rather than starting without it.
			if simEnabled {
				cfg := simulator.DefaultConfig()
				if v, err := strconv.ParseFloat(os.Getenv("SIM_TARGET_OCCUPANCY"), 64); err == nil && v > 0 && v <= 1 {
					cfg.TargetOccupancy = v
				}
				runner := simulator.NewRunner(db, simulator.New(db, cfg, uint64(time.Now().UnixNano())))
				defer func() { _ = runner.Close() }()

				if _, err := scheduler.Every(1).Minute().SingletonMode().Do(func() {
					rep, ran, err := runner.Tick(ctx, time.Now())
					if err != nil {
						logger.Error("simulator tick", zap.Error(err))
					}
					if ran && (rep.TicketsSold > 0 || rep.AppointmentsBooked > 0 || rep.OrdersCancelled > 0) {
						logger.Info("simulator tick",
							zap.Int("tickets", rep.TicketsSold), zap.Int("appointments", rep.AppointmentsBooked),
							zap.Int("cancelled", rep.OrdersCancelled+rep.AppointmentsDropped),
							zap.Int("abandoned", rep.HoldsAbandoned))
					}
				}); err != nil {
					return err
				}
				logger.Info("simulator enabled", zap.Float64("target_occupancy", cfg.TargetOccupancy))
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

// retentionEnabled reads RETENTION_ENABLED. Only "true" turns the purge on.
func retentionEnabled() bool {
	return os.Getenv("RETENTION_ENABLED") == "true"
}

// retentionPeriod reads RETENTION_DAYS, falling back to the booking service's
// default when it is unset or not a positive number.
func retentionPeriod() time.Duration {
	if days, err := strconv.Atoi(os.Getenv("RETENTION_DAYS")); err == nil && days > 0 {
		return time.Duration(days) * 24 * time.Hour
	}
	return booking.DefaultRetention
}
