package main

import (
	"errors"

	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the moansubs HTTP server",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		// The store, API and lookup layers land in later steps (see PLAN.md
		// Order of work). Until then, fail loudly rather than pretending to
		// listen on a port that serves nothing.
		return errors.New("moansubs serve: not implemented yet")
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
