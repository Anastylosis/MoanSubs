package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/Anastylosis/MoanSubs/internal/subtitle"
	"github.com/spf13/cobra"
)

var trackCmd = &cobra.Command{
	Use:   "track",
	Short: "Operate on stored subtitle tracks",
}

// parseTrackID is the id-argument boilerplate shared by track
// withdraw/restore/show — a bad id is a usage error, not a store error.
func parseTrackID(what, arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("moansubs %s: invalid id %q: %w", what, arg, err)
	}
	return id, nil
}

var trackWithdrawReason string

// trackWithdrawCmd is the CLI half of a takedown: marks a track withdrawn
// (WP-A1) so every lookup/match/download read path stops surfacing it,
// without deleting the row — reversible via `track restore`.
var trackWithdrawCmd = &cobra.Command{
	Use:   "withdraw <id>",
	Short: "Withdraw (soft-delete) a subtitle track",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := parseTrackID("track withdraw", args[0])
		if err != nil {
			return err
		}
		s, ctx, cancel, err := openStore(cmd, "track withdraw")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if err := s.WithdrawTrack(ctx, id, trackWithdrawReason); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs track withdraw: no track with id %d", id)
			}
			return fmt.Errorf("moansubs track withdraw: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Track %d withdrawn.\n", id)
		return nil
	},
}

// trackRestoreCmd undoes trackWithdrawCmd.
var trackRestoreCmd = &cobra.Command{
	Use:   "restore <id>",
	Short: "Restore a withdrawn subtitle track",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := parseTrackID("track restore", args[0])
		if err != nil {
			return err
		}
		s, ctx, cancel, err := openStore(cmd, "track restore")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if err := s.RestoreTrack(ctx, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs track restore: no track with id %d", id)
			}
			return fmt.Errorf("moansubs track restore: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Track %d restored.\n", id)
		return nil
	},
}

// trackShowCmd prints a track's metadata without its body — release, lang,
// generated, uploader name, created, withdrawn — for an operator deciding
// whether to withdraw/restore something without downloading the subtitle
// text itself.
var trackShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show a subtitle track's metadata (no body)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := parseTrackID("track show", args[0])
		if err != nil {
			return err
		}
		s, ctx, cancel, err := openStore(cmd, "track show")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		d, err := s.GetTrackDetail(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs track show: no track with id %d", id)
			}
			return fmt.Errorf("moansubs track show: %w", err)
		}

		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "id: %d\n", d.ID)
		_, _ = fmt.Fprintf(out, "release: %d\n", d.ReleaseID)
		_, _ = fmt.Fprintf(out, "lang: %s\n", d.Lang)
		_, _ = fmt.Fprintf(out, "generated: %v\n", d.Generated)
		uploader := "(none)"
		if d.UploaderName != nil {
			uploader = *d.UploaderName
		}
		_, _ = fmt.Fprintf(out, "uploader: %s\n", uploader)
		_, _ = fmt.Fprintf(out, "created: %s\n", d.CreatedAt.UTC().Format(time.RFC3339))
		_, _ = fmt.Fprintf(out, "score: +%d/-%d\n", d.Up, d.Down)
		if d.WithdrawnAt == nil {
			_, _ = fmt.Fprintln(out, "withdrawn: no")
		} else {
			reason := ""
			if d.WithdrawnReason != nil {
				reason = " (" + *d.WithdrawnReason + ")"
			}
			_, _ = fmt.Fprintf(out, "withdrawn: %s%s\n", d.WithdrawnAt.UTC().Format(time.RFC3339), reason)
		}

		votes, err := s.VotesForTrack(ctx, id)
		if err != nil {
			return fmt.Errorf("moansubs track show: listing votes: %w", err)
		}
		if len(votes) == 0 {
			_, _ = fmt.Fprintln(out, "votes: none cast")
		} else {
			_, _ = fmt.Fprintln(out, "votes:")
			for _, v := range votes {
				reason := "-"
				if v.Reason != nil {
					reason = *v.Reason
				}
				note := ""
				if v.Note != nil {
					note = *v.Note
				}
				_, _ = fmt.Fprintf(out, "  %s\t%+d\t%s\t%s\n", v.Voter, v.Value, reason, note)
			}
		}
		return nil
	},
}

// flaggedMinDown is `track list --flagged`'s down-vote floor (WP-C3 spec:
// "ListFlaggedTracks(minDown=3)") — below this a handful of downvotes is
// just ordinary disagreement, not something worth an operator's attention
// on its own (a spam vote flags a track regardless of this floor).
const flaggedMinDown = 3

var trackListFlagged bool

// trackListCmd lists tracks needing operator attention (WP-C3 spec).
// --flagged is required for now rather than an unpaged dump of every
// track: nothing in this work package asks for a general listing, and
// inventing one unbounded would be scope creep with its own pagination
// and ordering questions to answer.
var trackListCmd = &cobra.Command{
	Use:   "list",
	Short: "List subtitle tracks",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !trackListFlagged {
			return errors.New("moansubs track list: only --flagged is supported")
		}

		s, ctx, cancel, err := openStore(cmd, "track list")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		tracks, err := s.ListFlaggedTracks(ctx, flaggedMinDown)
		if err != nil {
			return fmt.Errorf("moansubs track list: %w", err)
		}

		out := cmd.OutOrStdout()
		for _, t := range tracks {
			uploader := "(none)"
			if t.UploaderName != nil {
				uploader = *t.UploaderName
			}
			topReason := "(none)"
			if t.TopReason != nil {
				topReason = *t.TopReason
			}
			_, _ = fmt.Fprintf(out, "%d\t%d\t%s\t%s\t+%d/-%d\t%s\n",
				t.ID, t.ReleaseID, t.Lang, uploader, t.Up, t.Down, topReason)
		}
		return nil
	},
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
	trackWithdrawCmd.Flags().StringVar(&trackWithdrawReason, "reason", "", "reason recorded for the withdrawal")
	trackListCmd.Flags().BoolVar(&trackListFlagged, "flagged", false, "list tracks flagged for operator review (the only supported mode)")
	trackCmd.AddCommand(trackResanitizeCmd, trackWithdrawCmd, trackRestoreCmd, trackShowCmd, trackListCmd)
	rootCmd.AddCommand(trackCmd)
}
