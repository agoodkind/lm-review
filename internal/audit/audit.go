// Package audit writes structured JSONL review events to XDG_STATE_HOME.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"goodkind.io/lm-review/internal/xdg"
)

// Entry is one line in the audit log.
type Entry struct {
	Timestamp  time.Time `json:"ts"`
	Scope      string    `json:"scope"`            // diff | pr | repo
	Model      string    `json:"model"`
	DiffHash   string    `json:"diff_hash,omitempty"`
	LatencyMS  int64     `json:"latency_ms"`
	Verdict    string    `json:"verdict"`
	IssueCount int       `json:"issue_count"`
	Error      string    `json:"error,omitempty"`
}

// Logger writes audit entries to the XDG state log.
type Logger struct {
	mu   sync.Mutex
	file *os.File
}

// New opens (or creates) the audit log file.
func New() (*Logger, error) {
	path := xdg.AuditLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create audit log dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}

	return &Logger{file: f}, nil
}

// Write appends an entry to the audit log.
func (l *Logger) Write(entry Entry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.file, "%s\n", data)
}

// Close closes the underlying log file.
func (l *Logger) Close() error {
	return l.file.Close()
}
