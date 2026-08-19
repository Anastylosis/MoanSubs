package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Anastylosis/MoanSubs/internal/hash"
	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/Anastylosis/MoanSubs/internal/subtitle"
	"github.com/spf13/cobra"
)

// importCmd implements `moansubs import FILE` (PLAN.md WP-B2): the reverse
// of `moansubs dump`, into an empty or already-populated node. Releases are
// keyed by oshash (GetOrCreateRelease, same as a normal upload) and tracks
// go through the same idempotent duplicate check uploads use
// (FindIdenticalTrack), so re-running import against the same file — or a
// newer dump from the same upstream — never doubles anything up.
var importCmd = &cobra.Command{
	Use:   "import FILE",
	Short: "Import releases and tracks from a moansubs dump",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, ctx, cancel, err := openStore(cmd, "import")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		f, err := os.Open(args[0])
		if err != nil {
			return fmt.Errorf("moansubs import: %w", err)
		}
		defer func() { _ = f.Close() }()

		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("moansubs import: %s: %w", args[0], err)
		}
		defer func() { _ = gz.Close() }()

		stats, err := runImport(ctx, s, gz, cmd.OutOrStdout())
		if err != nil {
			return fmt.Errorf("moansubs import: %w", err)
		}

		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "releases: %d\n", stats.releases)
		_, _ = fmt.Fprintf(out, "tracks: %d imported, %d already present, %d skipped (unparseable), %d skipped (release withdrawn here)\n",
			stats.tracksImported, stats.tracksDuplicate, stats.tracksSkipped, stats.tracksWithdrawn)
		return nil
	},
}

type importStats struct {
	releases        int
	tracksImported  int
	tracksDuplicate int
	tracksSkipped   int
	tracksWithdrawn int
}

// importLineKind reads just enough of a dump line to dispatch on — every
// line moansubs dump writes carries a "kind" discriminator (dump.go).
type importLineKind struct {
	Kind string `json:"kind"`
}

// runImport reads r (an already-decompressed dump stream) line by line and
// applies it. Releases must appear before any track that references them —
// moansubs dump writes them in that order — since a track's release_id in
// the dump names a release by *its origin node's* id, which this node's
// GetOrCreateRelease will very often assign a different id to; releaseIDs
// maps the dumped id to the id actually assigned here.
func runImport(ctx context.Context, s *store.Store, r io.Reader, out io.Writer) (importStats, error) {
	dec := json.NewDecoder(r)
	var stats importStats
	releaseIDs := make(map[int64]int64)

	line := 0
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return stats, fmt.Errorf("line %d: %w", line+1, err)
		}
		line++

		var k importLineKind
		if err := json.Unmarshal(raw, &k); err != nil {
			return stats, fmt.Errorf("line %d: %w", line, err)
		}

		switch k.Kind {
		case "meta":
			var m dumpMetaLine
			if err := json.Unmarshal(raw, &m); err != nil {
				return stats, fmt.Errorf("line %d: decoding meta: %w", line, err)
			}
			if m.Format != dumpFormat {
				return stats, fmt.Errorf("line %d: dump format %d, this build only understands format %d", line, m.Format, dumpFormat)
			}

		case "release":
			var rl dumpReleaseLine
			if err := json.Unmarshal(raw, &rl); err != nil {
				return stats, fmt.Errorf("line %d: decoding release: %w", line, err)
			}
			local, err := importRelease(ctx, s, out, rl)
			if err != nil {
				return stats, fmt.Errorf("line %d: %w", line, err)
			}
			// A release this node has withdrawn stays withdrawn: its tracks
			// in the dump are counted and dropped, so a takedown here is
			// not undone by re-importing upstream. Marked with 0 (never a
			// real id) rather than left out of the map, which would read
			// as a malformed dump below.
			if local.WithdrawnAt != nil {
				releaseIDs[rl.ID] = 0
			} else {
				releaseIDs[rl.ID] = local.ID
			}
			stats.releases++

		case "track":
			var tl dumpTrackLine
			if err := json.Unmarshal(raw, &tl); err != nil {
				return stats, fmt.Errorf("line %d: decoding track: %w", line, err)
			}
			localReleaseID, ok := releaseIDs[tl.ReleaseID]
			if !ok {
				return stats, fmt.Errorf("line %d: track %d references release %d, not seen earlier in this dump", line, tl.ID, tl.ReleaseID)
			}
			if localReleaseID == 0 {
				stats.tracksWithdrawn++
				continue
			}
			result, err := importTrack(ctx, s, out, localReleaseID, tl)
			if err != nil {
				return stats, fmt.Errorf("line %d: %w", line, err)
			}
			switch result {
			case trackImported:
				stats.tracksImported++
			case trackDuplicate:
				stats.tracksDuplicate++
			case trackSkippedUnparseable:
				stats.tracksSkipped++
			}

		default:
			return stats, fmt.Errorf("line %d: unknown kind %q", line, k.Kind)
		}
	}
	return stats, nil
}

