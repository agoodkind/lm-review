// Package xdg resolves XDG base directories for lm-review.
package xdg

import (
	"os"
	"path/filepath"
)

const app = "lm-review"

// ConfigDir returns $XDG_CONFIG_HOME/lm-review.
func ConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, app)
}

// StateDir returns $XDG_STATE_HOME/lm-review (logs, audit trail).
func StateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, app)
}

// RuntimeDir returns $XDG_RUNTIME_DIR/lm-review (sockets, PIDs).
// Falls back to os.TempDir() which uses TMPDIR, TEMP, TMP, or the
// OS default (/tmp on Linux, /var/folders/... on macOS, %TEMP% on Windows).
func RuntimeDir() string {
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return filepath.Join(base, app)
	}
	return filepath.Join(os.TempDir(), app)
}

// ConfigPath returns the path to config.toml.
func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.toml")
}

// AuditLogPath returns the path to the audit log.
func AuditLogPath() string {
	return filepath.Join(StateDir(), "audit.jsonl")
}

// DaemonSocketPath returns the Unix socket path for the daemon.
func DaemonSocketPath() string {
	return filepath.Join(RuntimeDir(), "daemon.sock")
}

// DaemonLogPath returns the path to the daemon log file.
func DaemonLogPath() string {
	return filepath.Join(StateDir(), "daemon.log")
}
