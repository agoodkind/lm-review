// Package main is the lm-review CLI entrypoint.
package main

import (
	"context"
	"os/exec"
)

// newBgCmd creates an [exec.Cmd] configured to run detached from the
// current process.
func newBgCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd
}
