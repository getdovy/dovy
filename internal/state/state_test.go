package state

import (
	"os/exec"
	"testing"
	"time"
)

func TestLock(t *testing.T) {
	dir := t.TempDir()
	f, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open file: a second open conflicts, as another process would.
	if _, err := Lock(dir); err != ErrBusy {
		t.Errorf("second lock: %v", err)
	}
	f.Close()
	g, err := Lock(dir)
	if err != nil {
		t.Errorf("after release: %v", err)
	}
	g.Close()
}

func TestJournal(t *testing.T) {
	dir := t.TempDir()
	if j, err := Unfinished(dir); j != nil || err != nil {
		t.Fatalf("empty: %v %v", j, err)
	}
	j := &Journal{Command: "install", Recipes: []string{"zram"}, Packages: []Package{{"dovy-zram", "1-1~dovy13"}}, Started: time.Unix(0, 0).UTC()}
	p, err := j.Encode(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("cp", p, JournalPath(dir)).Run(); err != nil {
		t.Fatal(err)
	}
	got, err := Unfinished(dir)
	if err != nil || got.Command != "install" || got.Packages[0].Version != "1-1~dovy13" {
		t.Errorf("got %+v %v", got, err)
	}
}
