package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"goodkind.io/lm-review/internal/clock"
)

const (
	reviewIDEntropyBytes = 8
	diffHashPrefixBytes  = 8
)

func newReviewID() string {
	var raw [reviewIDEntropyBytes]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "lmrev-" + hex.EncodeToString(raw[:])
	}

	return fmt.Sprintf("lmrev-%d", clock.Now().UnixNano())
}

func diffHash(diff string) string {
	if diff == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(diff))

	return hex.EncodeToString(sum[:diffHashPrefixBytes])
}
