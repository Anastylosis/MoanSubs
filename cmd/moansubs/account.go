package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Anastylosis/MoanSubs/internal/store"
	"github.com/spf13/cobra"
)

var accountCmd = &cobra.Command{
	Use:   "account",
	Short: "Manage upload accounts",
}

// accountCreateCmd is PLAN.md "Upload safety"'s admin CLI: "No
// self-registration in v1. Accounts are created by an admin CLI command
// (moansubs account create <name>) that prints a random 256-bit hex token
// exactly once; the server stores only the token's SHA-256 in
// accounts.token_hash."
var accountCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create an upload account and print its API token once",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return fmt.Errorf("moansubs account create: DATABASE_URL is not set")
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()

		s, err := store.Open(ctx, dsn)
		if err != nil {
			return fmt.Errorf("moansubs account create: %w", err)
		}
		defer s.Close()

		id, token, err := s.CreateAccount(ctx, args[0])
		if err != nil {
			return fmt.Errorf("moansubs account create: %w", err)
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Account %q created (id %d).\n", args[0], id)
		fmt.Fprintln(out, "API token — store this now, it will not be shown again:")
		fmt.Fprintln(out, token)
		return nil
	},
}

func init() {
	accountCmd.AddCommand(accountCreateCmd)
	rootCmd.AddCommand(accountCmd)
}
