// Package state holds dovy's lock and plan journal in /var/lib/dovy
// (principles 8.1, 8.3, 8.4).
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Dir is dovy's system folder, owned by root.
const Dir = "/var/lib/dovy"

// ErrBusy: another dovy holds the lock.
var ErrBusy = errors.New("another dovy is running")

// Lock takes the lock on dir, a root-owned folder, opened read-only.
// The lock lasts until the returned file is closed or dovy exits.
func Lock(dir string) (*os.File, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return f, nil
}

// Package is one package of a plan.
type Package struct {
	Name, Version string
}

// Journal is the plan, kept while apt runs (8.3).
type Journal struct {
	Command  string    `json:"command"`
	Recipes  []string  `json:"recipes"`
	Packages []Package `json:"packages"`
	Started  time.Time `json:"started"`
}

// JournalPath is the journal's place in dir.
func JournalPath(dir string) string { return filepath.Join(dir, "journal") }

// Unfinished reads the journal of an operation that did not finish.
func Unfinished(dir string) (*Journal, error) {
	b, err := os.ReadFile(JournalPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j := &Journal{}
	return j, json.Unmarshal(b, j)
}

// Encode writes j to a file in tmp, for root to install in place.
func (j *Journal) Encode(tmp string) (string, error) {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(tmp, "journal")
	return p, os.WriteFile(p, append(b, '\n'), 0o644)
}
