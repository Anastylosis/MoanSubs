package main

import (
	"fmt"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/Anastylosis/MoanSubs/internal/subtitle"
	"github.com/spf13/cobra"
)

var trackCmd = &cobra.Command{
	Use:   "track",
	Short: "Operate on stored subtitle tracks",
}

// resanitizeBatchSize is how many tracks `track resanitize` fetches and
// updates per round trip, so a full-table backfill never holds one long
// transaction or loads the whole table into memory at once.
const resanitizeBatchSize = 500

var (
	resanitizeDryRun bool
	resanitizeID     int64
)

// trackResanitizeCmd runs every stored subtitle body through the current
// internal/subtitle parse+render pair — the same entry points
// handleUploadSubtitle uses on ingest (internal/api/subtitles.go) — so a
// backfill can never disagree with what a fresh upload would produce.
// Bodies are already sanitized SRT, so a parse failure here is a bug in the
// stored data, not bad input: printed and skipped, never withdrawn (there is
// no withdrawal mechanism to invoke yet, and even once WP-A1 lands, a parse
// regression is not the uploader's fault).
var trackResanitizeCmd = &cobra.Command{
	Use:   "resanitize",
	Short: "Re-render stored subtitle bodies through the current sanitizer",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		s, ctx, cancel, err := openStore(cmd, "track resanitize")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		out := cmd.OutOrStdout()
		var scanned, updated, skipped int

		resanitize := func(t store.SubtitleTrackBody) error {
			scanned++
			cues, err := subtitle.Parse([]byte(t.Body))
			if err != nil {
				_, _ = fmt.Fprintf(out, "id: %d parse error, skipping: %v\n", t.ID, err)
				skipped++
				return nil
			}
			rendered := subtitle.RenderSRT(cues)
			if rendered == t.Body {
				return nil
			}

			_, _ = fmt.Fprintf(out, "id: %d %d bytes → %d bytes\n", t.ID, len(t.Body), len(rendered))
			updated++
			if resanitizeDryRun {
				return nil
			}
			if err := s.UpdateSubtitleTrackBody(ctx, t.ID, rendered); err != nil {
				return fmt.Errorf("moansubs track resanitize: updating id %d: %w", t.ID, err)
			}
			return nil
		}

		if resanitizeID != 0 {
			track, err := s.GetSubtitleTrack(ctx, resanitizeID)
			if err != nil {
				return fmt.Errorf("moansubs track resanitize: %w", err)
			}
			if err := resanitize(store.SubtitleTrackBody{ID: track.ID, Body: track.Body}); err != nil {
				return err
			}
		} else {
			// Walk every track (withdrawn_at does not exist yet — WP-A1 lands
			// separately; the filter belongs here once it does) in batches of
			// 500 by id, so no single transaction spans the whole table.
			var afterID int64
			for {
				batch, err := s.SubtitleTracksAfter(ctx, afterID, resanitizeBatchSize)
				if err != nil {
					return fmt.Errorf("moansubs track resanitize: %w", err)
				}
				if len(batch) == 0 {
					break
				}
				for _, t := range batch {
					if err := resanitize(t); err != nil {
						return err
					}
				}
				afterID = batch[len(batch)-1].ID
			}
		}

		verb := "updated"
		if resanitizeDryRun {
			verb = "would update"
		}
		_, _ = fmt.Fprintf(out, "scanned %d, %s %d, skipped %d (parse errors)\n", scanned, verb, updated, skipped)
		return nil
	},
}

func init() {
	trackResanitizeCmd.Flags().BoolVar(&resanitizeDryRun, "dry-run", false, "print what would change without writing")
	trackResanitizeCmd.Flags().Int64Var(&resanitizeID, "id", 0, "resanitize only this track id")
	trackCmd.AddCommand(trackResanitizeCmd)
	rootCmd.AddCommand(trackCmd)
}
