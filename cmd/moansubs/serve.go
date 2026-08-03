package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Wasylq/moansubs/internal/api"
	"github.com/Wasylq/moansubs/internal/store"
	"github.com/spf13/cobra"
)

// defaultListen matches PLAN.md Order of work step 2's serve wiring: "reads
// MOANSUBS_LISTEN (default :8080) and DATABASE_URL".
const defaultListen = ":8080"

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the moansubs HTTP server",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return errors.New("moansubs serve: DATABASE_URL is not set")
		}
		listen := os.Getenv("MOANSUBS_LISTEN")
		if listen == "" {
			listen = defaultListen
		}

		// Cancelled on SIGINT/SIGTERM, which also starts the graceful
		// shutdown below.
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		// store.Open runs pending migrations before the server accepts any
		// traffic (PLAN.md: "runs migrations on startup").
		openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		s, err := store.Open(openCtx, dsn)
		cancel()
		if err != nil {
			return fmt.Errorf("moansubs serve: %w", err)
		}
		defer s.Close()

		srv := &http.Server{
			Addr:    listen,
			Handler: api.NewMux(api.NewServer(s)),
		}

		errCh := make(chan error, 1)
		go func() {
			fmt.Fprintf(cmd.OutOrStdout(), "moansubs serve: listening on %s\n", listen)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
				return
			}
			errCh <- nil
		}()

		select {
		case err := <-errCh:
			if err != nil {
				return fmt.Errorf("moansubs serve: %w", err)
			}
			return nil
		case <-ctx.Done():
			fmt.Fprintln(cmd.OutOrStdout(), "moansubs serve: shutting down")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("moansubs serve: graceful shutdown: %w", err)
			}
			return nil
		}
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
