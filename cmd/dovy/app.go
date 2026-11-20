package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/fetch"
	"github.com/getdovy/dovy/internal/repo"
	"github.com/getdovy/dovy/internal/ui"
)

// app holds what the commands need; tests replace any of it.
type app struct {
	out, errs *ui.UI
	dir       string                            // local catalog
	url       string                            // where updates come from
	signers   []byte                            // trusted keys
	installed func() (map[string]string, error) // recipe -> installed version
	in        io.Reader                         // answers to Continue?
	root      repo.Root                         // runs a command as root

	// Live line state, written by other goroutines.
	wait  *ui.Live               // while planning
	step  atomic.Pointer[string] // planning step
	file  atomic.Pointer[string] // file being fetched
	got   atomic.Int64           // its bytes so far
	total atomic.Int64           // its size, or -1
	pct   atomic.Int64           // apt's progress, percent
	dl    atomic.Bool            // that step is a download
	pkg   atomic.Pointer[string] // package apt is on
}

func (a *app) planning() func() {
	a.at("reading packages")
	a.file.Store(nil)
	a.wait = a.out.Live(ui.Cyan, "Planning", func(l *ui.Live) (string, string) {
		step := *a.step.Load()
		if f := a.file.Load(); f != nil && *f != "" { // a vendor package fetched to plan it
			step = "fetching " + *f + a.fetched()
		}
		return step, l.Elapsed()
	})
	return a.wait.Stop
}

func (a *app) at(step string) { a.step.Store(&step) }

// fetched returns " 42%", or the size so far when the total is unknown.
func (a *app) fetched() string {
	got, total := a.got.Load(), a.total.Load()
	switch {
	case total > 0:
		return fmt.Sprintf(" %d%%", got*100/total)
	case got > 0:
		return " " + ui.Bytes(got)
	}
	return ""
}

// progress is the fetch.Progress callback.
func (a *app) progress(url string, got, total int64) {
	name := filepath.Base(url)
	a.file.Store(&name)
	if total > 0 && got >= total { // done: the step shows again
		a.file.Store(nil)
	}
	a.got.Store(got)
	a.total.Store(total)
}

func (a *app) fetching(url string) *ui.Live {
	name := filepath.Base(url)
	a.got.Store(0)
	a.total.Store(-1)
	return a.out.Live(ui.Green, "Fetching", func(l *ui.Live) (string, string) {
		got, total := a.got.Load(), a.total.Load()
		if total <= 0 {
			return name + "  " + ui.Bytes(got), l.Rate(got)
		}
		return fmt.Sprintf("%s %3d%%  %s  %s of %s", ui.Bar(got, total, 16), got*100/total, name, ui.Bytes(got), ui.Bytes(total)), l.Rate(got)
	})
}

func (a *app) stepping(color, verb string) *ui.Live {
	a.dl.Store(verb == "Fetching") // apt reports its own downloads in every step
	a.pct.Store(0)
	a.pkg.Store(nil)
	return a.out.Live(color, verb, func(l *ui.Live) (string, string) {
		pct := a.pct.Load()
		s := fmt.Sprintf("%s %3d%%", ui.Bar(pct, 100, 16), pct)
		if p := a.pkg.Load(); p != nil {
			s += "  " + *p
		}
		return s, l.Elapsed()
	})
}

func newApp(out, errs *ui.UI) (*app, error) {
	dir, err := catalog.Dir()
	if err != nil {
		return nil, err
	}
	signers := catalog.Signers(catalog.KeysDir, func(s string) { errs.Say(ui.Yellow, "Warning:", s) })
	a := &app{out: out, errs: errs, dir: dir, url: catalog.URL, signers: signers,
		installed: installed, in: os.Stdin, root: repo.Sudo}
	fetch.Progress = a.progress
	return a, nil
}

