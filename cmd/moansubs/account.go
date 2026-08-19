package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/spf13/cobra"
)

var accountCmd = &cobra.Command{
	Use:   "account",
	Short: "Manage upload accounts",
}

// tokenKeyFromEnv reads and validates MOANSUBS_TOKEN_KEY from the environment.
// It returns the decoded 32-byte key on success, nil if unset, or an error if
// the value is invalid. Validation: 64 hex characters exactly.
func tokenKeyFromEnv() ([]byte, error) {
	v := os.Getenv("MOANSUBS_TOKEN_KEY")
	if v == "" {
		return nil, nil
	}
	key, err := hex.DecodeString(v)
	if err != nil || len(key) != 32 {
		return nil, errors.New("MOANSUBS_TOKEN_KEY must be 64 hex characters (32 bytes); generate one with `openssl rand -hex 32`")
	}
	return key, nil
}

// openStore is the DATABASE_URL boilerplate every account subcommand needs.
func openStore(cmd *cobra.Command, what string) (*store.Store, context.Context, context.CancelFunc, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, nil, nil, fmt.Errorf("moansubs %s: DATABASE_URL is not set", what)
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("moansubs %s: %w", what, err)
	}
	tokenKey, err := tokenKeyFromEnv()
	if err != nil {
		cancel()
		s.Close()
		return nil, nil, nil, fmt.Errorf("moansubs %s: %w", what, err)
	}
	s.SetTokenKey(tokenKey)
	return s, ctx, cancel, nil
}

// accountCreateCmd mints an account without going through the public
// registration endpoint — the only route on a node that has registration
// closed, and the way the operator's own account gets made.
var accountCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create an upload account and print its API token once",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, ctx, cancel, err := openStore(cmd, "account create")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		id, token, err := s.CreateAccount(ctx, args[0])
		if err != nil {
			return fmt.Errorf("moansubs account create: %w", err)
		}

		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "Account %q created (id %d).\n", args[0], id)
		_, _ = fmt.Fprintln(out, "API token — store this now, it will not be shown again:")
		_, _ = fmt.Fprintln(out, token)
		return nil
	},
}

// accountListCmd exists because self-registration means the operator no
// longer knows who holds an account without asking the database.
var accountListCmd = &cobra.Command{
	Use:   "list",
	Short: "List accounts, oldest first",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		s, ctx, cancel, err := openStore(cmd, "account list")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		accounts, err := s.ListAccounts(ctx)
		if err != nil {
			return fmt.Errorf("moansubs account list: %w", err)
		}

		out := cmd.OutOrStdout()
		if len(accounts) == 0 {
			_, _ = fmt.Fprintln(out, "No accounts.")
			return nil
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME\tSTATUS\tCREATED")
		for _, a := range accounts {
			status := "active"
			if a.Disabled {
				status = "disabled"
			}
			_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
				a.ID, a.Name, status, a.CreatedAt.UTC().Format(time.RFC3339))
		}
		return w.Flush()
	},
}

// setDisabled backs both `account disable` and `account enable`: revocation
// is a flag flip, not a delete, so the account's uploads keep their
// attribution and the name cannot be re-registered by someone else.
//
// Disabling also kills any live browser sessions (WP-C1,
// DeleteSessionsForAccount) — a revoked account should not still be logged
// in somewhere until its session cookie happens to expire. Enabling
// deliberately does not recreate anything: `enable` undoes the upload
// block, not "log the account back in everywhere".
func setDisabled(disabled bool, verb string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		s, ctx, cancel, err := openStore(cmd, "account "+verb)
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if err := s.SetAccountDisabled(ctx, args[0], disabled); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs account %s: no account named %q", verb, args[0])
			}
			return fmt.Errorf("moansubs account %s: %w", verb, err)
		}
		if disabled {
			account, err := s.GetAccountByName(ctx, args[0])
			if err != nil {
				return fmt.Errorf("moansubs account %s: %w", verb, err)
			}
			if err := s.DeleteSessionsForAccount(ctx, account.ID); err != nil {
				return fmt.Errorf("moansubs account %s: deleting sessions: %w", verb, err)
			}
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Account %q %sd.\n", args[0], verb)
		return nil
	}
}

var accountDisableCmd = &cobra.Command{
	Use:   "disable <name>",
	Short: "Revoke an account's ability to upload",
	Args:  cobra.ExactArgs(1),
	RunE:  setDisabled(true, "disable"),
}

var accountEnableCmd = &cobra.Command{
	Use:   "enable <name>",
	Short: "Restore a disabled account",
	Args:  cobra.ExactArgs(1),
	RunE:  setDisabled(false, "enable"),
}

// accountRotateTokenCmd rotates an account's API token — the action to take
// when a token has leaked. The old token becomes invalid immediately; the new
// token is shown once. Deliberately does not touch anything but token_hash:
// rotation is "my token leaked", not "log me out".
var accountRotateTokenCmd = &cobra.Command{
	Use:   "rotate-token <name>",
	Short: "Rotate an account's API token",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, ctx, cancel, err := openStore(cmd, "account rotate-token")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		token, err := s.RotateAccountToken(ctx, args[0])
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs account rotate-token: no account named %q", args[0])
			}
			return fmt.Errorf("moansubs account rotate-token: %w", err)
		}

		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "Token rotated for account %q.\n", args[0])
		_, _ = fmt.Fprintln(out, "New API token — store this now, it will not be shown again:")
		_, _ = fmt.Fprintln(out, token)
		return nil
	},
}

var accountPurgeReason string

