// Package log keeps one file per operation.
package log

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const Dir = "/var/log/dovy"

const Keep = 100

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

// Old relies on names starting with their time; other files are not dovy's and are left.
func Old(names []string, keep int) []string {
	var logs []string
	for _, n := range names {
		if strings.HasSuffix(n, ".log") && len(n) >= len("2006-01-02T15:04:05") {
			if _, err := time.Parse("2006-01-02T15:04:05", n[:len("2006-01-02T15:04:05")]); err == nil {
				logs = append(logs, n)
			}
		}
	}
	sort.Strings(logs)
	if len(logs) <= keep {
		return nil
	}
	return logs[:len(logs)-keep]
}
