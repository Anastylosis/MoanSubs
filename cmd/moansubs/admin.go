package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Operator-side node maintenance commands",
}

// adminBootstrapCmd is bootstrapAdmin's (serve.go) on-demand door: for an
// operator who ran `serve` with MOANSUBS_BOOTSTRAP_ADMIN=false specifically
// to keep credentials out of container logs, and would rather mint the
// first admin account via `docker compose exec` instead, where the output
// lands in their own terminal rather than anything collected by a log
// shipper. Always passes enabled=true — the env var that disables the
// automatic step at serve startup has no bearing on an explicit, manual
// invocation of this command.
var adminBootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Create the node's first admin account if none exists yet",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		s, ctx, cancel, err := openStore(cmd, "admin bootstrap")
		if err != nil {
			return err
		}
		defer cancel()
		defer s.Close()

		created, err := bootstrapAdmin(ctx, s, os.Getenv("MOANSUBS_ADMIN_NAME"), true, cmd.OutOrStdout())
		if err != nil {
			return fmt.Errorf("moansubs admin bootstrap: %w", err)
		}
		if !created {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "moansubs admin bootstrap: an admin account already exists; nothing to do")
		}
		return nil
	},
}

func init() {
	adminCmd.AddCommand(adminBootstrapCmd)
	rootCmd.AddCommand(adminCmd)
}
