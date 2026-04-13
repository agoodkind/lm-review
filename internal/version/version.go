package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// Set via ldflags at build time.
var (
	Commit  = "unknown"
	Version = "dev"
	Dirty   = "false"
)

// BuildHash computes the SHA-256 of the running binary, truncated to 12 hex chars.
func BuildHash() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	f, err := os.Open(exe)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))[:12]
}
