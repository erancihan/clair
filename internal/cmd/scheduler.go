package cmd

import (
	"context"
	"net/http"
	"time"

	"github.com/erancihan/clair/internal/clair"
	"github.com/erancihan/clair/internal/utils"
	"github.com/go-co-op/gocron"
	"github.com/spf13/cobra"
)

func SchedulerCmd(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "scheduler",
		Args: cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := utils.NewLogger("scheduler")
			defer func() { _ = logger.Sync() }()

			// database configuration
			// TODO:

			// redis configuration
			// TODO:

			// scheduler configuration
			scheduler := gocron.NewScheduler(time.UTC)

			el := clair.NewEventLoop()
			defer el.Close()

			// configure scheduler
			scheduler.Every(5).Seconds().Do(func() { el.Loop() })

			// start scheduler
			scheduler.StartAsync()

			logger.Info("Scheduler started")

			srv := &http.Server{Addr: ":8080"}
			go func() { _ = srv.ListenAndServe() }()

			<-ctx.Done()

			scheduler.Stop()

			return nil
		},
	}

	return cmd
}
