package api

import (
	"context"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
)

// statsCacheTTL is how long GET /api/v1/stats's response is cached
// in-process before recomputing (WP-A2 spec: "cached in-process for 5
// min") — PublicCounts scans subtitle_tracks/releases in full, too heavy to
// run on every anonymous, otherwise-unthrottled hit.
const statsCacheTTL = 5 * time.Minute

// StatsFlushInterval is how often Stats.Run persists the in-memory lookup
// counters to the stats table (WP-A2 spec: "flushed to stats every 30s and
// on shutdown").
const StatsFlushInterval = 30 * time.Second

// lookupLevels is the fixed set of lookup "shapes" WP-A2 tracks hit rate
// for, in the order GET /api/v1/stats reports them. The names double as
// the suffix on both the atomic field lookup below and the persisted
// "lookups.<level>"/"hits.<level>" stats keys.
var lookupLevels = []string{"oshash", "phash", "batch", "exact", "match", "stash"}

// Stats holds moansubs's in-memory, per-process lookup hit-rate counters
// (WP-A2), plus the store handle Run/Flush need to persist them and the
// small cache GET /api/v1/stats reads through. atomic.Int64 fields rather
// than a mutex-guarded map: the counter set is fixed and known at compile
// time (5 lookup kinds x {total,hit}), so a plain struct of atomics avoids
// both lock contention on the hot request path and the bookkeeping a
// map[string]*atomic.Int64 would need to know every key in advance.
//
// Losing up to one flush interval's worth of increments on a crash is an
// accepted trade-off (WP-A2 spec): this is telemetry, not the ledger — the
// per-track downloads column (store.IncrementDownloads) is what's durable
// per-request.
type Stats struct {
	store *store.Store

	LookupsOshash atomic.Int64
	HitsOshash    atomic.Int64
	LookupsPhash  atomic.Int64
	HitsPhash     atomic.Int64
	LookupsBatch  atomic.Int64
	HitsBatch     atomic.Int64
	LookupsExact  atomic.Int64
	HitsExact     atomic.Int64
	LookupsMatch  atomic.Int64
	HitsMatch     atomic.Int64
	// LookupsStash/HitsStash count GET /api/v1/lookup/stash/{ehash}/{stash_id}
	// (migration 0011, WP-C9a) — the batch endpoint's stash_ids entries fold
	// into LookupsBatch/HitsBatch instead, same as its oshash/phash entries
	// do.
	LookupsStash atomic.Int64
	HitsStash    atomic.Int64

	cacheMu     sync.Mutex
	cached      statsResponse
	cachedUntil time.Time
}

// NewStats returns a Stats bound to s, ready for Add* calls and Run.
func NewStats(s *store.Store) *Stats {
	return &Stats{store: s}
}

// counters maps each persisted stats.key to the atomic field that
// accumulates it in-process — the single place the in-memory/stored key
// mapping is defined, used by both Flush (write) and the level lookup in
// snapshot (read, via store.Counters instead, since a flush may have
// already happened on another process — see Counters' doc comment).
func (st *Stats) counters() map[string]*atomic.Int64 {
	return map[string]*atomic.Int64{
		"lookups.oshash": &st.LookupsOshash,
		"hits.oshash":    &st.HitsOshash,
		"lookups.phash":  &st.LookupsPhash,
		"hits.phash":     &st.HitsPhash,
		"lookups.batch":  &st.LookupsBatch,
		"hits.batch":     &st.HitsBatch,
		"lookups.exact":  &st.LookupsExact,
		"hits.exact":     &st.HitsExact,
		"lookups.match":  &st.LookupsMatch,
		"hits.match":     &st.HitsMatch,
		"lookups.stash":  &st.LookupsStash,
		"hits.stash":     &st.HitsStash,
	}
}

// record bumps lookups by one, and hits with it when hit is true — the
// per-request accounting call every lookup-family handler makes exactly
// once, right before writing its response, on its own pair of counters
// (e.g. &st.LookupsOshash, &st.HitsOshash).
func (st *Stats) record(lookups, hits *atomic.Int64, hit bool) {
	lookups.Add(1)
	if hit {
		hits.Add(1)
	}
}

