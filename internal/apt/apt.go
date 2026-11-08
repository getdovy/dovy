// Package apt runs apt-get and apt-cache and reads their output
// (principles 8.3, 8.9).
package apt

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Action is one line of an apt-get simulation.
type Action struct {
	Op      string // Inst, Remv, Purg
	Name    string
	Old     string // installed version, if any
	Version string // new version (Inst)
	Origin  string // "dovy" for local .debs, else the archive's origin
}

var actionRe = regexp.MustCompile(`^(Inst|Remv|Purg) (\S+)(?: \[([^\]]*)\])?(?: \((\S+) ([^\[]*)\[[^\]]*\]\))?`)

// Parse reads the actions from `apt-get -s` output.
func Parse(out string) []Action {
	var as []Action
	for _, l := range strings.Split(out, "\n") {
		m := actionRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		origin := strings.TrimSpace(m[5])
		if i := strings.IndexAny(origin, ":,"); i >= 0 {
			origin = origin[:i]
		}
		switch origin {
		case "local-deb":
			origin = "dovy"
		case "Debian Backports":
			origin = "Backports"
		}
		as = append(as, Action{Op: m[1], Name: m[2], Old: m[3], Version: m[4], Origin: origin})
	}
	return as
}

// NoLongerRequired reads apt's "no longer required" list.
func NoLongerRequired(out string) []string {
	var names []string
	in := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "The following package") && strings.Contains(l, "no longer required"): // one or many
			in = true
		case in && strings.HasPrefix(l, "  "):
			names = append(names, strings.Fields(l)...)
		default:
			in = false
		}
	}
	return names
}

// Simulate runs `apt-get -s` as the user with extra options; nothing changes.
func Simulate(opts []string, args ...string) (string, error) {
	cmd := exec.Command("apt-get", append(append([]string{"-s", "-q", "-o", "Debug::NoLocking=1"}, opts...), args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("apt-get -s: %s", firstError(errb.String(), err))
	}
	return out.String(), nil
}

func firstError(s string, err error) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "E: ") {
			return strings.TrimPrefix(l, "E: ")
		}
	}
	return err.Error()
}

// Size is a package's download and installed size in bytes.
type Size struct{ Download, Installed int64 }

// Sizes looks up name=version pairs with apt-cache; unknown ones are left out.
func Sizes(opts []string, pkgs map[string]string) map[string]Size {
	if len(pkgs) == 0 {
		return nil
	}
	args := append(append([]string{}, opts...), "show", "--no-all-versions")
	for n, v := range pkgs {
		args = append(args, n+"="+v)
	}
	out, _ := exec.Command("apt-cache", args...).Output()
	sizes := map[string]Size{}
	var name string
	var s Size
	for _, l := range append(strings.Split(string(out), "\n"), "") {
		k, v, _ := strings.Cut(l, ": ")
		switch k {
		case "Package":
			name = v
		case "Size":
			s.Download, _ = strconv.ParseInt(v, 10, 64)
		case "Installed-Size":
			kib, _ := strconv.ParseInt(v, 10, 64)
			s.Installed = kib * 1024
		case "":
			if name != "" {
				sizes[name] = s
			}
			name, s = "", Size{}
		}
	}
	return sizes
}

// Status is a tidy line from apt's status channel (APT::Status-Fd).
type Status struct{ Verb, Package string }

// ParseStatus reads a pmstatus line: "pmstatus:pkg:pct:Unpacking pkg (amd64)".
// ok is false for lines that are not status lines; Verb is empty for status
// lines not shown.
func ParseStatus(l string) (s Status, ok bool) {
	f := strings.SplitN(l, ":", 4)
	if len(f) != 4 || (f[0] != "pmstatus" && f[0] != "dlstatus" && f[0] != "pmerror" && f[0] != "pmconffile") {
		return s, false
	}
	if f[0] == "pmerror" {
		return Status{"Error:", f[1] + ": " + f[3]}, true
	}
	if f[0] != "pmstatus" {
		return s, true
	}
	verbs := []struct{ prefix, verb string }{
		{"Unpacking ", "Unpacking:"},
		{"Configuring ", "Setting up:"},
		{"Removing ", "Removing:"},
		{"Completely removing ", "Purging:"},
		{"Running post-installation trigger ", "Processing:"},
	}
	for _, v := range verbs {
		if rest, found := strings.CutPrefix(f[3], v.prefix); found {
			name, _, _ := strings.Cut(rest, " ")
			return Status{v.verb, name}, true
		}
	}
	return s, true
}

