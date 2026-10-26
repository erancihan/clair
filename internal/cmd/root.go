package cmd

import (
	"context"
	"log"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"runtime"
	"runtime/pprof"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

func Execute(ctx context.Context) int {
	_ = godotenv.Load()

	profile := false

	rootCmd := &cobra.Command{
		Use: "clair",
		// Usage text answers "you typed the flags wrong". It does not answer "the
		// port is taken", and printing it there buries the line that does.
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if !profile {
				return nil
			}

			// Only when asked, and on a mux of its own: the default mux is a
			// global anything can register on.
			mux := http.NewServeMux()
			mux.HandleFunc("/debug/pprof/", httppprof.Index)
			mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
			mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
			mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
			mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
			go func() {
				if err := http.ListenAndServe("localhost:6060", mux); err != nil {
					log.Printf("pprof listener: %v", err)
				}
			}()

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
			runtime.GC()

			if err := pprof.WriteHeapProfile(f); err != nil {
				_ = f.Close()
				return err
			}
			return f.Close()
		},
	}

	rootCmd.PersistentFlags().BoolVarP(&profile, "profile", "P", false, "Enable CPU pprof")

	rootCmd.AddCommand(ServerCmd(ctx))
	rootCmd.AddCommand(SchedulerCmd(ctx))
	rootCmd.AddCommand(MigrateCmd(ctx))
	rootCmd.AddCommand(SeedCmd(ctx))

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return 1
	}

	return 0
}
