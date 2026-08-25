package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/api"
	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/stashbox"
	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/spf13/cobra"
)

var stashboxCmd = &cobra.Command{
	Use:   "stashbox",
	Short: "Operator-side stash-box tools",
}

var (
	backfillEndpoint string
	backfillLimit    int
	backfillDelay    time.Duration
	backfillDryRun   bool
	backfillKey      string
	backfillAs       string
)

type backfillOptions struct {
	endpoint   string
	limit      int
	delay      time.Duration
	dryRun     bool
	proposer   *int64
	autoPin    []string // nil: auto-confirm off
	maxRetries int
}

type backfillStats struct {
	fingerprint, proposed, none, errored int
}

var stashboxBackfillCmd = &cobra.Command{
	Use:   "backfill [--endpoint URL] [--limit N] [--delay 1s] [--dry-run] [--as NAME]",
	Short: "Attach stash-box ids to releases that have none, using your own key",
	Long: "Sweeps releases without a stash-box id. A fingerprint hit attaches the id;\n" +
		"a title+date hit only becomes a metadata proposal from --as, a trusted\n" +
		"account, so the auto-confirm rules decide what reaches a crawler.\n" +
		"The key (--key or MOANSUBS_STASHBOX_KEY) is yours, used for this run and\n" +
		"never stored.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		key := backfillKey
		if key == "" {
			key = os.Getenv("MOANSUBS_STASHBOX_KEY")
		}
		if key == "" {
			return errors.New("moansubs stashbox backfill: no key; pass --key or set MOANSUBS_STASHBOX_KEY")
		}
		accepted, err := api.ParseStashEndpoints(os.Getenv("MOANSUBS_STASH_ENDPOINTS"))
		if err != nil {
			return fmt.Errorf("moansubs stashbox backfill: invalid MOANSUBS_STASH_ENDPOINTS: %w", err)
		}
		endpoints, err := backfillEndpoints(accepted, backfillEndpoint)
		if err != nil {
			return fmt.Errorf("moansubs stashbox backfill: %w", err)
		}

		s, _, cancel, err := openStore(cmd, "stashbox backfill")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()
		// openStore's 30 s budget suits one query, not a sweep paced at one
		// request per second.
		ctx, cancelSweep := context.WithCancel(cmd.Context())
		defer cancelSweep()

		opts := backfillOptions{limit: backfillLimit, delay: backfillDelay, dryRun: backfillDryRun, maxRetries: 3}
		if backfillAs != "" {
			id, trusted, disabled, err := s.AccountStanding(ctx, backfillAs)
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs stashbox backfill: no account named %q", backfillAs)
			}
			if err != nil {
				return fmt.Errorf("moansubs stashbox backfill: %w", err)
			}
			if !trusted || disabled {
				return fmt.Errorf("moansubs stashbox backfill: account %q must be trusted (moansubs account trust) and enabled to propose metadata", backfillAs)
			}
			opts.proposer = &id
		}
		if v := os.Getenv("MOANSUBS_AUTOCONFIRM"); v != "" {
			on, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("moansubs stashbox backfill: invalid MOANSUBS_AUTOCONFIRM %q", v)
			}
			if on {
				pin, err := api.ParseAutoConfirmEndpoints(os.Getenv("MOANSUBS_AUTOCONFIRM_ENDPOINTS"))
				if err != nil {
					return fmt.Errorf("moansubs stashbox backfill: invalid MOANSUBS_AUTOCONFIRM_ENDPOINTS: %w", err)
				}
				opts.autoPin = pin
			}
		}

		out := cmd.OutOrStdout()
		for _, ep := range endpoints {
			opts.endpoint = ep
			st, err := runBackfill(ctx, s, stashbox.New(ep, key), opts, out)
			_, _ = fmt.Fprintf(out, "%s: %d attached by fingerprint, %d proposed by name, %d not found, %d errors%s\n",
				ep, st.fingerprint, st.proposed, st.none, st.errored, dryRunSuffix(opts.dryRun))
			if err != nil {
				return fmt.Errorf("moansubs stashbox backfill: %w", err)
			}
		}
		return nil
	},
}