// chatter is apt-get's own text, shown as tidy lines instead.
var chatter = []string{
	"Reading package lists", "Building dependency tree", "Reading state information",
	"The following ", "Suggested packages", "Recommended packages", "Use '",
	"0 upgraded", "Need to get", "After this operation", "Fetched ", "Download complete",
	"Selecting previously", "(Reading database", "Preparing to unpack", "Unpacking ",
	"Setting up ", "Processing triggers", "Removing ", "Purging configuration",
	"Get:", "Hit:", "Ign:", "Err:",
}

// Chatter reports whether l is apt's own text rather than a package's output.
func Chatter(l string) bool {
	if strings.HasPrefix(l, "  ") || strings.TrimSpace(l) == "" {
		return true
	}
	for _, c := range chatter {
		if strings.HasPrefix(l, c) {
			return true
		}
	}
	return false
}

// Fetch is one line of apt's download or update output.
type Fetch struct {
	Kind    string // Get, Hit, Ign, Err
	Text    string // "name version" for a package, else the index
	Size    string // "1434 kB"
	Package bool
}

var arches = map[string]bool{"amd64": true, "all": true, "i386": true}

var fetchRe = regexp.MustCompile(`^(Get|Hit|Ign|Err):\d+ (.*?)(?: \[([^\]]+)\])?$`)

// Bytes reads apt's size notation: "1434 kB", "236 B", "9.6 MB".
func Bytes(s string) int64 {
	f := strings.Fields(strings.ReplaceAll(s, ",", ""))
	if len(f) != 2 {
		return -1
	}
	n, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return -1
	}
	unit := map[string]float64{"B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9}[f[1]]
	if unit == 0 {
		return -1
	}
	return int64(n * unit)
}

// ParseFetch reads "Get:1 http://deb.debian.org/debian trixie/main amd64 ripgrep amd64 14.1.1-1+b4 [1434 kB]".
func ParseFetch(l string) (Fetch, bool) {
	m := fetchRe.FindStringSubmatch(l)
	if m == nil {
		return Fetch{}, false
	}
	fe := Fetch{Kind: m[1], Text: m[2], Size: m[3]}
	// A package ends in "<name> [<arch>] <version>"; versions start with a
	// digit, index names never do.
	f := strings.Fields(m[2])
	if n := len(f); m[1] == "Get" && n >= 3 && f[n-1][0] >= '0' && f[n-1][0] <= '9' {
		name := f[n-2]
		if arches[name] {
			name = f[n-3]
		}
		fe.Text, fe.Package = name+" "+f[n-1], true
	}
	return fe, true
}

// Lines streams r line by line to fn and to log.
func Lines(r io.Reader, log io.Writer, fn func(string)) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		fmt.Fprintln(log, s.Text())
		fn(s.Text())
	}
}

// Run runs `sudo apt-get args` and streams its output, merged, to fn and log.
// Questions were asked before, by dpkg-preconfigure (8.11). The status
// channel is fd 3, a copy of stdout made after sudo (which closes fds above
// 2): apt closes the status fd for maintainer scripts, so it must not be 1.
func Run(log io.Writer, fn func(string), args ...string) error {
	cmd := exec.Command("sudo", append([]string{"--", "env", "DEBIAN_FRONTEND=noninteractive", "sh", "-c", `exec apt-get "$@" 3>&1`, "apt-get"}, args...)...)
	cmd.Stdin = os.Stdin
	r, w := io.Pipe()
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { Lines(r, log, fn); close(done) }()
	err := cmd.Wait()
	w.Close()
	<-done
	if err != nil {
		return fmt.Errorf("apt-get %s: %w", args[0], err)
	}
	return nil
}

// Options every real apt-get run gets: status lines on fd 3 (see Run), wait for
// apt's lock, keep the user's changed config files (8.4, 8.5, 8.11).
var Options = []string{
	"-o", "APT::Status-Fd=3", "-o", "Dpkg::Use-Pty=0", "-o", "DPkg::Lock::Timeout=-1",
	"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
}
