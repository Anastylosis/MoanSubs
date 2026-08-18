package main

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/spf13/cobra"
)

var releaseCmd = &cobra.Command{
	Use:   "release",
	Short: "Operate on stored releases",
}

var releaseWithdrawReason string

// releaseWithdrawCmd withdraws a release AND every one of its tracks
// (store.WithdrawRelease's cascade) — a whole encode's worth of subtitles
// taken down in one step, e.g. for a video that shouldn't have been
// fingerprinted at all.
var releaseWithdrawCmd = &cobra.Command{
	Use:   "withdraw <id>",
	Short: "Withdraw a release and all its tracks",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("moansubs release withdraw: invalid id %q: %w", args[0], err)
		}
		s, ctx, cancel, err := openStore(cmd, "release withdraw")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if err := s.WithdrawRelease(ctx, id, releaseWithdrawReason); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs release withdraw: no release with id %d", id)
			}
			return fmt.Errorf("moansubs release withdraw: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Release %d withdrawn (all its tracks too).\n", id)
		return nil
	},
}

// releaseRestoreCmd undoes releaseWithdrawCmd, restoring the release and
// every track under it (store.RestoreRelease — see its doc comment for the
// tradeoff on tracks withdrawn individually before the release was).
var releaseRestoreCmd = &cobra.Command{
	Use:   "restore <id>",
	Short: "Restore a withdrawn release and its tracks",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return fmt.Errorf("moansubs release restore: invalid id %q: %w", args[0], err)
		}
		s, ctx, cancel, err := openStore(cmd, "release restore")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if err := s.RestoreRelease(ctx, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs release restore: no release with id %d", id)
			}
			return fmt.Errorf("moansubs release restore: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Release %d restored (all its tracks too).\n", id)
		return nil
	},
}

func init() {
	releaseWithdrawCmd.Flags().StringVar(&releaseWithdrawReason, "reason", "", "reason recorded for the withdrawal")
	releaseCmd.AddCommand(releaseWithdrawCmd, releaseRestoreCmd)
	rootCmd.AddCommand(releaseCmd)
}