func releaseLabel(rel store.Release) string {
	if rel.Title != nil && *rel.Title != "" {
		return fmt.Sprintf("release %d %q", rel.ID, *rel.Title)
	}
	return fmt.Sprintf("release %d (untitled)", rel.ID)
}

// boxLabel is the endpoint's host: "stashdb.org", not the GraphQL URL.
func boxLabel(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return endpoint
}

func dryRunSuffix(dry bool) string {
	if dry {
		return " (dry run, nothing written)"
	}
	return ""
}

func backfillEndpoints(accepted []string, only string) ([]string, error) {
	wildcard := len(accepted) == 1 && accepted[0] == "*"
	if only == "" {
		if wildcard {
			return nil, errors.New("MOANSUBS_STASH_ENDPOINTS is *; pass --endpoint to say which box to sweep")
		}
		return accepted, nil
	}
	norm, err := hash.NormalizeStashEndpoint(only)
	if err != nil {
		return nil, err
	}
	if !wildcard {
		found := false
		for _, a := range accepted {
			found = found || a == norm
		}
		if !found {
			return nil, fmt.Errorf("endpoint %s is not in this node's accepted stash-box list (MOANSUBS_STASH_ENDPOINTS)", norm)
		}
	}
	return []string{norm}, nil
}

func runBackfill(ctx context.Context, s *store.Store, client *stashbox.Client, opts backfillOptions, out io.Writer) (backfillStats, error) {
	var st backfillStats
	cands, err := s.StashBoxSweepCandidates(ctx, opts.endpoint, opts.limit)
	if err != nil {
		return st, err
	}
	retries := 0
	for i := 0; i < len(cands); {
		rel := cands[i]
		if i > 0 || retries > 0 {
			if err := sleepCtx(ctx, opts.delay*time.Duration(1+retries)); err != nil {
				return st, err
			}
		}
		outcome, matched, err := backfillOne(ctx, s, client, rel, opts)
		switch {
		case errors.Is(err, stashbox.ErrUnauthorized):
			// A rejected key stays rejected; stop before a limiter notices it.
			return st, err
		case errors.Is(err, stashbox.ErrRateLimited):
			retries++
			if retries > opts.maxRetries {
				return st, fmt.Errorf("%w after %d retries; re-run later, progress is kept", err, opts.maxRetries)
			}
			continue
		case err != nil:
			st.errored++
			_, _ = fmt.Fprintf(out, "release %d: error: %v\n", rel.ID, err)
			outcome = store.LookupError
		}
		retries = 0
		switch outcome {
		case store.LookupFingerprint:
			st.fingerprint++
			_, _ = fmt.Fprintf(out, "matched fingerprint of %s to %q - %s - %s%s\n", releaseLabel(rel), matched.Title, boxLabel(opts.endpoint), matched.ID, dryRunSuffix(opts.dryRun))
		case store.LookupProposed:
			st.proposed++
			_, _ = fmt.Fprintf(out, "matched name of %s to %q - %s - %s, proposed%s\n", releaseLabel(rel), matched.Title, boxLabel(opts.endpoint), matched.ID, dryRunSuffix(opts.dryRun))
		case store.LookupNone:
			st.none++
			_, _ = fmt.Fprintf(out, "no match for %s\n", releaseLabel(rel))
		}
		if !opts.dryRun {
			if err := s.RecordStashBoxLookup(ctx, rel.ID, opts.endpoint, outcome); err != nil {
				return st, err
			}
		}
		i++
	}
	return st, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Returns the matched scene alongside the outcome so the caller can say
// what was matched to what.
func backfillOne(ctx context.Context, s *store.Store, client *stashbox.Client, rel store.Release, opts backfillOptions) (string, *stashbox.Scene, error) {
	scene, err := findByFingerprint(ctx, client, rel)
	if err != nil {
		return "", nil, err
	}
	if scene != nil {
		if opts.dryRun {
			return store.LookupFingerprint, scene, nil
		}
		ids := []store.ReleaseStashID{{Endpoint: opts.endpoint, EHash: hash.EndpointHash(opts.endpoint), StashID: scene.ID}}
		if err := s.AddReleaseStashIDs(ctx, rel.ID, ids, opts.proposer); err != nil {
			return "", nil, err
		}
		if opts.proposer != nil {
			if err := propose(ctx, s, rel.ID, *scene, opts); err != nil {
				return "", nil, err
			}
		}
		return store.LookupFingerprint, scene, nil
	}

	if opts.proposer == nil || rel.Title == nil || rel.ReleaseDate == nil || *rel.Title == "" || *rel.ReleaseDate == "" {
		return store.LookupNone, nil, nil
	}
	scenes, err := client.SearchScene(ctx, *rel.Title)
	if err != nil {
		return "", nil, err
	}
	for _, sc := range scenes {
		if sc.ID == "" || sc.Date != *rel.ReleaseDate {
			continue
		}
		if !opts.dryRun {
			if err := propose(ctx, s, rel.ID, sc, opts); err != nil {
				return "", nil, err
			}
		}
		return store.LookupProposed, &sc, nil
	}
	return store.LookupNone, nil, nil
}

func findByFingerprint(ctx context.Context, client *stashbox.Client, rel store.Release) (*stashbox.Scene, error) {
	queries := [][2]string{{"OSHASH", rel.OSHash.String()}}
	if rel.PHash != nil {
		queries = append(queries, [2]string{"PHASH", rel.PHash.String()})
	}
	for _, q := range queries {
		scenes, err := client.FindSceneByFingerprint(ctx, q[0], q[1], int(rel.DurationMs))
		if err != nil {
			return nil, err
		}
		for _, sc := range scenes {
			if sc.ID != "" {
				return &sc, nil
			}
		}
	}
	return nil, nil
}

// A name-only hit is a proposal with the id as evidence, never an attached
// id: the 0017 rules decide whether it pins.
func propose(ctx context.Context, s *store.Store, releaseID int64, sc stashbox.Scene, opts backfillOptions) error {
	p := store.MetadataProposal{
		ReleaseID: releaseID, ProposedBy: opts.proposer,
		Title: optStr(sc.Title), ReleaseDate: optStr(sc.Date), Studio: optStr(sc.Studio),
		Performers: sc.Performers, StashID: optStr(sc.ID), Endpoint: &opts.endpoint,
	}
	recorded, err := s.RecordProposal(ctx, p)
	if err != nil || !recorded {
		return err
	}
	if err := s.DeriveAfterProposal(ctx, releaseID); err != nil {
		return err
	}
	if opts.autoPin != nil {
		if _, err := s.AutoConfirmIfEligible(ctx, releaseID, opts.autoPin); err != nil {
			return err
		}
	}
	return nil
}

func optStr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func init() {
	f := stashboxBackfillCmd.Flags()
	f.StringVar(&backfillEndpoint, "endpoint", "", "sweep only this stash-box (must be in MOANSUBS_STASH_ENDPOINTS)")
	f.IntVar(&backfillLimit, "limit", 0, "stop after N releases per endpoint (0 = all)")
	f.DurationVar(&backfillDelay, "delay", time.Second, "pause between requests")
	f.BoolVar(&backfillDryRun, "dry-run", false, "query the box but write nothing")
	f.StringVar(&backfillKey, "key", "", "your stash-box api key (or MOANSUBS_STASHBOX_KEY); never stored")
	f.StringVar(&backfillAs, "as", "", "trusted account that name-only hits are proposed as; without it they are skipped")
	stashboxCmd.AddCommand(stashboxBackfillCmd)
	rootCmd.AddCommand(stashboxCmd)
}
