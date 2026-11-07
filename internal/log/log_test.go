package log

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dovy")
	now := time.Date(2026, 11, 5, 18, 2, 11, 0, time.UTC)
	f, err := Open(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if want := filepath.Join(dir, "2026-11-05T18:02:11.log"); f.Name() != want {
		t.Errorf("got %s, want %s", f.Name(), want)
	}
	if _, err := Open(dir, now); err == nil {
		t.Error("a second log with the same name overwrote the first")
	}
}
