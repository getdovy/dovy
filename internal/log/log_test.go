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
	g, err := Open(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	g.Close()
	if want := filepath.Join(dir, "2026-11-05T18:02:11-2.log"); g.Name() != want {
		t.Errorf("second log: got %s, want %s", g.Name(), want)
	}
}
