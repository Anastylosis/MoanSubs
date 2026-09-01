package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/api"
	"github.com/Anastylosis/MoanSubs/internal/config"
	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/spf13/cobra"
)

// defaultListen matches PLAN.md Order of work step 2's serve wiring: "reads
// MOANSUBS_LISTEN (default :8080) and DATABASE_URL".
const defaultListen = ":8080"

// resolveRegistrationMode implements MOANSUBS_REGISTRATION's precedence
// over the deprecated MOANSUBS_OPEN_REGISTRATION alias (WP-C7a spec): the
// new variable wins whenever it's set; the old boolean maps
// true→open, false→closed for one release. usedLegacy tells the caller
// whether to print the one-line deprecation notice.
func resolveRegistrationMode(explicit, legacy string) (mode api.RegistrationMode, usedLegacy bool, err error) {
	if explicit != "" {
		mode, err = api.ParseRegistrationMode(explicit)
		if err != nil {
			return "", false, fmt.Errorf("invalid MOANSUBS_REGISTRATION %q: %w", explicit, err)
		}
		return mode, false, nil
	}
	if legacy != "" {
		b, err := strconv.ParseBool(legacy)
		if err != nil {
			return "", false, fmt.Errorf("invalid MOANSUBS_OPEN_REGISTRATION %q: %w", legacy, err)
		}
		if b {
			return api.RegistrationOpen, true, nil
		}
		return api.RegistrationClosed, true, nil
	}
	return api.RegistrationOpen, false, nil
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the moansubs HTTP server",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// The file fills in whatever the environment has not already said;
		// see internal/config for why it feeds the env layer rather than
		// replacing it. A path the operator named must exist; the default
		// one is allowed to be absent, since the server has always run on
		// environment alone.
		path, _ := cmd.Flags().GetString("config")
		explicit := cmd.Flags().Changed("config")
		if path == "" {
			path = config.DefaultPath
		}
		if err := config.Load(path, explicit); err != nil {
			return fmt.Errorf("moansubs serve: %w", err)
		}

		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return errors.New("moansubs serve: DATABASE_URL is not set (config file or environment)")
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
		// invite-only or fully closed sets MOANSUBS_REGISTRATION here
		// rather than in code.
		registration, legacyRegistration, err := resolveRegistrationMode(
			os.Getenv("MOANSUBS_REGISTRATION"), os.Getenv("MOANSUBS_OPEN_REGISTRATION"))
		if err != nil {
			return fmt.Errorf("moansubs serve: %w", err)
		}
		if legacyRegistration {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
				"moansubs serve: MOANSUBS_OPEN_REGISTRATION is deprecated; set MOANSUBS_REGISTRATION=open|invite|closed instead")
		}

		// The invite economy's three knobs (WP-C7c): initial codes a fresh
		// account starts with, how many visible uploads earn one more, and
		// the ceiling on codes sitting unused at once. 0 for
		// MOANSUBS_INVITES_PER_UPLOADS disables earning by upload entirely.
		invitesInitial := api.DefaultInvitesInitial
		if v := os.Getenv("MOANSUBS_INVITES_INITIAL"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_INVITES_INITIAL %q", v)
			}
			invitesInitial = n
		}
		invitesPerUploads := api.DefaultInvitesPerUploads
		if v := os.Getenv("MOANSUBS_INVITES_PER_UPLOADS"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_INVITES_PER_UPLOADS %q", v)
			}
			invitesPerUploads = n
		}
		invitesCap := api.DefaultInvitesCap
		if v := os.Getenv("MOANSUBS_INVITES_CAP"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_INVITES_CAP %q", v)
			}
			invitesCap = n
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

		// Per-account vote budget (WP-C3), same shape as the upload rate:
		// generous for a real person triaging their own downloads, tight
		// enough to stop a script grinding a track's score.
		voteRate := api.VoteRateLimitPerHour
		if v := os.Getenv("MOANSUBS_VOTE_RATE_PER_HOUR"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_VOTE_RATE_PER_HOUR %q", v)
			}
			voteRate = n
		}

		// Per-account fit-report budget (WP-fit), separate from voteRate so
		// one can't be exhausted by hammering the other.
		fitRate := api.FitRateLimitPerHour
		if v := os.Getenv("MOANSUBS_FIT_RATE_PER_HOUR"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_FIT_RATE_PER_HOUR %q", v)
			}
			fitRate = n
		}

		removalRate := api.RemovalRateLimitPerHour
		if v := os.Getenv("MOANSUBS_REMOVAL_RATE_PER_HOUR"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_REMOVAL_RATE_PER_HOUR %q", v)
			}
			removalRate = n
		}

		// Its own budget, separate from uploadRate (PLAN_1.md WP-R3).
		revisionRate := api.RevisionRateLimitPerHour
		if v := os.Getenv("MOANSUBS_REVISION_RATE_PER_HOUR"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_REVISION_RATE_PER_HOUR %q", v)
			}
			revisionRate = n
		}

		// Outside 0..1 is a startup error, not a silent clamp (PLAN_1.md).
		revisionMaxDivergence := api.DefaultRevisionMaxDivergence
		if v := os.Getenv("MOANSUBS_REVISION_MAX_DIVERGENCE"); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || f > 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_REVISION_MAX_DIVERGENCE %q (want 0..1)", v)
			}
			revisionMaxDivergence = f
		}

		revisionRetimeHint := true
		if v := os.Getenv("MOANSUBS_REVISION_RETIME_HINT"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_REVISION_RETIME_HINT %q", v)
			}
			revisionRetimeHint = b
		}

		// Per-account metadata-contribution budget: each entry costs a
		// derivation, and a grouped release derives its whole work, so this
		// is bounded separately from the upload budget rather than sharing
		// it -- someone with no subtitle to give should still be able to
		// name a library's worth of scenes.
		metadataRate := api.MetadataRateLimitPerHour
		if v := os.Getenv("MOANSUBS_METADATA_RATE_PER_HOUR"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_METADATA_RATE_PER_HOUR %q", v)
			}
			metadataRate = n
		}

		// Offer the front page to crawlers while the catalogue stays
		// unlisted -- the launch posture for a node whose releases are
		// still mostly named from filenames.
		indexFrontPage := false
		if v := os.Getenv("MOANSUBS_INDEX_FRONT_PAGE"); v != "" {
			b, perr := strconv.ParseBool(v)
			if perr != nil {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_INDEX_FRONT_PAGE %q", v)
			}
			indexFrontPage = b
		}

		// Auto-confirm, off by default. Pinning is what opens a page to
		// crawlers, so switching this on is an operator saying they stand
		// behind the accounts they have marked trusted -- and it does
		// nothing at all until at least one is (`moansubs account trust`).
		autoConfirm := false
		if v := os.Getenv("MOANSUBS_AUTOCONFIRM"); v != "" {
			b, perr := strconv.ParseBool(v)
			if perr != nil {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_AUTOCONFIRM %q", v)
			}
			autoConfirm = b
		}

		// Which stash-boxes may pin a name without a moderator. Narrower
		// than MOANSUBS_STASH_ENDPOINTS deliberately: a node can accept ids
		// from a broad, loosely-curated database for matching without
		// letting them publish a name unreviewed.
		autoConfirmEndpoints, err := api.ParseAutoConfirmEndpoints(os.Getenv("MOANSUBS_AUTOCONFIRM_ENDPOINTS"))
		if err != nil {
			return fmt.Errorf("moansubs serve: invalid MOANSUBS_AUTOCONFIRM_ENDPOINTS: %w", err)
		}

		// This node's origin as visitors reach it, for the absolute URLs
		// the sitemap protocol and Open Graph both require. Unset derives
		// it per request from Host, which is right until a node answers to
		// more than one name.
		publicURL := strings.TrimSpace(os.Getenv("MOANSUBS_PUBLIC_URL"))
		if publicURL != "" {
			u, perr := url.Parse(publicURL)
			if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_PUBLIC_URL %q: want an absolute http(s) URL", publicURL)
			}
			publicURL = strings.TrimSuffix(u.Scheme+"://"+u.Host, "/")
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

		// MOANSUBS_TOKEN_KEY (WP-C8): AES-256-GCM key for accounts.token_enc,
		// 64 hex characters (32 bytes). Validated before store.Open so a
		// misconfigured key fails startup immediately rather than silently
		// leaving every account's token_enc NULL.
		tokenKey, err := tokenKeyFromEnv()
		if err != nil {
			return fmt.Errorf("moansubs serve: %w", err)
		}

		// The first-run admin bootstrap (WP-C8): MOANSUBS_ADMIN_NAME
		// overrides the default name "admin"; MOANSUBS_BOOTSTRAP_ADMIN=false
		// disables the automatic step entirely (for an operator who'd
		// rather run `moansubs admin bootstrap` by hand than have
		// credentials land in container logs).
		adminName := os.Getenv("MOANSUBS_ADMIN_NAME")
		bootstrapAdminEnabled := true
		if v := os.Getenv("MOANSUBS_BOOTSTRAP_ADMIN"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_BOOTSTRAP_ADMIN %q", v)
			}
			bootstrapAdminEnabled = b
		}

		// The 18+ click-through (WP-C10) is on by default for an
		// adult-focused node; MOANSUBS_AGE_GATE=false is for an operator
		// who handles that requirement some other way (e.g. a reverse
		// proxy already gating the whole node).
		ageGate := true
		if v := os.Getenv("MOANSUBS_AGE_GATE"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_AGE_GATE %q", v)
			}
			ageGate = b
		}

		// Search-engine indexing is off by default: /robots.txt disallows
		// everything and the catalogue pages send noindex, which is what
		// this node has always done. MOANSUBS_INDEXABLE=true opens the
		// public catalogue and lets the major crawlers past the age gate —
		// see MANUAL.md, including what that second part trades away.
		indexable := false
		if v := os.Getenv("MOANSUBS_INDEXABLE"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_INDEXABLE %q", v)
			}
			indexable = b
		}

		// The accent colour every page is built around (MOANSUBS_ACCENT).
		// Only hue and saturation are taken from it: the server re-derives
		// a lightness per theme so the result stays legible on both, which
		// is why an operator cannot configure an unreadable site here.
		theme, err := api.ParseAccent(os.Getenv("MOANSUBS_ACCENT"))
		if err != nil {
			return fmt.Errorf("moansubs serve: invalid MOANSUBS_ACCENT: %w", err)
		}

		// The optional analytics tag (MOANSUBS_ANALYTICS_SCRIPT plus
		// MOANSUBS_ANALYTICS_WEBSITE_ID). Unset on both leaves the node
		// with no tracker and the unwidened CSP; setting one without the
		// other is an error rather than a default, since the resulting tag
		// would load and silently record nothing.
		analytics, err := api.ParseAnalytics(
			os.Getenv("MOANSUBS_ANALYTICS_SCRIPT"), os.Getenv("MOANSUBS_ANALYTICS_WEBSITE_ID"))
		if err != nil {
			return fmt.Errorf("moansubs serve: %w", err)
		}

		// The per-query statement_timeout (WP-P9): a fuzzy phash lookup
		// or CreatorNames' DISTINCT+unnest is a full-table scan an
		// anonymous caller can trigger, and without a cap a burst of
		// slow requests pins every pooled connection with nothing able
		// to kill them. Unset keeps store.DefaultStatementTimeout
		// (30s); 0 explicitly removes the limit; the DSN's own
		// statement_timeout param, if present, always wins over this
		// (see MANUAL.md).
		statementTimeout := store.DefaultStatementTimeout
		if v := os.Getenv("MOANSUBS_STATEMENT_TIMEOUT"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_STATEMENT_TIMEOUT %q", v)
			}
			statementTimeout = d
		}

		// The stash-box endpoint allow-list (WP-R6): uploads naming an
		// endpoint outside it are rejected with 400, defense in depth
		// against a rogue uploader attaching an arbitrary URL the UI would
		// otherwise render as a link. Unset keeps api.DefaultStashEndpoints
		// (stashdb.org, fansdb.cc); the single value "*" accepts any
		// http(s) endpoint.
		stashEndpoints := api.DefaultStashEndpoints
		if v := os.Getenv("MOANSUBS_STASH_ENDPOINTS"); v != "" {
			parsed, err := api.ParseStashEndpoints(v)
			if err != nil {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_STASH_ENDPOINTS: %w", err)
			}
			stashEndpoints = parsed
		}

		contactEmail := os.Getenv("MOANSUBS_CONTACT_EMAIL")
		contactEnabled := false
		if v := os.Getenv("MOANSUBS_CONTACT"); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("moansubs serve: invalid MOANSUBS_CONTACT %q", v)
			}
			contactEnabled = b
		}
		contactNote := os.Getenv("MOANSUBS_CONTACT_NOTE")

		// Cancelled on SIGINT/SIGTERM, which also starts the graceful
		// shutdown below.
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		// store.Open runs pending migrations before the server accepts any
		// traffic (PLAN.md: "runs migrations on startup").
		openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		s, err := store.Open(openCtx, dsn, store.Options{StatementTimeout: statementTimeout})
		cancel()
		if err != nil {
			return fmt.Errorf("moansubs serve: %w", err)
		}
		defer s.Close()
		s.SetTokenKey(tokenKey)

		bootstrapCtx, bootstrapCancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = bootstrapAdmin(bootstrapCtx, s, adminName, bootstrapAdminEnabled, cmd.OutOrStdout())
		bootstrapCancel()
		if err != nil {
			return fmt.Errorf("moansubs serve: %w", err)
		}

		apiSrv := api.NewServer(s)
		apiSrv.Limiter = api.NewRateLimiter(uploadRate)
		apiSrv.RegisterLimiter = api.NewRateLimiter(registerRate)
		apiSrv.LoginLimiter = api.NewRateLimiter(loginRate)
		apiSrv.SessionTTL = sessionTTL
		apiSrv.SearchLimiter = api.NewRateLimiterPerMinute(searchRate)
		apiSrv.Registration = registration
		apiSrv.InvitesInitial = invitesInitial
		apiSrv.InvitesPerUploads = invitesPerUploads
		apiSrv.InvitesCap = invitesCap
		apiSrv.DumpURL = dumpURL
		apiSrv.VoteLimiter = api.NewRateLimiter(voteRate)
		apiSrv.FitLimiter = api.NewRateLimiter(fitRate)
		apiSrv.RemovalLimiter = api.NewRateLimiter(removalRate)
		apiSrv.MetadataLimiter = api.NewRateLimiter(metadataRate)
		apiSrv.RevisionLimiter = api.NewRateLimiter(revisionRate)
		apiSrv.RevisionMaxDivergence = revisionMaxDivergence
		apiSrv.RevisionRetimeHint = revisionRetimeHint
		apiSrv.PublicURL = publicURL
		apiSrv.AutoConfirm = autoConfirm
		apiSrv.IndexFrontPage = indexFrontPage
		apiSrv.AutoConfirmEndpoints = autoConfirmEndpoints
		// version is main.go's ldflags-stamped build var ("dev" when built
		// without them); GET /api/v1/version reports whatever this process
		// actually is, the same source --version already uses.
		apiSrv.Version = version
		apiSrv.TrustedProxyCIDRs = trustedProxyCIDRs
		apiSrv.AgeGate = ageGate
		apiSrv.StashEndpoints = stashEndpoints
		apiSrv.Analytics = analytics
		apiSrv.Indexable = indexable
		apiSrv.Theme = theme
		apiSrv.ContactEmail = contactEmail
		apiSrv.ContactEnabled = contactEnabled
		apiSrv.ContactNote = contactNote
		srv := &http.Server{
			Addr:    listen,
			Handler: api.NewMux(apiSrv),
			// Exposed to the internet: a client that opens a connection
			// and trickles bytes must not hold a goroutine forever. Reads
			// are bounded generously enough for a 2 MiB upload on a slow
			// link; writes cover the largest dump-free response (a batch
			// lookup); idle keep-alives are cut after two minutes.
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    64 << 10,
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
				listen, registration)
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
	serveCmd.Flags().String("config", "",
		"path to a YAML config file (default "+config.DefaultPath+"; environment variables still win over it)")
	rootCmd.AddCommand(serveCmd)
}

