package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/spf13/cobra"
)

var accountCmd = &cobra.Command{
	Use:   "account",
	Short: "Manage upload accounts",
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

func init() {
	accountCmd.AddCommand(accountCreateCmd, accountListCmd, accountDisableCmd, accountEnableCmd)
	rootCmd.AddCommand(accountCmd)
}
