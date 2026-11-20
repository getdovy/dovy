// Package apt runs apt-get and apt-cache and reads their output.
package apt

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

type Action struct {
	Op      string // Inst, Remv, Purg
	Name    string
	Old     string // installed version, if any
	Version string
	Origin  string // "dovy" for local .debs, else the archive's origin
}

var actionRe = regexp.MustCompile(`^(Inst|Remv|Purg) (\S+)(?: \[([^\]]*)\])?(?: \((\S+) ([^\[]*)\[[^\]]*\]\))?`)

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

// Unused makes apt treat a package only suggested by another as unused; apt keeps it by default.
var Unused = []string{"-o", "APT::AutoRemove::SuggestsImportant=false"}

func NoLongerRequired(out string) []string {
	var names []string
	in := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "The following package") && strings.Contains(l, "no longer required"): // "package" or "packages"
			in = true
		case in && strings.HasPrefix(l, "  "):
			names = append(names, strings.Fields(l)...)
		default:
			in = false
		}
	}
	return names
}

var (
	interrupted atomic.Bool
	running     atomic.Int32
)

func Running() bool { return running.Load() > 0 }

func Interrupted() bool { return interrupted.Load() }

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

type Size struct{ Download, Installed int64 }

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

type Status struct {
	Verb, Package string
	Download      bool
	Percent       float64 // of the whole step; -1 if none
}

// ParseStatus reads "pmstatus:pkg:pct:Unpacking pkg (amd64)". Verb is empty for lines not shown.
func ParseStatus(l string) (s Status, ok bool) {
	f := strings.SplitN(l, ":", 4)
	if len(f) != 4 || (f[0] != "pmstatus" && f[0] != "dlstatus" && f[0] != "pmerror" && f[0] != "pmconffile") {
		return s, false
	}
	s.Percent = -1
	if _, err := strconv.ParseFloat(f[2], 64); err != nil { // pkg:arch:pct:text
		if g := strings.SplitN(f[3], ":", 2); len(g) == 2 {
			f[1], f[2], f[3] = f[1]+":"+f[2], g[0], g[1]
		}
	}
	if f[0] == "pmerror" {
		return Status{Verb: "Error:", Package: f[1] + ": " + f[3], Percent: -1}, true
	}
	if pct, err := strconv.ParseFloat(f[2], 64); err == nil {
		s.Percent = pct
	}
	s.Download = f[0] == "dlstatus"
	if f[0] != "pmstatus" {
		return s, true
	}
	s.Package = f[1]
	verbs := []struct{ prefix, verb string }{
		{"Unpacking ", "Unpacking:"},
		{"Configuring ", "Setting up:"},
		{"Removing ", "Removing:"},
		{"Completely removing ", "Purging:"},
		{"Running post-installation trigger ", "Processing:"},
	}
	for _, v := range verbs {
		if rest, found := strings.CutPrefix(f[3], v.prefix); found {
			name, arch, _ := strings.Cut(rest, " ")
			if arch == "(i386)" && !strings.Contains(name, ":") { // as apt-get -s names it
				name += ":i386"
			}
			s.Verb, s.Package = v.verb, name
			return s, true
		}
	}
	return s, true
}

var chatter = []string{
	"Reading package lists", "Building dependency tree", "Reading state information",
	"The following ", "Suggested packages", "Recommended packages", "Use '",
	"0 upgraded", "Need to get", "After this operation", "Fetched ", "Download complete",
	"Selecting previously", "(Reading database", "Preparing to unpack", "Unpacking ",
	"Setting up ", "Processing triggers", "Removing ", "Purging configuration",
	"Get:", "Hit:", "Ign:", "Err:",
}

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

// forced is printed when the plan purges i386 copies of protected libraries; the plan already lists them.
var forced = []string{
	"WARNING: The following essential packages will be removed.",
	"This should NOT be done unless you know exactly what you are doing!",
	"dpkg: warning: overriding problem because --force enabled:",
	"dpkg: warning: this is a protected package; it should not be removed",
}

func Forced(l string) bool {
	l = strings.TrimSpace(l)
	for _, f := range forced {
		if l == f {
			return true
		}
	}
	return false
}

type Fetch struct {
	Kind    string
	Text    string // "name version" for a package, else the index
	Size    string // "1434 kB"
	Package bool
}

var arches = map[string]bool{"amd64": true, "all": true, "i386": true}

var fetchRe = regexp.MustCompile(`^(Get|Hit|Ign|Err):\d+ (.*?)(?: \[([^\]]+)\])?$`)

// Bytes reads apt's decimal size notation: "1434 kB", "236 B", "9.6 MB".
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

func ParseFetch(l string) (Fetch, bool) {
	m := fetchRe.FindStringSubmatch(l)
	if m == nil {
		return Fetch{}, false
	}
	fe := Fetch{Kind: m[1], Text: m[2], Size: m[3]}
	// Versions start with a digit, index names never do.
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

func Lines(r io.Reader, log io.Writer, fn func(string)) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	for s.Scan() {
		fmt.Fprintln(log, s.Text())
		fn(s.Text())
	}
}

// Run uses fd 3 for apt's status, duplicated after sudo (which closes fds above 2):
// apt closes the status fd for maintainer scripts, so it cannot be stdout.
func Run(log io.Writer, fn func(string), args ...string) error {
	cmd := exec.Command("sudo", append([]string{"--", "env", "DEBIAN_FRONTEND=noninteractive", "sh", "-c", `exec apt-get "$@" 3>&1`, "apt-get"}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // Ctrl-C reaches dovy only; apt finishes the transaction
	r, w := io.Pipe()
	cmd.Stdout, cmd.Stderr = w, w
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	running.Add(1)
	defer running.Add(-1)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		for range sig {
			if !interrupted.Swap(true) {
				fn("Ctrl-C: finishing the current apt transaction, then stopping.")
			}
		}
	}()
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

// Options waits for apt's lock and keeps the user's changed config files.
var Options = []string{
	"-o", "APT::Status-Fd=3", "-o", "Dpkg::Use-Pty=0", "-o", "DPkg::Lock::Timeout=-1",
	"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
	// A stalled download fails after 30 s and is retried instead of hanging.
	"-o", "Acquire::Retries=3", "-o", "Acquire::http::Timeout=30", "-o", "Acquire::https::Timeout=30",
}

// RestoreConfig makes dpkg recreate missing config files. dpkg keeps an edited one
// whatever the options, so the caller moves it aside first.
func RestoreConfig() []string {
	return append(append([]string{}, Options...), "-o", "Dpkg::Options::=--force-confmiss")
}