// Flush drains every counter — Swap(0), not Load — and merges the deltas
// into the stats table. Swap so a request landing mid-flush still counts
// exactly once, into whichever side of the swap it happens to hit, instead
// of being read here and then double-counted (or dropped) by the next
// flush. Exported (rather than only reachable via Run) so tests can flush
// synchronously without waiting on a ticker.
func (st *Stats) Flush(ctx context.Context) error {
	deltas := make(map[string]int64, len(lookupLevels)*2)
	for key, counter := range st.counters() {
		if v := counter.Swap(0); v != 0 {
			deltas[key] = v
		}
	}
	if len(deltas) == 0 {
		return nil
	}
	if err := st.store.MergeCounters(ctx, deltas); err != nil {
		// Put the interval back so a transient DB error costs a retry, not
		// the counts — the next flush merges them along with what's new.
		counters := st.counters()
		for key, v := range deltas {
			counters[key].Add(v)
		}
		return err
	}
	return nil
}

// Run flushes st every interval, and once more when ctx is cancelled so a
// graceful shutdown doesn't drop the last partial period (WP-A2:
// cmd/moansubs/serve.go starts this next to the existing graceful
// shutdown). Blocks until the final flush completes; callers that need the
// process to wait for it should run Run in a goroutine and join it (e.g.
// via a WaitGroup) before closing the store. Flush errors are logged, not
// returned — a telemetry flush failing must never take the server down.
func (st *Stats) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := st.Flush(ctx); err != nil {
				log.Printf("api: Stats.Flush: %v", err)
			}
		case <-ctx.Done():
			// A fresh, un-cancelled context: ctx is already Done, and the
			// final flush on shutdown must not be aborted by the same
			// cancellation that triggered it.
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := st.Flush(flushCtx); err != nil {
				log.Printf("api: Stats.Flush (shutdown): %v", err)
			}
			cancel()
			return
		}
	}
}

// -- GET /api/v1/stats -------------------------------------------------

type lookupLevelStats struct {
	Total int64 `json:"total"`
	Hits  int64 `json:"hits"`
}

// statsResponse is GET /api/v1/stats's JSON body (WP-A2 spec).
type statsResponse struct {
	Tracks         int64                       `json:"tracks"`
	Releases       int64                       `json:"releases"`
	Languages      map[string]int64            `json:"languages"`
	GeneratedShare float64                     `json:"generated_share"`
	DownloadsTotal int64                       `json:"downloads_total"`
	Lookups        map[string]lookupLevelStats `json:"lookups"`
}

// snapshot returns the cached response if it's still fresh, else recomputes
// it from the store and caches the result for statsCacheTTL.
func (st *Stats) snapshot(ctx context.Context) (statsResponse, error) {
	st.cacheMu.Lock()
	defer st.cacheMu.Unlock()

	if time.Now().Before(st.cachedUntil) {
		return st.cached, nil
	}

	counts, err := st.store.PublicCounts(ctx)
	if err != nil {
		return statsResponse{}, err
	}
	raw, err := st.store.Counters(ctx)
	if err != nil {
		return statsResponse{}, err
	}

	lookups := make(map[string]lookupLevelStats, len(lookupLevels))
	for _, level := range lookupLevels {
		lookups[level] = lookupLevelStats{
			Total: raw["lookups."+level],
			Hits:  raw["hits."+level],
		}
	}

	body := statsResponse{
		Tracks:         counts.Tracks,
		Releases:       counts.Releases,
		Languages:      counts.Languages,
		GeneratedShare: counts.GeneratedShare,
		DownloadsTotal: counts.DownloadsTotal,
		Lookups:        lookups,
	}
	st.cached = body
	st.cachedUntil = time.Now().Add(statsCacheTTL)
	return body, nil
}

// handleStats implements GET /api/v1/stats: public, unauthenticated, and
// not IP rate-limited like the lookup endpoints — the 5-minute in-process
// cache above already bounds how often the underlying aggregate queries
// actually run, the same reasoning GET /api/v1/version uses for going
// unthrottled. Withdrawn tracks/releases are excluded from every total
// (store.PublicCounts); the lookups.* numbers reflect the last flush, up to
// StatsFlushInterval behind the live in-memory counters.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	body, err := s.Stats.snapshot(r.Context())
	if err != nil {
		log.Printf("api: Stats.snapshot: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, body)
}