// accountPurgeCmd is the escalation past `account disable`: withdraws every
// track the account ever uploaded (store.WithdrawTracksByUploader), then
// disables it, so a leaked/abusive account's whole contribution comes down
// in one step instead of an operator hunting down each track id by hand.
// Order matters: withdraw first, then disable — a failure between the two
// steps should never leave a disabled account whose content is still live.
var accountPurgeCmd = &cobra.Command{
	Use:   "purge <name>",
	Short: "Withdraw every track an account uploaded, then disable it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, ctx, cancel, err := openStore(cmd, "account purge")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		account, err := s.GetAccountByName(ctx, args[0])
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs account purge: no account named %q", args[0])
			}
			return fmt.Errorf("moansubs account purge: %w", err)
		}

		n, err := s.WithdrawTracksByUploader(ctx, account.ID, accountPurgeReason)
		if err != nil {
			return fmt.Errorf("moansubs account purge: withdrawing tracks: %w", err)
		}
		if err := s.SetAccountDisabled(ctx, args[0], true); err != nil {
			return fmt.Errorf("moansubs account purge: disabling account: %w", err)
		}
		// Same reasoning as `account disable` (WP-C1): a purged account
		// should not still be logged in anywhere.
		if err := s.DeleteSessionsForAccount(ctx, account.ID); err != nil {
			return fmt.Errorf("moansubs account purge: deleting sessions: %w", err)
		}

		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Withdrew %d track(s) uploaded by %q and disabled the account.\n", n, args[0])
		return nil
	},
}

// validAccountRoles mirrors migration 0009's CHECK constraint — checked
// here too so a typo gets a clean CLI error instead of a raw
// constraint-violation message from Postgres.
var validAccountRoles = map[string]bool{"user": true, "mod": true, "admin": true}

// accountRoleCmd is the operator's only way to grant mod/admin (WP-C7a) —
// nothing in this package uses the distinction yet beyond the CLI's own
// gating of who may disable someone else's invite code; WP-C7b's
// moderation surfaces are the actual consumer.
var accountRoleCmd = &cobra.Command{
	Use:   "role <name> <user|mod|admin>",
	Short: "Set an account's role",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		role := args[1]
		if !validAccountRoles[role] {
			return fmt.Errorf("moansubs account role: invalid role %q (want user, mod, or admin)", role)
		}

		s, ctx, cancel, err := openStore(cmd, "account role")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if err := s.SetAccountRole(ctx, args[0], role); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs account role: no account named %q", args[0])
			}
			return fmt.Errorf("moansubs account role: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Account %q role set to %q.\n", args[0], role)
		return nil
	},
}

// accountSetPasswordCmd is WP-C8's answer to "an account with no password
// (API-created, or a pre-existing row) can't log in": an operator sets one
// on the account's behalf. The password is read from stdin rather than a
// flag — a flag value lands in shell history, which defeats the purpose of
// a password an operator is setting on someone else's behalf.
var accountSetPasswordCmd = &cobra.Command{
	Use:   "set-password <name>",
	Short: "Set (or reset) an account's password, read from stdin",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("moansubs account set-password: reading password: %w", err)
		}
		password := strings.TrimRight(line, "\r\n")
		if n := len([]rune(password)); n < store.MinPasswordLen || n > store.MaxPasswordLen {
			return fmt.Errorf("moansubs account set-password: password must be between %d and %d characters",
				store.MinPasswordLen, store.MaxPasswordLen)
		}

		s, ctx, cancel, err := openStore(cmd, "account set-password")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		if err := s.SetAccountPassword(ctx, args[0], password); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs account set-password: no account named %q", args[0])
			}
			return fmt.Errorf("moansubs account set-password: %w", err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Password set for account %q.\n", args[0])
		return nil
	},
}

// accountShowCmd prints one account's details, including two facts derived
// rather than stored directly (has a password? is the token
// recoverable?) — WP-C8 spec's exact field list.
var accountShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show one account's details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, ctx, cancel, err := openStore(cmd, "account show")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		d, err := s.AccountDetail(ctx, args[0])
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("moansubs account show: no account named %q", args[0])
			}
			return fmt.Errorf("moansubs account show: %w", err)
		}

		status := "active"
		if d.Disabled {
			status = "disabled"
		}
		invitedBy := "none"
		if d.InvitedByName != nil {
			invitedBy = *d.InvitedByName
		}

		out := cmd.OutOrStdout()
		_, _ = fmt.Fprintf(out, "Name:               %s\n", d.Name)
		_, _ = fmt.Fprintf(out, "Role:               %s\n", d.Role)
		_, _ = fmt.Fprintf(out, "Created:            %s\n", d.CreatedAt.UTC().Format(time.RFC3339))
		_, _ = fmt.Fprintf(out, "Status:             %s\n", status)
		_, _ = fmt.Fprintf(out, "Invited by:         %s\n", invitedBy)
		_, _ = fmt.Fprintf(out, "Has password:       %s\n", yesNo(d.HasPassword))
		_, _ = fmt.Fprintf(out, "Displayable token:  %s\n", yesNo(d.HasDisplayableToken))
		return nil
	},
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func init() {
	accountPurgeCmd.Flags().StringVar(&accountPurgeReason, "reason", "", "reason recorded for the withdrawal")
	accountCmd.AddCommand(accountCreateCmd, accountListCmd, accountDisableCmd, accountEnableCmd, accountRotateTokenCmd, accountPurgeCmd, accountRoleCmd, accountSetPasswordCmd, accountShowCmd)
	rootCmd.AddCommand(accountCmd)
}
