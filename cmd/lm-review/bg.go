package main

import (
	"os/exec"
)

// newBgCmd creates an exec.Cmd configured to run detached from the current process.
func newBgCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd
}
