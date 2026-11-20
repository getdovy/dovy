// Package state holds dovy's lock and plan journal in /var/lib/dovy.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const Dir = "/var/lib/dovy"

var ErrBusy = errors.New("another dovy is running")

// Lock opens the root-owned dir read-only; the lock lasts until the file is closed or dovy exits.
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

type Package struct {
	Name, Version string
}

// Journal is kept while apt runs.
type Journal struct {
	Command  string    `json:"command"`
	Options  []string  `json:"options,omitempty"` // e.g. --reinstall, given again on recovery
	Recipes  []string  `json:"recipes"`
	Packages []Package `json:"packages"`
	Started  time.Time `json:"started"`
}

func JournalPath(dir string) string { return filepath.Join(dir, "journal") }

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

// Encode writes to tmp; root then installs the file in place.
func (j *Journal) Encode(tmp string) (string, error) {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(tmp, "journal")
	return p, os.WriteFile(p, append(b, '\n'), 0o644)
}