// adminPasswordAlphabet mirrors invite.go's inviteCodeAlphabet reasoning —
// no look-alike characters — for a password an operator has to actually
// type once, off a container log, before changing it.
const adminPasswordAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

const adminPasswordLen = 24

// randomPassword returns an n-character password drawn uniformly from
// adminPasswordAlphabet via crypto/rand, using rejection sampling (same
// technique as internal/store/invite.go's generateInviteCode) so the
// alphabet's size not evenly dividing 256 doesn't bias the low end of it.
func randomPassword(n int) (string, error) {
	limit := 256 - (256 % len(adminPasswordAlphabet))
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if int(buf[0]) >= limit {
			continue
		}
		out = append(out, adminPasswordAlphabet[int(buf[0])%len(adminPasswordAlphabet)])
	}
	return string(out), nil
}

// bootstrapAdmin creates the node's first admin account when none exists
// yet (WP-C8). The trigger is "no admin exists", not "first run" — a node
// that already has an admin (from a previous start, or hand-created with
// `account role`) always no-ops here, silently and without a query beyond
// the one existence check. serve's RunE calls this once after migrations,
// honoring MOANSUBS_BOOTSTRAP_ADMIN via the enabled parameter;
// `moansubs admin bootstrap` (admin.go) calls it on demand with
// enabled=true unconditionally, for an operator who set
// MOANSUBS_BOOTSTRAP_ADMIN=false specifically to avoid credentials in
// container logs and would rather mint them via `exec` instead.
//
// name is MOANSUBS_ADMIN_NAME's value, or "" for the default ("admin").
// The generated password and API token are printed to out with fmt.Fprintf
// — not the logger — exactly once, so they appear as plain lines rather
// than a leveled/timestamped log entry, and never again after this call
// returns created=true.
func bootstrapAdmin(ctx context.Context, s *store.Store, name string, enabled bool, out io.Writer) (created bool, err error) {
	if !enabled {
		return false, nil
	}
	if name == "" {
		name = "admin"
	}

	hasAdmin, err := s.HasAdmin(ctx)
	if err != nil {
		return false, fmt.Errorf("bootstrapAdmin: %w", err)
	}
	if hasAdmin {
		return false, nil
	}

	if _, err := s.GetAccountByName(ctx, name); err == nil {
		// A non-admin account already holds this name — an admin account
		// would have been caught by HasAdmin above, so this is a genuine
		// collision, not the account we're about to create. Refuse rather
		// than silently promoting or overwriting someone else's account.
		return false, fmt.Errorf(
			"bootstrapAdmin: account %q already exists and is not an admin; run `moansubs account role %s admin` instead",
			name, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, fmt.Errorf("bootstrapAdmin: %w", err)
	}

	password, err := randomPassword(adminPasswordLen)
	if err != nil {
		return false, fmt.Errorf("bootstrapAdmin: generating password: %w", err)
	}
	id, token, err := s.CreateAccountWithPassword(ctx, name, password)
	if err != nil {
		return false, fmt.Errorf("bootstrapAdmin: %w", err)
	}
	if err := s.SetAccountRole(ctx, name, "admin"); err != nil {
		return false, fmt.Errorf("bootstrapAdmin: %w", err)
	}

	_, _ = fmt.Fprintf(out, "moansubs: created initial admin account %q (id %d)\n", name, id)
	_, _ = fmt.Fprintf(out, "  password: %s\n", password)
	_, _ = fmt.Fprintf(out, "  API token: %s\n", token)
	_, _ = fmt.Fprintln(out, "  log in and change the password at /me soon: this line stays in the container logs")
	return true, nil
}
