package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
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

		loginRate := api.LoginRateLimitPerHour
		if v := os.Getenv("MOANSUBS_LOGIN_RATE_PER_HOUR"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_LOGIN_RATE_PER_HOUR %q", v)
			}
			loginRate = n
		}

		sessionTTL := api.DefaultSessionTTL
		if v := os.Getenv("MOANSUBS_SESSION_TTL"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_SESSION_TTL %q", v)
			}
			sessionTTL = d
		}
		// GET /search is the only catalogue page where a stranger makes the
		// database do real work (WP-C2), so it gets its own budget rather
		// than sharing the generous browse-a-scene-wall lookup limit.
		searchRate := api.SearchRateLimitPerMinute
		if v := os.Getenv("MOANSUBS_SEARCH_RATE_PER_MINUTE"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_SEARCH_RATE_PER_MINUTE %q", v)
			}
			searchRate = n
		}

		// Unset (the default) hides the front page's dump link entirely —
		// publishing a dump is an out-of-band operator choice (WP-C2,
		// deploy/README.md), not something this server does on its own.
		dumpURL := os.Getenv("MOANSUBS_DUMP_URL")

		// Unset trusts no proxy, so clientIP ignores X-Forwarded-For and
		// always uses RemoteAddr -- see internal/api/ratelimit.go.
		var trustedProxyCIDRs []*net.IPNet
		if v := os.Getenv("MOANSUBS_TRUSTED_PROXY_CIDRS"); v != "" {
			for _, part := range strings.Split(v, ",") {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				_, cidr, err := net.ParseCIDR(part)
				if err != nil {
					return fmt.Errorf("moansubs serve: invalid MOANSUBS_TRUSTED_PROXY_CIDRS %q: %w", part, err)
				}
				trustedProxyCIDRs = append(trustedProxyCIDRs, cidr)
			}
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
		apiSrv.LoginLimiter = api.NewRateLimiter(loginRate)
		apiSrv.SessionTTL = sessionTTL
		apiSrv.SearchLimiter = api.NewRateLimiterPerMinute(searchRate)
		apiSrv.OpenRegistration = openRegistration
		apiSrv.DumpURL = dumpURL
		// version is main.go's ldflags-stamped build var ("dev" when built
		// without them); GET /api/v1/version reports whatever this process
		// actually is, the same source --version already uses.
		apiSrv.Version = version
		apiSrv.TrustedProxyCIDRs = trustedProxyCIDRs
		srv := &http.Server{
			Addr:    listen,
			Handler: api.NewMux(apiSrv),
		}

		// Stats.Run flushes the in-memory lookup counters to the stats
		// table every api.StatsFlushInterval, and once more when ctx is
		// cancelled (WP-A2). A WaitGroup so both exit paths below can wait
		// for that final flush to land before returning — s.Close() is
		// deferred above and would otherwise race closing the pool against
		// a flush still in flight.
		var statsWG sync.WaitGroup
		statsWG.Add(1)
		go func() {
			defer statsWG.Done()
			apiSrv.Stats.Run(ctx, api.StatsFlushInterval)
		}()

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
			// The server exited on its own (crash or otherwise): cancel ctx
			// so Stats.Run's shutdown branch still fires and flushes
			// whatever it's holding, the same as a signal-triggered exit.
			stop()
			statsWG.Wait()
			if err != nil {
				return fmt.Errorf("moansubs serve: %w", err)
			}
			return nil
		case <-ctx.Done():
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "moansubs serve: shutting down")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			shutdownErr := srv.Shutdown(shutdownCtx)
			statsWG.Wait()
			if shutdownErr != nil {
				return fmt.Errorf("moansubs serve: graceful shutdown: %w", shutdownErr)
			}
			return nil
		}
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
