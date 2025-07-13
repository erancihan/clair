package cmd

import (
	"context"
	"os"
	"strconv"

	"github.com/erancihan/clair/internal/database"
	"github.com/erancihan/clair/internal/server"
	"github.com/erancihan/clair/internal/utils"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func ServerCmd(ctx context.Context) *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:  "server",
		Args: cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			port = 4000
			// port configuration
			if os.Getenv("SERVER_PORT") != "" {
				port, _ = strconv.Atoi(os.Getenv("SERVER_PORT"))
			}

			// logger configuration
			logger := utils.NewLogger("server")
			defer func() { _ = logger.Sync() }()

			// database configuration
			db, err := database.New(ctx)
			if err != nil {
				logger.Error("failed to connect database", zap.Error(err))
				return nil
			}
			// TODO: gorm defer

			// ValKey configuration
			valkey := utils.NewValKeyClient(ctx)
			defer func() {
				if valkey != nil {
					valkey.Close()
				}
			}()

			bnd := server.NewBackEnd(ctx, logger, valkey, db)
			srv := bnd.Server(port)

			go func() {
				_ = srv.ListenAndServe()
			}()

			logger.Info("Backend server started", zap.Int("port", port))

			<-ctx.Done()

			_ = srv.Shutdown(ctx)

			return nil
		},
	}

	return cmd
}
