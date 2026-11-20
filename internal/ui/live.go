package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The live line is ASCII only, never wider than the terminal, and erased before
// any other output.

// terminal is the one screen stdout and stderr share.
type terminal struct {
	mu    sync.Mutex
	w     io.Writer // nil: no terminal
	width int
	color bool
	cur   *Live
	drawn bool
	end   [2]byte // the last two bytes written
	any   bool
}

func (t *terminal) needBlank() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.any && t.end != [2]byte{'\n', '\n'}
}

var term terminal

type guarded struct{ w io.Writer }

func (g guarded) Write(p []byte) (int, error) {
	term.mu.Lock()
	defer term.mu.Unlock()
	term.erase()
	for _, c := range p {
		term.end = [2]byte{term.end[1], c}
	}
	term.any = term.any || len(p) > 0
	return g.w.Write(p)
}

func (t *terminal) erase() {
	if t.drawn {
		io.WriteString(t.w, "\r\033[K")
		t.drawn = false
	}
}

func (t *terminal) paint(code, s string) string {
	if !t.color || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

type Live struct {
	color, verb string
	render      func(*Live) (text, tail string)
	start       time.Time
	frame       int
	stop        chan struct{}
}

const frames = `|/-\`

// Live replaces a running live line; on no terminal it does nothing.
func (u *UI) Live(color, verb string, render func(*Live) (text, tail string)) *Live {
	l := &Live{color: color, verb: verb, render: render, start: time.Now(), stop: make(chan struct{})}
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.w == nil {
		return l
	}
	if term.cur != nil {
		close(term.cur.stop)
	}
	term.cur = l
	term.draw()
	go l.run()
	return l
}

func (l *Live) run() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			term.mu.Lock()
			if term.cur == l {
				l.frame++
				term.draw()
			}
			term.mu.Unlock()
		}
	}
}

// Stop is safe on nil and more than once.
func (l *Live) Stop() {
	if l == nil {
		return
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.cur == l {
		close(l.stop)
		term.cur = nil
		term.erase()
	}
}

func (l *Live) Elapsed() string {
	s := int(time.Since(l.start).Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %02ds", s/60, s%60)
}

func (l *Live) Rate(n int64) string {
	if d := time.Since(l.start).Seconds(); d >= 0.5 && n > 0 {
		return Bytes(int64(float64(n)/d)) + "/s"
	}
	return ""
}

func (t *terminal) draw() {
	l := t.cur
	text, tail := l.render(l)
	text, tail = plain(text), plain(tail)
	room := t.width - label - 4 // spinner, verb, spaces, one spare column
	text = cut(text, room)
	if room -= utf8.RuneCountInString(text) + 2; room < 1 {
		tail = ""
	}
	s := "\r\033[K" + t.paint(Bold, frames[l.frame%4:l.frame%4+1]) + " " + t.paint(l.color, pad(l.verb, label)) + " " + text
	if tail = cut(tail, room); tail != "" {
		s += "  " + t.paint(Dim, tail)
	}
	io.WriteString(t.w, s)
	t.drawn = true
}

func cut(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// plain: tool output may carry colors, tabs or wide characters that break the width.
func plain(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case esc:
			esc = !(r >= '@' && r <= '~' && r != '[')
		case r == '\033':
			esc = true
		case r == '\t':
			b.WriteByte(' ')
		case r >= ' ' && r < 0x7f:
			b.WriteRune(r)
		case r >= 0x80:
			b.WriteByte('?')
		}
	}
	return strings.TrimSpace(b.String())
}

func Bar(done, total int64, width int) string {
	n := 0
	if total > 0 {
		n = int(min(max(done, 0), total) * int64(width) / total)
	}
	return "[" + strings.Repeat("#", n) + strings.Repeat("-", width-n) + "]"
}
