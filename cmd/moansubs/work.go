package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/Anastylosis/MoanSubs/internal/subtitle"
	"github.com/Anastylosis/MoanSubs/internal/work"
	"github.com/spf13/cobra"
)

var workCmd = &cobra.Command{
	Use:   "work",
	Short: "Group releases that are the same video in different encodes",
	Long: `A work groups releases that are the same video cut or encoded
differently — the case phash cannot see, because Stash samples frames at
fixed fractions of a video's duration, so trimming an intro moves every
sample and pushes two copies of one film far apart.

Grouping is advisory: it never gates a lookup, never merges rows, and
unlinking restores the previous state exactly.`,
}

var (
	workSuggestLimit  int
	workSuggestApply  bool
	workSuggestSignal string
)

// workSuggestCmd is the backfill path for a node that already has a
// catalogue: inference runs here, on demand, rather than on every upload.
var workSuggestCmd = &cobra.Command{
	Use:   "suggest",
	Short: "Propose groupings from stash ids, subtitle overlap and names",
	RunE: func(cmd *cobra.Command, _ []string) error {
		s, ctx, cancel, err := openStore(cmd, "work suggest")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		out := cmd.OutOrStdout()
		var candidates []work.Candidate

		// (a) Shared stash-box id: an external catalogue asserting these
		// are one scene. The only signal safe to apply unattended.
		if workSuggestSignal == "" || workSuggestSignal == work.SignalStashID {
			pairs, err := s.StashIDCandidates(ctx)
			if err != nil {
				return fmt.Errorf("moansubs work suggest: %w", err)
			}
			for _, p := range pairs {
				candidates = append(candidates, work.Candidate{
					ReleaseA: p.A, ReleaseB: p.B,
					Signal: work.SignalStashID, Confidence: 1,
					Reason: "both carry stash id " + p.SharedStashIDVal,
				})
			}
		}

		// (b) and (c) share a pre-filter: only releases whose runtimes are
		// close enough to be the same film are worth comparing at all.
		if workSuggestSignal == "" || workSuggestSignal != work.SignalStashID {
			pairs, err := s.NearDurationCandidates(ctx, work.MaxNameDurationDeltaMs, workSuggestLimit*20)
			if err != nil {
				return fmt.Errorf("moansubs work suggest: %w", err)
			}
			cues := map[int64]map[string][]string{}
			cuesFor := func(id int64) map[string][]string {
				if c, ok := cues[id]; ok {
					return c
				}
				bodies, err := s.TrackBodiesByRelease(ctx, id)
				c := map[string][]string{}
				if err == nil {
					for lang, bs := range bodies {
						for _, b := range bs {
							parsed, perr := subtitle.Parse([]byte(b))
							if perr != nil {
								continue
							}
							for _, q := range parsed {
								c[lang] = append(c[lang], q.Text)
							}
						}
					}
				}
				cues[id] = c
				return c
			}

			for _, p := range pairs {
				matched := false
				if workSuggestSignal == "" || workSuggestSignal == work.SignalSubtitleOverlap {
					ca, cb := cuesFor(p.A), cuesFor(p.B)
					for lang, la := range ca {
						lb, ok := cb[lang]
						if !ok {
							continue
						}
						if c, ok := work.SubtitleOverlapCandidate(p.A, p.B, la, lb); ok {
							c.Reason += " [" + lang + "]"
							candidates = append(candidates, c)
							matched = true
							break
						}
					}
				}
				// Name+duration only speaks when overlap did not: it is the
				// weaker claim and would only duplicate the stronger one.
				if !matched && (workSuggestSignal == "" || workSuggestSignal == work.SignalNameDuration) {
					if c, ok := work.NameDurationCandidate(p.A, p.B, p.NameA, p.NameB, p.DurationA, p.DurationB); ok {
						candidates = append(candidates, c)
					}
				}
			}
		}

		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].Signal != candidates[j].Signal {
				return signalRank(candidates[i].Signal) < signalRank(candidates[j].Signal)
			}
			return candidates[i].Confidence > candidates[j].Confidence
		})
		if len(candidates) > workSuggestLimit {
			candidates = candidates[:workSuggestLimit]
		}
		if len(candidates) == 0 {
			_, _ = fmt.Fprintln(out, "No groupings proposed.")
			return nil
		}

		for _, c := range candidates {
			_, _ = fmt.Fprintf(out, "%d + %d  %-16s %.2f  %s\n",
				c.ReleaseA, c.ReleaseB, c.Signal, c.Confidence, c.Reason)
		}
		if !workSuggestApply {
			_, _ = fmt.Fprintf(out, "\n%d proposed. Re-run with --apply to link the stash-id ones.\n", len(candidates))
			return nil
		}

		// --apply deliberately links only the stash-id signal. The other
		// two are inferences, and a wrong grouping puts the wrong
		// subtitles in front of someone; those stay a human's call.
		linked := 0
		for _, c := range candidates {
			if c.Signal != work.SignalStashID {
				continue
			}
			if _, err := s.LinkReleases(ctx, c.ReleaseA, c.ReleaseB); err != nil {
				_, _ = fmt.Fprintf(out, "  linking %d+%d failed: %v\n", c.ReleaseA, c.ReleaseB, err)
				continue
			}
			linked++
		}
		_, _ = fmt.Fprintf(out, "\nLinked %d stash-id pair(s); %d inferred pair(s) left for review.\n",
			linked, len(candidates)-linked)
		return nil
	},
}

