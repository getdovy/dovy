// Package log keeps one file per operation (principles topic 8, rule 7).
package log

import (
	"os"
	"path/filepath"
	"time"
)

// Dir is where operation logs go.
const Dir = "/var/log/dovy"

// Open creates a new log file in dir, named after the time.
func Open(dir string, now time.Time) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := filepath.Join(dir, now.Format("2006-01-02T15:04:05")+".log")
	return os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}
