// Package version exposes build-time identity strings (commit, version,
// dirty flag) populated via ldflags and a content hash of the running
// binary.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// Commit is the git commit SHA the binary was built from. Set via
// ldflags at build time.
var Commit = "unknown"

// Version is the semantic version string of the binary. Set via ldflags
// at build time.
var Version = "dev"

// Dirty is "true" when the working tree had uncommitted changes at
// build time. Set via ldflags at build time.
var Dirty = "false"

// BuildHash computes the SHA-256 of the running binary, truncated to
// 12 hex chars.
func BuildHash() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	f, err := os.Open(exe)
	if err != nil {
		return "unknown"
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	_, err = io.Copy(h, f)
	if err != nil {
		return "unknown"
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
