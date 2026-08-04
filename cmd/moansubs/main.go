// Command moansubs runs the moansubs server and its supporting CLI.
//
// See https://github.com/Anastylosis/MoanSubs for documentation.
package main

import (
	"os"

	"github.com/spf13/cobra"
)

// Set by -ldflags at release build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "moansubs",
	Short: "moansubs — a self-hostable subtitle database for Stash",
}

func main() {
	rootCmd.Version = version + " (" + commit + ", " + date + ")"
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
