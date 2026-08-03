package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Wasylq/moansubs/internal/store"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply any pending database migrations and exit",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return fmt.Errorf("moansubs migrate: DATABASE_URL is not set")
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()

		// store.Open runs Migrate before returning, so opening (and
		// immediately closing) the store is the entire command.
		s, err := store.Open(ctx, dsn)
		if err != nil {
			return fmt.Errorf("moansubs migrate: %w", err)
		}
		s.Close()

		fmt.Fprintln(cmd.OutOrStdout(), "migrations applied")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}
