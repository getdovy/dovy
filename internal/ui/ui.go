// Package ui draws dovy's terminal output (principles topic 12).
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

// Colors. Empty when color is off.
const (
	Bold   = "1"
	Dim    = "2"
	Red    = "31"
	Green  = "32"
	Yellow = "33"
	Cyan   = "36"
)

const maxWidth = 100

// UI writes to Out. Width is the usable line width.
type UI struct {
	Out   io.Writer
	Color bool
	Width int
}

// New sets up a UI for f: color only on a terminal without NO_COLOR.
func New(f *os.File) *UI {
	tty := isTerminal(f)
	return &UI{
		Out:   f,
		Color: tty && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
		Width: min(columns(f), maxWidth),
	}
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

// Paint wraps s in a color code when color is on.
func (u *UI) Paint(code, s string) string {
	if !u.Color || code == "" || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func (u *UI) line(s string) { fmt.Fprintln(u.Out, strings.TrimRight(s, " ")) }

// Blank prints an empty line.
func (u *UI) Blank() { fmt.Fprintln(u.Out) }

func width(s string) int { return utf8.RuneCountInString(s) }

func pad(s string, w int) string  { return s + strings.Repeat(" ", max(w-width(s), 0)) }
func rpad(s string, w int) string { return strings.Repeat(" ", max(w-width(s), 0)) + s }

// Rule prints a centered title rule: ──── Title ────
func (u *UI) Rule(color, title string) {
	t := " " + title + " "
	l := (u.Width - width(title) - 2) / 2
	r := max(u.Width-l-width(t), 0)
	u.line(u.Paint(Dim, strings.Repeat("─", max(l, 0))) + u.Paint(color, u.Paint(Bold, t)) + u.Paint(Dim, strings.Repeat("─", r)))
}

// Say prints a tool line with an aligned verb: "Unpacking:  steam (1.0)".
func (u *UI) Say(color, verb, text string) {
	u.line(u.Paint(color, pad(verb, 11)) + " " + text)
}

// Raw prints tool output indented, dim.
func (u *UI) Raw(s string) { u.line("    " + u.Paint(Dim, s)) }

// KV prints "  Key          value".
func (u *UI) KV(k, v string) { u.line("  " + u.Paint(Dim, pad(k, 12)) + " " + v) }

// Fail prints the error block (topic 8, rule 8).
func (u *UI) Fail(failed, system, next, log string) {
	u.Rule(Red, "Error")
	u.KV("Failed", failed)
	u.KV("System", system)
	u.KV("Next", next)
	if log != "" {
		u.KV("Log", log)
	}
}

// Refuse prints a refusal: nothing was changed.
func (u *UI) Refuse(recipe, reason, found string) {
	u.Rule(Yellow, "Refused")
	u.KV("Recipe", recipe)
	u.KV("Reason", reason)
	if found != "" {
		u.KV("Found", found)
	}
	u.KV("System", "unchanged")
}

// Confirm asks "Continue? [y/N]"; anything but y or yes is no.
func (u *UI) Confirm(in io.Reader) bool {
	fmt.Fprint(u.Out, "Continue? [y/N] ")
	a, _ := bufio.NewReader(in).ReadString('\n')
	if f, ok := in.(*os.File); !ok || !isTerminal(f) { // no echoed Enter ends the line
		fmt.Fprintln(u.Out)
	}
	a = strings.ToLower(strings.TrimSpace(a))
	return a == "y" || a == "yes"
}

// Bytes formats a size: 412 KiB, 4.2 MiB.
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
