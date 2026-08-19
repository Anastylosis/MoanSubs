package main

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/spf13/cobra"
)

var inviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Manage registration invite codes",
}

var (
	inviteCreateFor       string
	inviteCreateUses      int
	inviteCreateUnlimited bool
	inviteCreateExpires   time.Duration
)

// inviteCreateCmd mints an arbitrary invite code, attributed to --for.
// "The operator's own unlimited code is just `invite create --unlimited`"
// (WP-C7a spec) — there's no separate "operator invite" concept, an
// operator account uses the same command as anyone else's admin-minted
// code.
var inviteCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Mint an invite code",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if inviteCreateFor == "" {
			return errors.New("moansubs invite create: --for is required")
		}
		// --uses' "given" state is its own positive value, not
		// cmd.Flags().Changed: Changed sticks on a *pflag.FlagSet forever
		// once true, which would leak across a test binary's repeated
		// Execute() calls the same way a stale package-level flag var
		// would (see track_test.go's comment on resanitizeDryRun/
		// resanitizeID for the same gotcha) — testing this command means
		// always passing both --uses and --unlimited explicitly, which a
		// Changed-based check can't distinguish from a genuine omission.
		usesGiven := inviteCreateUses > 0
		if !usesGiven && !inviteCreateUnlimited {
			return errors.New("moansubs invite create: one of --uses or --unlimited is required")
		}
		if usesGiven && inviteCreateUnlimited {
			return errors.New("moansubs invite create: --uses and --unlimited are mutually exclusive")
		}

		s, ctx, cancel, err := openStore(cmd, "invite create")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		account, err := s.GetAccountByName(ctx, inviteCreateFor)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs invite create: no account named %q", inviteCreateFor)
			}
			return fmt.Errorf("moansubs invite create: %w", err)
		}

		var maxUses *int
		if usesGiven {
			maxUses = &inviteCreateUses
		}
		var expiresAt *time.Time
		if inviteCreateExpires > 0 {
			t := time.Now().Add(inviteCreateExpires)
			expiresAt = &t
		}

		code, err := s.CreateInvite(ctx, account.ID, maxUses, expiresAt)
		if err != nil {
			return fmt.Errorf("moansubs invite create: %w", err)
		}

		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "Invite code for %q: %s\n", inviteCreateFor, code)
		_, _ = fmt.Fprintf(out, "Share: /register?invite=%s\n", code)
		return nil
	},
}

var inviteListFor string

// inviteListCmd prints tab-separated invites, either one account's own
// (--for) or every code on the node.
var inviteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List invite codes",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		s, ctx, cancel, err := openStore(cmd, "invite list")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		out := cmd.OutOrStdout()

		if inviteListFor != "" {
			account, err := s.GetAccountByName(ctx, inviteListFor)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("moansubs invite list: no account named %q", inviteListFor)
				}
				return fmt.Errorf("moansubs invite list: %w", err)
			}
			invites, err := s.InvitesByCreator(ctx, account.ID)
			if err != nil {
				return fmt.Errorf("moansubs invite list: %w", err)
			}
			if len(invites) == 0 {
				_, _ = fmt.Fprintf(out, "No invite codes for %q.\n", inviteListFor)
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "CODE\tUSES\tSTATUS\tEXPIRES\tCREATED")
			for _, inv := range invites {
				uses, status, expires, created := inviteFields(inv)
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", inv.Code, uses, status, expires, created)
			}
			return w.Flush()
		}

		invites, err := s.ListInvitesWithCreators(ctx)
		if err != nil {
			return fmt.Errorf("moansubs invite list: %w", err)
		}
		if len(invites) == 0 {
			_, _ = fmt.Fprintln(out, "No invite codes.")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "CODE\tFOR\tUSES\tSTATUS\tEXPIRES\tCREATED")
		for _, inv := range invites {
			uses, status, expires, created := inviteFields(inv.Invite)
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", inv.Code, inv.CreatedByName, uses, status, expires, created)
		}
		return w.Flush()
	},
}

// inviteFields renders an invite's mutable state as the four columns
// shared by both `invite list` shapes above.
func inviteFields(inv store.Invite) (uses, status, expires, created string) {
	uses = fmt.Sprintf("%d/∞", inv.Uses)
	if inv.MaxUses != nil {
		uses = fmt.Sprintf("%d/%d", inv.Uses, *inv.MaxUses)
	}
	status = "active"
	if inv.DisabledAt != nil {
		status = "disabled"
	}
	expires = "never"
	if inv.ExpiresAt != nil {
		expires = inv.ExpiresAt.UTC().Format(time.RFC3339)
	}
	created = inv.CreatedAt.UTC().Format(time.RFC3339)
	return uses, status, expires, created
}

// inviteDisableCmd is the operator's blunt instrument: disables any code
// regardless of who created it, unlike /me's disable button which only
// lets a member disable their own (or an admin's, via requireRole).
var inviteDisableCmd = &cobra.Command{
	Use:   "disable <code>",
	Short: "Disable an invite code",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, ctx, cancel, err := openStore(cmd, "invite disable")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if _, err := s.GetInvite(ctx, args[0]); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs invite disable: no invite code %q", args[0])
			}
			return fmt.Errorf("moansubs invite disable: %w", err)
		}
		if err := s.DisableInvite(ctx, args[0]); err != nil {
			return fmt.Errorf("moansubs invite disable: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Invite code %q disabled.\n", args[0])
		return nil
	},
}

func init() {
	inviteCreateCmd.Flags().StringVar(&inviteCreateFor, "for", "", "account the code is attributed to (required)")
	inviteCreateCmd.Flags().IntVar(&inviteCreateUses, "uses", 0, "maximum redemptions")
	inviteCreateCmd.Flags().BoolVar(&inviteCreateUnlimited, "unlimited", false, "no redemption limit")
	inviteCreateCmd.Flags().DurationVar(&inviteCreateExpires, "expires", 0, "how long the code stays valid (e.g. 720h); 0 means never")
	inviteListCmd.Flags().StringVar(&inviteListFor, "for", "", "only this account's codes")
	inviteCmd.AddCommand(inviteCreateCmd, inviteListCmd, inviteDisableCmd)
	rootCmd.AddCommand(inviteCmd)
}