func installed() (map[string]string, error) {
	out, err := exec.Command("dpkg-query", "-W", "-f", "${X-Dovy-Recipe}\t${Version}\t${db:Status-Abbrev}\n").Output()
	if err != nil {
		return nil, fmt.Errorf("dpkg-query: %w", err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) == 3 && f[0] != "" && strings.HasPrefix(f[2], "ii") {
			m[f[0]] = f[1]
		}
	}
	return m, nil
}

// A local-build's files' last change is part of its version; "" when it has none.
func localInputs(rs map[string]*catalog.Recipe) {
	for _, r := range rs {
		if r.Kind == catalog.LocalBuild {
			_, _, r.Input, _ = build.LocalFiles(r)
		}
	}
}

// load verifies the catalog on every run.
func (a *app) load() (map[string]*catalog.Recipe, int) {
	if err := catalog.Verify(a.dir, a.signers); err != nil {
		next := "dovy update"
		var tooNew catalog.TooNew
		if errors.As(err, &tooNew) { // dovy was downgraded: update cannot go back (rollback)
			next = "install the newest dovy from https://github.com/getdovy/dovy/releases"
		}
		a.errs.Fail("catalog check: "+err.Error(), "unchanged", next, "")
		return nil, exitFailed
	}
	rs, err := catalog.Load(a.dir, nil)
	if err != nil {
		a.errs.Fail("catalog check: "+err.Error(), "unchanged", "dovy update", "")
		return nil, exitFailed
	}
	tag, _ := catalog.Tag(a.dir)
	for _, r := range rs {
		r.Release = tag
	}
	localInputs(rs)
	return rs, exitOK
}

func (a *app) update() int {
	l := a.out.Live(ui.Cyan, "Fetching", func(l *ui.Live) (string, string) { return "catalog", l.Elapsed() })
	c, err := catalog.Update(a.dir, a.url, a.signers)
	l.Stop()
	if err != nil {
		sys, next := "unchanged; the previous catalog is kept", "check the network, then: dovy update"
		if _, e := os.Stat(a.dir); e != nil {
			sys = "unchanged; no catalog yet"
		}
		var tooNew catalog.TooNew
		switch {
		case errors.Is(err, catalog.ErrUntrusted):
			next = "check the maintainer keys in " + catalog.KeysDir
		case errors.As(err, &tooNew):
			next = "install the newest dovy from https://github.com/getdovy/dovy/releases"
		}
		a.errs.Fail("update: "+err.Error(), sys, next, "")
		return exitFailed
	}
	for _, s := range c.Skipped {
		a.errs.Say(ui.Yellow, "Skipped:", s.Tag+": "+s.Why)
	}
	if len(c.Added)+len(c.Updated)+len(c.Dropped) == 0 {
		fmt.Fprintln(a.out.Out, "Catalog verified: "+c.Tag+". Nothing new.")
		return exitOK
	}
	a.out.Rule("", "Catalog updated")
	a.out.Say(ui.Bold, "Release:", c.Tag)
	rs, _ := catalog.Load(a.dir, nil)
	for _, n := range c.Added {
		a.out.Say(ui.Green, "Added:", n+" "+rs[n].Version)
	}
	for _, u := range c.Updated {
		a.out.Say(ui.Cyan, "Updated:", u.Name+" "+u.Old+" -> "+u.New)
	}
	for _, n := range c.Dropped {
		a.out.Say(ui.Yellow, "Dropped:", n)
	}
	return exitOK
}

type row struct {
	name, version, status, note, description string
}

