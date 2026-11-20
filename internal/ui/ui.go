// Package ui draws dovy's terminal output.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

const (
	Bold   = "1"
	Dim    = "2"
	Red    = "31"
	Green  = "32"
	Yellow = "33"
	Cyan   = "36"
)

const maxWidth = 100

// Rows start at column 2, their details at 4; labels are 12 wide, so text starts at 15.
const (
	indent = "  "
	sub    = "    "
	label  = 12
)

type UI struct {
	Out   io.Writer
	Color bool
	Width int
}

func New(f *os.File) *UI {
	tty := isTerminal(f)
	u := &UI{
		Out:   f,
		Color: tty && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
		Width: min(columns(f), maxWidth),
	}
	u.Out = guarded{f}
	if tty && os.Getenv("TERM") != "dumb" && f == os.Stdout { // the live line is drawn there
		term.mu.Lock()
		term.w, term.width, term.color = f, u.Width, u.Color
		term.mu.Unlock()
	}
	return u
}

func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return e == 0
}

func columns(f *os.File) int {
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	var ws struct{ Row, Col, X, Y uint16 }
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if e == 0 && ws.Col > 0 {
		return int(ws.Col)
	}
	return 80
}

func (u *UI) Paint(code, s string) string {
	if !u.Color || code == "" || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func (u *UI) line(s string) { fmt.Fprintln(u.Out, strings.TrimRight(s, " ")) }

func (u *UI) Blank() { fmt.Fprintln(u.Out) }

func width(s string) int { return utf8.RuneCountInString(s) }

func pad(s string, w int) string  { return s + strings.Repeat(" ", max(w-width(s), 0)) }
func rpad(s string, w int) string { return strings.Repeat(" ", max(w-width(s), 0)) + s }

func (u *UI) Rule(color, title string) {
	if _, ok := u.Out.(guarded); ok && term.needBlank() { // a rule never touches the line above it
		u.Blank()
	}
	t := " " + title + " "
	l := (u.Width - width(title) - 2) / 2
	r := max(u.Width-l-width(t), 0)
	u.line(u.Paint(Dim, strings.Repeat("-", max(l, 0))) + u.Paint(color, u.Paint(Bold, t)) + u.Paint(Dim, strings.Repeat("-", r)))
}

func (u *UI) Say(color, verb, text string) {
	u.line(indent + u.Paint(color, pad(verb, label)) + " " + text)
}

func (u *UI) Under(text string) { u.line(indent + pad("", label) + " " + text) }

func (u *UI) Raw(s string) { u.line(sub + u.Paint(Dim, s)) }

func (u *UI) KV(k, v string) { u.line(indent + u.Paint(Dim, pad(k, label)) + " " + v) }

// Grace delays the error block: after Ctrl-C a dying tool returns an error, but
// dovy's own handler prints "Cancelled" within this time. Zero in tests.
var Grace time.Duration

func (u *UI) Fail(failed, system, next, log string) {
	time.Sleep(Grace)
	u.Rule(Red, "Error")
	u.KV("Failed", failed)
	u.KV("System", system)
	u.KV("Next", next)
	if log != "" {
		u.KV("Log", log)
	}
}

func (u *UI) Refuse(recipe, reason, found string) {
	time.Sleep(Grace)
	u.Rule(Yellow, "Refused")
	u.KV("Recipe", recipe)
	u.KV("Reason", reason)
	if found != "" {
		u.KV("Found", found)
	}
	u.KV("System", "unchanged")
}

func (u *UI) Confirm(in io.Reader) bool {
	fmt.Fprint(u.Out, "Continue? [y/N] ")
	a, _ := bufio.NewReader(in).ReadString('\n')
	if f, ok := in.(*os.File); !ok || !isTerminal(f) { // no echoed Enter ends the line
		fmt.Fprintln(u.Out)
	}
	a = strings.ToLower(strings.TrimSpace(a))
	return a == "y" || a == "yes"
}

func Bytes(n int64) string {
	switch {
	case n < 1024:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1024*1024:
		return strconv.FormatInt((n+512)/1024, 10) + " KiB"
	case n < 1024*1024*1024:
		return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64) + " MiB"
	}
	return strconv.FormatFloat(float64(n)/(1024*1024*1024), 'f', 1, 64) + " GiB"
}
