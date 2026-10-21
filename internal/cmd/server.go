package cmd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

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
				return err
			}

			// ValKey configuration
			valkey := utils.NewValKeyClient(ctx)
			defer func() {
				if valkey != nil {
					valkey.Close()
				}
			}()

			bnd := server.NewBackEnd(ctx, logger, valkey, db)
			srv := bnd.Server(port)

			// Bind before announcing. Serving in a goroutine and logging "started"
			// regardless means a port already held by something else produces a
			// process that claims to be up and answers nothing — and everything
			// downstream believes the log.
			listener, err := net.Listen("tcp", srv.Addr)
			if err != nil {
				logger.Error("failed to listen", zap.String("addr", srv.Addr), zap.Error(err))
				return err
			}

			logger.Info("Backend server started", zap.Int("port", port))

			// ErrServerClosed is the ordinary answer to Shutdown, not a failure.
			// Anything else killed the server and has to end the command.
			serveErr := make(chan error, 1)
			go func() {
				if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					serveErr <- err
				}
			}()

			select {
			case err := <-serveErr:
				logger.Error("server stopped", zap.Error(err))
				return err
			case <-ctx.Done():
			}

			// Shutdown needs a context of its own: ctx is already cancelled by the
			// time we get here, and handing it over would abandon in-flight
			// requests instead of draining them.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				logger.Error("shutdown", zap.Error(err))
			}

			return nil
		},
	}

	return cmd
}