func (a *app) rows() ([]row, int) {
	rs, code := a.load()
	if code != exitOK {
		return nil, code
	}
	inst, err := a.installed()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return nil, exitFailed
	}
	var out []row
	for n, r := range rs {
		x := row{name: n, version: r.Upstream(), status: "available", note: note(r), description: r.Description}
		if why := unavailable(r, rs); why != "" {
			x.status, x.note = "not available", why
		}
		if v, ok := inst[n]; ok {
			x.version, x.status = v, "installed"
			if upstream(v) != r.Upstream() {
				x.status = "update " + r.Upstream()
			}
		}
		out = append(out, x)
	}
	for n, v := range inst {
		if rs[n] == nil {
			out = append(out, row{name: n, version: v, status: "orphaned", note: "dropped from the catalog"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, exitOK
}

// upstream strips the Debian revision: 1.0.0.85-1~dovy1 -> 1.0.0.85.
func upstream(v string) string {
	if i := strings.LastIndex(v, "-"); i > 0 {
		v = v[:i]
	}
	if i := strings.Index(v, ":"); i >= 0 {
		v = v[i+1:]
	}
	return v
}

func note(r *catalog.Recipe) string {
	switch {
	case len(r.Conflicts) > 0:
		return "pair: " + strings.Join(r.Conflicts, ", ")
	case r.Repo != nil:
		if u, err := url.Parse(r.Repo.URI); err == nil {
			return u.Host + " repository"
		}
	}
	return ""
}

func (a *app) list(args []string) int {
	rows, code := a.rows()
	if code != exitOK {
		return code
	}
	filter := ""
	if len(args) == 1 {
		filter = args[0]
	}
	keep := map[string]func(row) bool{
		"":            func(row) bool { return true },
		"--installed": func(r row) bool { return r.status != "available" && r.status != "not available" },
		"--updates":   func(r row) bool { return strings.HasPrefix(r.status, "update") },
		"--orphaned":  func(r row) bool { return r.status == "orphaned" },
	}[filter]
	var grid [][4]string
	for _, r := range rows {
		if keep(r) {
			grid = append(grid, [4]string{r.name, r.version, r.status, r.note})
		}
	}
	title := map[string]string{"": "Catalog", "--installed": "Installed", "--updates": "Updates", "--orphaned": "Orphaned"}[filter]
	a.out.Grid(fmt.Sprintf("%s (%d)", title, len(grid)), [4]string{"Recipe", "Version", "Status", "Note"}, grid)
	hint := map[string]string{"--updates": "Apply: dovy upgrade", "--orphaned": "Remove: dovy remove <recipe>"}[filter]
	if hint != "" && len(grid) > 0 {
		a.out.Blank()
		fmt.Fprintln(a.out.Out, a.out.Paint(ui.Dim, hint))
	}
	return exitOK
}

func (a *app) search(word string) int {
	rows, code := a.rows()
	if code != exitOK {
		return code
	}
	w := strings.ToLower(word)
	var grid [][4]string
	for _, r := range rows {
		if strings.Contains(r.name, w) || strings.Contains(strings.ToLower(r.description), w) {
			grid = append(grid, [4]string{r.name, r.version, r.status, r.description})
		}
	}
	if len(grid) == 0 {
		fmt.Fprintf(a.out.Out, "No recipe matches %q.\n", word)
		return exitOK
	}
	title := fmt.Sprintf("%d results", len(grid))
	if len(grid) == 1 {
		title = "1 result"
	}
	a.out.Grid(title, [4]string{"Recipe", "Version", "Status", "Description"}, grid)
	return exitOK
}

func (a *app) show(args []string) int {
	name, opt := "", ""
	for _, x := range args {
		if strings.HasPrefix(x, "-") {
			opt = x
		} else {
			name = x
		}
	}
	rs, code := a.load()
	if code != exitOK {
		return code
	}
	r := rs[name]
	if r == nil {
		a.errs.Fail("no recipe "+name, "unchanged", "dovy search "+name, "")
		return exitFailed
	}
	inst, err := a.installed()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return exitFailed
	}
	switch opt {
	case "--recipe":
		b, err := os.ReadFile(filepath.Join(a.dir, "recipes", name, "recipe.toml"))
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "dovy update", "")
			return exitFailed
		}
		a.out.Rule("", name+"/recipe.toml")
		io.WriteString(a.out.Out, string(b))
		return exitOK
	case "--diff":
		if _, ok := inst[name]; !ok {
			a.errs.Fail(name+" is not installed; nothing to compare", "unchanged", "dovy show "+name+" --recipe", "")
			return exitFailed
		}
		return a.diff(rs[name])
	}
	version := r.Upstream()
	if v, ok := inst[name]; ok {
		version = v + " (installed)"
		if upstream(v) != r.Upstream() {
			version += ", " + r.Upstream() + " available"
		}
	}
	a.out.Rule("", name)
	a.out.KV("Description", r.Description)
	a.out.KV("Version", version)
	a.out.KV("Kind", r.Kind)
	a.out.KV("Source", source(r))
	a.out.KV("Requires", or(strings.Join(r.Requires, ", "), "nothing"))
	if len(r.Depends) > 0 {
		a.out.KV("Depends", strings.Join(r.Depends, ", "))
	}
	a.out.KV("Replaces", or(strings.Join(r.Replaces, ", "), "nothing"))
	hw := or(r.Hardware, "any")
	if why := unavailable(r, rs); why != "" {
		hw += "; not available: " + why
	}
	a.out.KV("Hardware", hw)
	a.out.KV("Debian", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(r.Debian)), ", "), "[]"))
	a.out.KV("Maintainer", r.Maintainer)
	if r.UserNotes != "" {
		a.out.Blank()
		a.out.Rule("", "Per-user steps")
		for _, l := range strings.Split(strings.TrimSpace(r.UserNotes), "\n") {
			fmt.Fprintln(a.out.Out, "  "+l)
		}
	}
	return exitOK
}

