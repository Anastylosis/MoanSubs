package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/spf13/cobra"
)

// dumpFormat is the JSONL schema version stamped on every dump's meta line.
// Bump it (and teach `moansubs import` to reject or translate older ones)
// the day the shape of a release/track line actually changes.
const dumpFormat = 1

// dumpBatchSize mirrors resanitizeBatchSize (cmd/moansubs/track.go): page
// releases and tracks in batches of 500 by id, so a full-node export never
// holds one long-running query or materializes the whole table in Go.
const dumpBatchSize = 500

// dumpMetaLine is always the first line of a dump.
type dumpMetaLine struct {
	Kind        string    `json:"kind"`
	Format      int       `json:"format"`
	GeneratedAt time.Time `json:"generated_at"`
	Node        string    `json:"node"`
}

// dumpReleaseLine is one non-withdrawn release, in the same shape the
// lookup API returns a release (internal/api/lookup.go's lookupRelease)
// minus its nested Tracks — tracks are their own dump lines instead — plus
// the name metadata migration 0003 keeps server-side for /match: a mirror
// without it would have a dead name fallback and an empty catalogue.
type dumpReleaseLine struct {
	Kind        string   `json:"kind"`
	ID          int64    `json:"id"`
	OSHash      string   `json:"oshash"`
	PHash       *string  `json:"phash"`
	DurationMs  int64    `json:"duration_ms"`
	Width       *int     `json:"width"`
	Height      *int     `json:"height"`
	VideoCodec  *string  `json:"video_codec"`
	Title       *string  `json:"title,omitempty"`
	Stem        *string  `json:"stem,omitempty"`
	ReleaseDate *string  `json:"date,omitempty"`
	Studio      *string  `json:"studio,omitempty"`
	Performers  []string `json:"performers,omitempty"`
}

// dumpTrackLine is one non-withdrawn track. Uploader is the account's
// display name, never its id or token — the only thing dump output carries
// from accounts (PLAN.md WP-B2: "Nothing from accounts, sessions,
// track_votes beyond the aggregate"). Downloads is the origin's count,
// informational for a mirror (import starts its own at zero). up/down are
// omitted until votes exist (WP-C3).
type dumpTrackLine struct {
	Kind       string          `json:"kind"`
	ID         int64           `json:"id"`
	ReleaseID  int64           `json:"release_id"`
	Lang       string          `json:"lang"`
	Generated  bool            `json:"generated"`
	Provenance json.RawMessage `json:"provenance,omitempty"`
	License    string          `json:"license"`
	Source     *string         `json:"source,omitempty"`
	Uploader   *string         `json:"uploader"`
	CreatedAt  time.Time       `json:"created_at"`
	Downloads  int64           `json:"downloads"`
	Body       string          `json:"body"`
}

var dumpOutputPath string

// dumpCmd implements `moansubs dump` (PLAN.md WP-B2): every non-withdrawn
// release and track, as gzip JSONL, one meta line followed by one line per
// row. Writes to stdout by default so `moansubs dump | rclone rcat ...`
// works untouched by any other output this command might otherwise print —
// nothing but the gzip stream ever reaches stdout here, in either mode.
var dumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Write every non-withdrawn release and track as gzip JSONL",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		s, ctx, cancel, err := openStore(cmd, "dump")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		w := cmd.OutOrStdout()
		if dumpOutputPath != "" {
			f, err := os.Create(dumpOutputPath)
			if err != nil {
				return fmt.Errorf("moansubs dump: creating %s: %w", dumpOutputPath, err)
			}
			defer func() { _ = f.Close() }()
			w = f
		}

		gz := gzip.NewWriter(w)
		bw := bufio.NewWriter(gz)
		enc := json.NewEncoder(bw)

		stats, err := writeDump(ctx, s, enc)
		if err != nil {
			return fmt.Errorf("moansubs dump: %w", err)
		}
		if err := bw.Flush(); err != nil {
			return fmt.Errorf("moansubs dump: flushing: %w", err)
		}
		// The gzip trailer is only written on Close, so a stream that isn't
		// explicitly closed here is truncated and unreadable by gunzip even
		// though every byte up to that point looks fine.
		if err := gz.Close(); err != nil {
			return fmt.Errorf("moansubs dump: closing gzip stream: %w", err)
		}

		// Only printed for -o: with no output path the gzip stream itself
		// is on stdout, and a summary line would corrupt it for a
		// `dump | rclone rcat` pipe.
		if dumpOutputPath != "" {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "wrote %d release(s), %d track(s) to %s\n",
				stats.releases, stats.tracks, dumpOutputPath)
		}
		return nil
	},
}

type dumpStats struct {
	releases int
	tracks   int
}

// writeDump streams the meta line, then every non-withdrawn release, then
// every non-withdrawn track, through enc. Releases are written in full
// before any track: import.go relies on that ordering to resolve a track's
// release_id against a release it has already seen.
func writeDump(ctx context.Context, s *store.Store, enc *json.Encoder) (dumpStats, error) {
	var stats dumpStats

	if err := enc.Encode(dumpMetaLine{
		Kind:        "meta",
		Format:      dumpFormat,
		GeneratedAt: time.Now().UTC(),
		Node:        version,
	}); err != nil {
		return stats, fmt.Errorf("writing meta line: %w", err)
	}

	var afterID int64
	for {
		batch, err := s.DumpReleasesAfter(ctx, afterID, dumpBatchSize)
		if err != nil {
			return stats, err
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			if err := enc.Encode(dumpReleaseFrom(r)); err != nil {
				return stats, fmt.Errorf("writing release %d: %w", r.ID, err)
			}
			stats.releases++
		}
		afterID = batch[len(batch)-1].ID
	}

	afterID = 0
	for {
		batch, err := s.DumpTracksAfter(ctx, afterID, dumpBatchSize)
		if err != nil {
			return stats, err
		}
		if len(batch) == 0 {
			break
		}
		for _, t := range batch {
			if err := enc.Encode(dumpTrackFrom(t)); err != nil {
				return stats, fmt.Errorf("writing track %d: %w", t.ID, err)
			}
			stats.tracks++
		}
		afterID = batch[len(batch)-1].ID
	}

	return stats, nil
}

func dumpReleaseFrom(r store.Release) dumpReleaseLine {
	var phash *string
	if r.PHash != nil {
		v := r.PHash.String()
		phash = &v
	}
	return dumpReleaseLine{
		Kind:        "release",
		ID:          r.ID,
		OSHash:      string(r.OSHash),
		PHash:       phash,
		DurationMs:  r.DurationMs,
		Width:       r.Width,
		Height:      r.Height,
		VideoCodec:  r.VideoCodec,
		Title:       r.Title,
		Stem:        r.Stem,
		ReleaseDate: r.ReleaseDate,
		Studio:      r.Studio,
		Performers:  r.Performers,
	}
}

func dumpTrackFrom(t store.DumpTrack) dumpTrackLine {
	return dumpTrackLine{
		Kind:       "track",
		ID:         t.ID,
		ReleaseID:  t.ReleaseID,
		Lang:       t.Lang,
		Generated:  t.Generated,
		Provenance: json.RawMessage(t.Provenance),
		License:    t.License,
		Source:     t.Source,
		Uploader:   t.UploaderName,
		CreatedAt:  t.CreatedAt,
		Downloads:  t.Downloads,
		Body:       t.Body,
	}
}

func init() {
	dumpCmd.Flags().StringVarP(&dumpOutputPath, "output", "o", "", "write to this file instead of stdout")
	rootCmd.AddCommand(dumpCmd)
}
