// Package log keeps one file per operation (principles topic 8, rule 7).
package log

import (
	"errors"
	"fmt"
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
	stamp := filepath.Join(dir, now.Format("2006-01-02T15:04:05"))
	for i := 1; ; i++ {
		name := stamp + ".log"
		if i > 1 { // a second operation within the second
			name = fmt.Sprintf("%s-%d.log", stamp, i)
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if !errors.Is(err, os.ErrExist) || i == 100 {
			return f, err
		}
	}
}