// importRelease finds-or-creates the local release for rl, the same
// GetOrCreateRelease path a normal upload uses — including its
// backfill-only handling of name metadata, so a dump never overwrites what
// this node already knows about a release — then attaches rl's stash ids
// (additive, same as AddReleaseStashIDs is everywhere else, WP-C9a).
func importRelease(ctx context.Context, s *store.Store, out io.Writer, rl dumpReleaseLine) (*store.Release, error) {
	oh, err := hash.ParseOSHash(rl.OSHash)
	if err != nil {
		return nil, fmt.Errorf("release %d: %w", rl.ID, err)
	}
	var phash *hash.PHash
	if rl.PHash != nil {
		p, err := hash.ParsePHash(*rl.PHash)
		if err != nil {
			return nil, fmt.Errorf("release %d: %w", rl.ID, err)
		}
		phash = &p
	}

	release, err := s.GetOrCreateRelease(ctx, store.Release{
		OSHash:      oh,
		PHash:       phash,
		DurationMs:  rl.DurationMs,
		Width:       rl.Width,
		Height:      rl.Height,
		VideoCodec:  rl.VideoCodec,
		Title:       rl.Title,
		Stem:        rl.Stem,
		ReleaseDate: rl.ReleaseDate,
		Studio:      rl.Studio,
		Performers:  rl.Performers,
	})
	if err != nil {
		return nil, fmt.Errorf("release %d: %w", rl.ID, err)
	}

	// A withdrawn release stays withdrawn on this node (the caller drops its
	// tracks below); attaching stash ids to it is harmless but pointless, so
	// skip the extra work.
	if release.WithdrawnAt != nil || len(rl.StashIDs) == 0 {
		return release, nil
	}

	stashIDs := make([]store.ReleaseStashID, 0, len(rl.StashIDs))
	for _, sid := range rl.StashIDs {
		endpoint, err := hash.NormalizeStashEndpoint(sid.Endpoint)
		if err != nil {
			_, _ = fmt.Fprintf(out, "release %d: skipping stash id with unparseable endpoint %q: %v\n", rl.ID, sid.Endpoint, err)
			continue
		}
		id, err := hash.ParseStashID(sid.StashID)
		if err != nil {
			_, _ = fmt.Fprintf(out, "release %d: skipping malformed stash_id %q: %v\n", rl.ID, sid.StashID, err)
			continue
		}
		stashIDs = append(stashIDs, store.ReleaseStashID{Endpoint: endpoint, EHash: hash.EndpointHash(endpoint), StashID: id})
	}
	if err := s.AddReleaseStashIDs(ctx, release.ID, stashIDs, nil); err != nil {
		return nil, fmt.Errorf("release %d: attaching stash ids: %w", rl.ID, err)
	}
	return release, nil
}

type trackImportResult int

const (
	trackImported trackImportResult = iota
	trackDuplicate
	trackSkippedUnparseable
)

// importTrack re-renders tl's body through the same parse+render pair
// handleUploadSubtitle uses on ingest (internal/api/subtitles.go) rather
// than trusting the dump's bytes directly — the file may have come from a
// different or older node than this binary. A track that fails to parse is
// printed and skipped, the same "bug in stored data, not fatal" handling
// `track resanitize` uses, since a dump's bodies are expected to already be
// sanitized SRT.
//
// The track is never attributed to a local account (there is no account to
// attach it to on this node): source records "mirror:<uploader>" when the
// dump named an uploader, or plain "mirror" when it didn't, so provenance
// survives the mirror even without uploader_id.
func importTrack(ctx context.Context, s *store.Store, out io.Writer, releaseID int64, tl dumpTrackLine) (trackImportResult, error) {
	cues, err := subtitle.Parse([]byte(tl.Body))
	if err != nil {
		_, _ = fmt.Fprintf(out, "track %d: unparseable, skipping: %v\n", tl.ID, err)
		return trackSkippedUnparseable, nil
	}
	rendered := subtitle.RenderSRT(cues)

	existingID, err := s.FindIdenticalTrack(ctx, releaseID, tl.Lang, rendered)
	if err != nil {
		return 0, fmt.Errorf("track %d: %w", tl.ID, err)
	}
	if existingID != 0 {
		return trackDuplicate, nil
	}

	source := "mirror"
	if tl.Uploader != nil && *tl.Uploader != "" {
		source = "mirror:" + *tl.Uploader
	}

	if _, err := s.CreateSubtitleTrack(ctx, store.SubtitleTrack{
		ReleaseID:  releaseID,
		Lang:       tl.Lang,
		Body:       rendered,
		Generated:  tl.Generated,
		Provenance: []byte(tl.Provenance),
		License:    tl.License,
		Source:     &source,
	}); err != nil {
		return 0, fmt.Errorf("track %d: %w", tl.ID, err)
	}
	return trackImported, nil
}

func init() {
	rootCmd.AddCommand(importCmd)
}