func signalRank(s string) int {
	switch s {
	case work.SignalStashID:
		return 0
	case work.SignalSubtitleOverlap:
		return 1
	default:
		return 2
	}
}

var workLinkCmd = &cobra.Command{
	Use:   "link <release-id> <release-id>",
	Short: "Put two releases in the same work",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		a, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("moansubs work link: invalid id %q: %w", args[0], err)
		}
		b, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("moansubs work link: invalid id %q: %w", args[1], err)
		}
		s, ctx, cancel, err := openStore(cmd, "work link")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		id, err := s.LinkReleases(ctx, a, b)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs work link: no such release")
			}
			return fmt.Errorf("moansubs work link: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Releases %d and %d are work %d.\n", a, b, id)
		return nil
	},
}

var workUnlinkCmd = &cobra.Command{
	Use:   "unlink <release-id>",
	Short: "Remove a release from its work",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("moansubs work unlink: invalid id %q: %w", args[0], err)
		}
		s, ctx, cancel, err := openStore(cmd, "work unlink")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if err := s.UnlinkRelease(ctx, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs work unlink: no release with id %d", id)
			}
			return fmt.Errorf("moansubs work unlink: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Release %d is no longer grouped.\n", id)
		return nil
	},
}

var workShowCmd = &cobra.Command{
	Use:   "show <release-id>",
	Short: "Show a release's work and its sibling tracks",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("moansubs work show: invalid id %q: %w", args[0], err)
		}
		s, ctx, cancel, err := openStore(cmd, "work show")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		out := cmd.OutOrStdout()
		w, err := s.WorkOf(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			_, _ = fmt.Fprintf(out, "Release %d is not grouped.\n", id)
			return nil
		}
		if err != nil {
			return fmt.Errorf("moansubs work show: %w", err)
		}
		ids, err := s.WorkReleaseIDs(ctx, w.ID)
		if err != nil {
			return fmt.Errorf("moansubs work show: %w", err)
		}
		_, _ = fmt.Fprintf(out, "Work %d: releases %v\n", w.ID, ids)

		sib, err := s.SiblingTracks(ctx, id)
		if err != nil {
			return fmt.Errorf("moansubs work show: %w", err)
		}
		if len(sib) == 0 {
			_, _ = fmt.Fprintln(out, "No sibling tracks.")
			return nil
		}
		for _, t := range sib {
			sync := "sync unknown"
			if t.OffsetMs != nil {
				src := ""
				if t.Source != nil {
					src = " (" + *t.Source + ")"
				}
				sync = fmt.Sprintf("%+.2fs%s", float64(*t.OffsetMs)/1000, src)
			}
			_, _ = fmt.Fprintf(out, "  track %d  %-6s from release %d  %s\n",
				t.TrackID, t.Lang, t.ReleaseID, sync)
		}
		return nil
	},
}

var workOffsetSource string

var workOffsetCmd = &cobra.Command{
	Use:   "offset <track-id> <release-id> <milliseconds>",
	Short: "Record how far a track must shift to fit a release",
	Long: `A positive offset delays the subtitle, which is the case when the
target encode carries extra footage at the head.

Nothing derives this automatically: correlating two subtitle files does not
work, because both are usually timed for the same cut and agree with each
other while disagreeing with the video.`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		trackID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("moansubs work offset: invalid track id %q: %w", args[0], err)
		}
		releaseID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("moansubs work offset: invalid release id %q: %w", args[1], err)
		}
		ms, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return fmt.Errorf("moansubs work offset: invalid milliseconds %q: %w", args[2], err)
		}
		s, ctx, cancel, err := openStore(cmd, "work offset")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if err := s.SetOffset(ctx, trackID, releaseID, ms, workOffsetSource); err != nil {
			return fmt.Errorf("moansubs work offset: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"Track %d plays against release %d with %+.2fs (%s).\n",
			trackID, releaseID, float64(ms)/1000, workOffsetSource)
		return nil
	},
}

func init() {
	workSuggestCmd.Flags().IntVar(&workSuggestLimit, "limit", 50, "maximum candidates to print")
	workSuggestCmd.Flags().BoolVar(&workSuggestApply, "apply", false, "link the stash-id candidates (inferred ones always need review)")
	workSuggestCmd.Flags().StringVar(&workSuggestSignal, "signal", "", "restrict to one signal: stash-id, subtitle-overlap, name-duration")
	workOffsetCmd.Flags().StringVar(&workOffsetSource, "source", store.OffsetManual, "manual, duration-delta or measured")

	workCmd.AddCommand(workSuggestCmd, workLinkCmd, workUnlinkCmd, workShowCmd, workOffsetCmd)
	rootCmd.AddCommand(workCmd)
}