func source(r *catalog.Recipe) string {
	switch {
	case len(r.Sources) > 0:
		return r.Sources[0].URL
	case r.Repo != nil:
		return r.Repo.URI
	case r.SourcePackage != "":
		return "Debian testing: " + r.SourcePackage
	case len(r.Packages) > 0:
		return "Debian: " + strings.Join(r.Packages, " ")
	case len(r.Members) > 0:
		return "bundle: " + strings.Join(r.Members, " ")
	case r.Local != nil:
		return "your files: " + strings.Join(r.Local.Match, ", ") + " in " + r.Local.Path
	}
	return "files only"
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// diff fetches and verifies the installed build's release to compare against.
func (a *app) diff(r *catalog.Recipe) int {
	out, _ := exec.Command("dpkg-query", "-W", "-f", "${X-Dovy-Release}", r.Package()).Output()
	was, cur := strings.TrimSpace(string(out)), r.Release
	if was == "" {
		a.errs.Fail(r.Name+" records no catalog release", "unchanged", "dovy show "+r.Name+" --recipe", "")
		return exitFailed
	}
	if was == cur {
		fmt.Fprintln(a.out.Out, "No changes since install ("+cur+").")
		return exitOK
	}
	tmp, err := os.MkdirTemp("", "dovy-")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer os.RemoveAll(tmp)
	track(tmp)
	old := filepath.Join(tmp, "catalog")
	if err := catalog.Fetch(old, a.url, was, a.signers); err != nil {
		a.errs.Fail("release "+was+": "+err.Error(), "unchanged", "check the network, then: dovy show "+r.Name+" --diff", "")
		return exitFailed
	}
	paths := []string{filepath.Join("recipes", r.Name)}
	if r.Repository != "" {
		paths = append(paths, filepath.Join("repositories", r.Repository+".toml"))
	}
	a.out.Rule("", r.Name+": "+was+" -> "+cur)
	same := true
	for _, p := range paths {
		cmd := exec.Command("git", "diff", "--no-index", "--no-color", "--", filepath.Join(old, p), filepath.Join(a.dir, p))
		b, err := cmd.Output()
		if len(b) > 0 {
			same = false
			text := strings.NewReplacer(old+"/", "/", a.dir+"/", "/").Replace(strings.TrimRight(string(b), "\n"))
			for _, l := range strings.Split(text, "\n") {
				a.out.Raw(l)
			}
		}
		if ee, ok := err.(*exec.ExitError); err != nil && (!ok || ee.ExitCode() != 1) {
			a.errs.Fail("git diff: "+err.Error(), "unchanged", "", "")
			return exitFailed
		}
	}
	if same {
		fmt.Fprintln(a.out.Out, "No changes since install.")
	}
	return exitOK
}
