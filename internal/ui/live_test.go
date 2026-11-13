package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestBar(t *testing.T) {
	for _, c := range []struct {
		done, total int64
		want        string
	}{{0, 100, "[--------]"}, {50, 100, "[####----]"}, {100, 100, "[########]"}, {150, 100, "[########]"}, {5, 0, "[--------]"}, {-1, 10, "[--------]"}} {
		if got := Bar(c.done, c.total, 8); got != c.want {
			t.Errorf("Bar(%d, %d) = %s, want %s", c.done, c.total, got, c.want)
		}
	}
}

func TestPlain(t *testing.T) {
	if got := plain("\033[1;32mok\033[0m\tzig build \r→ done\n"); got != "ok zig build ? done" {
		t.Errorf("got %q", got)
	}
}

// The line fits the width, is ASCII, and is erased before other output.
func TestLive(t *testing.T) {
	var b bytes.Buffer
	term.mu.Lock()
	term.w, term.width, term.color = &b, 40, false
	term.mu.Unlock()
	defer func() { term.mu.Lock(); term.w = nil; term.mu.Unlock() }()

	u := &UI{Out: guarded{&b}, Width: 40}
	l := u.Live(Green, "Fetching", func(*Live) (string, string) {
		return Bar(1, 2, 16) + "  50%  a-very-long-file-name-that-does-not-fit.tar.gz", "3.1 MiB/s"
	})
	time.Sleep(250 * time.Millisecond) // a few frames
	u.Say(Green, "Fetched:", "file")
	l.Stop()
	l.Stop()
	out := b.String()
	frames := strings.Split(out, "\r\033[K")
	if len(frames) < 3 {
		t.Fatalf("no redraws: %q", out)
	}
	for _, f := range frames {
		line, _, _ := strings.Cut(f, "\n")
		if len(line) > 39 {
			t.Errorf("%d columns: %q", len(line), line)
		}
		for _, r := range line {
			if r > 0x7e {
				t.Errorf("not ASCII: %q", line)
			}
		}
	}
	if !strings.Contains(out, "| Fetching     [########--------]  50%") {
		t.Errorf("first frame: %q", frames[1])
	}
	// The permanent line starts on a cleared row, and nothing is left after Stop.
	if !strings.Contains(out, "\r\033[K  Fetched:     file\n") || !strings.HasSuffix(out, "\r\033[K") && !strings.HasSuffix(out, "file\n") {
		t.Errorf("erase order: %q", out[max(0, len(out)-80):])
	}
	if term.cur != nil || term.drawn {
		t.Error("line left after Stop")
	}
}

// Without a terminal nothing is drawn.
func TestLiveNoTerminal(t *testing.T) {
	var b bytes.Buffer
	u := &UI{Out: &b, Width: 80}
	l := u.Live(Green, "Fetching", func(*Live) (string, string) { return "x", "" })
	time.Sleep(120 * time.Millisecond)
	l.Stop()
	var none *Live
	none.Stop()
	if b.Len() != 0 {
		t.Errorf("drew %q", b.String())
	}
	if e := l.Elapsed(); e != "0s" {
		t.Errorf("elapsed %s", e)
	}
}
