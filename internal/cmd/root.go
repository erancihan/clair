package cmd

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"

	"github.com/erancihan/clair/internal/clair"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

func Execute(ctx context.Context) int {
	_ = godotenv.Load()

	clair.SetupSentry()

	profile := false

	rootCmd := &cobra.Command{
		Use: "clair",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if !profile {
				return nil
			}

			f, perr := os.Create("cpu.pprof")
			if perr != nil {
				return perr
			}

			_ = pprof.StartCPUProfile(f)

			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			if !profile {
				return nil
			}

			pprof.StopCPUProfile()

			f, perr := os.Create("mem.pprof")
			if perr != nil {
				return perr
			}
			defer f.Close()

			runtime.GC()

			err := pprof.WriteHeapProfile(f)
			return err
		},
	}

	rootCmd.PersistentFlags().BoolVarP(&profile, "profile", "P", false, "Enable CPU pprof")
	rootCmd.PersistentFlags().StringP("port", "p", "8080", "Port to listen on")

	rootCmd.AddCommand(ServerCmd(ctx))
	rootCmd.AddCommand(SchedulerCmd(ctx))

	go func() {
		port := rootCmd.PersistentFlags().Lookup("port").Value.String()

		_ = http.ListenAndServe(":"+port, nil)
	}()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return 1
	}

	return 0
}
