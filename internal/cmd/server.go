package cmd

import (
	"context"
	"os"
	"strconv"

	"github.com/erancihan/clair/internal/server"
	"github.com/erancihan/clair/internal/utils"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func ServerCmd(ctx context.Context) *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:  "api",
		Args: cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			port = 4000
			// port configuration
			if os.Getenv("PORT") != "" {
				port, _ = strconv.Atoi(os.Getenv("PORT"))
			}

			// logger configuration
			logger := utils.NewLogger("api")
			defer func() { _ = logger.Sync() }()

			// database configuration
			// TODO:

			// redis configuration
			// TODO:

			bnd := server.NewBackEnd(ctx, nil, nil, nil)
			srv := bnd.Server(port)

			go func() {
				_ = srv.ListenAndServe()
			}()

			logger.Info("API server started", zap.Int("port", port))

			<-ctx.Done()

			_ = srv.Shutdown(ctx)

			return nil
		},
	}

	return cmd
}
