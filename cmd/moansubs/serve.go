package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/api"
	"github.com/Anastylosis/MoanSubs/internal/store"
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

		// The upload limit's safe default (30/h per token) assumes strangers
		// on a public node; the operator seeding their own node from a large
		// library needs to raise it, so it's env-tunable rather than
		// hardcoded (a 1,000-file seed at 30/h would take a day and a half).
		uploadRate := api.UploadRateLimitPerHour
		if v := os.Getenv("MOANSUBS_UPLOAD_RATE_PER_HOUR"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_UPLOAD_RATE_PER_HOUR %q", v)
			}
			uploadRate = n
		}

		// Registration is open by default; a node that wants to stay
		// invite-only closes it here rather than in code.
		openRegistration := true
		if v := os.Getenv("MOANSUBS_OPEN_REGISTRATION"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_OPEN_REGISTRATION %q", v)
			}
			openRegistration = b
		}

		registerRate := api.RegisterRateLimitPerHour
		if v := os.Getenv("MOANSUBS_REGISTER_RATE_PER_HOUR"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_REGISTER_RATE_PER_HOUR %q", v)
			}
			registerRate = n
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

		apiSrv := api.NewServer(s)
		apiSrv.Limiter = api.NewRateLimiter(uploadRate)
		apiSrv.RegisterLimiter = api.NewRateLimiter(registerRate)
		apiSrv.OpenRegistration = openRegistration
		srv := &http.Server{
			Addr:    listen,
			Handler: api.NewMux(apiSrv),
		}

		errCh := make(chan error, 1)
		go func() {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "moansubs serve: listening on %s (registration %s)\n",
				listen, map[bool]string{true: "open", false: "closed"}[openRegistration])
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
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "moansubs serve: shutting down")
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
