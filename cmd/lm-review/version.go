package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"goodkind.io/lm-review/internal/version"
)

// newVersionCmd prints build metadata. The first line begins with the binary
// name so selfupdate candidate validation (ValidateArgs "version",
// ValidateMatch "lm-review ") can confirm a downloaded binary before install.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build metadata",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "lm-review %s (%s)\n", version.Version, version.Commit)
			fmt.Fprintf(out, "commit:    %s\n", version.Commit)
			fmt.Fprintf(out, "dirty:     %s\n", version.Dirty)
			fmt.Fprintf(out, "buildHash: %s\n", version.BuildHash())
			return nil
		},
	}
}
