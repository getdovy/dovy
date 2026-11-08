package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/fetch"
	"github.com/getdovy/dovy/internal/log"
	"github.com/getdovy/dovy/internal/repo"
	"github.com/getdovy/dovy/internal/state"
	"github.com/getdovy/dovy/internal/ui"
)

// resolve returns the named recipes and their depends, dependencies first.
func resolve(rs map[string]*catalog.Recipe, names []string) ([]*catalog.Recipe, string) {
	var order []*catalog.Recipe
	seen := map[string]bool{}
	var visit func(n string) string
	visit = func(n string) string {
		if seen[n] {
			return ""
		}
		seen[n] = true
		r := rs[n]
		if r == nil {
			return n
		}
		for _, d := range r.Depends {
			if missing := visit(d); missing != "" {
				return missing
			}
		}
		order = append(order, r)
		return ""
	}
	for _, n := range names {
		if missing := visit(n); missing != "" {
			return nil, missing
		}
	}
	return order, ""
}

// plan is what install will do, from apt's simulation.
type plan struct {
	recipes  []*catalog.Recipe
	versions map[string]string // dovy package → version
	actions  []apt.Action
}

// sections turns the plan into the tables of topic 12, rule 1.
func (p *plan) sections(sizes map[string]apt.Size, sources map[string]int64) ([]ui.Section, []ui.Count, []ui.Pair) {
	var ins, deps []ui.Row
	var download, disk int64
	for _, a := range p.actions {
		if a.Op != "Inst" {
			continue
		}
		size := "—"
		if _, ok := p.versions[a.Name]; ok {
			if n := sources[a.Name]; n > 0 {
				size, download, disk = ui.Bytes(n), download+n, disk+n
			}
			ins = append(ins, ui.Row{Name: a.Name, Version: a.Version, Source: "dovy", Size: size})
			continue
		}
		if s, ok := sizes[a.Name]; ok {
			size, download, disk = ui.Bytes(s.Download), download+s.Download, disk+s.Installed
		}
		row := ui.Row{Name: a.Name, Version: a.Version, Source: a.Origin, Size: size}
		if a.Old != "" {
			row.Note = "upgrades " + a.Old
		}
		deps = append(deps, row)
	}
	var secs []ui.Section
	var counts []ui.Count
	if len(ins) > 0 {
		secs = append(secs, ui.Section{Color: ui.Green, Title: "Installing", Rows: ins})
		counts = append(counts, ui.Count{Verb: "Install", N: len(ins), Unit: plural(len(ins), "package")})
	}
	if len(deps) > 0 {
		secs = append(secs, ui.Section{Color: ui.Green, Title: "Installing dependencies", Rows: deps})
		counts = append(counts, ui.Count{Verb: "Dependencies", N: len(deps), Unit: plural(len(deps), "package")})
	}
	totals := []ui.Pair{{Item: "Download", Reason: ui.Bytes(download)}, {Item: "Disk required", Reason: ui.Bytes(disk)}}
	return secs, counts, totals
}

func plural(n int, s string) string {
	if n == 1 {
		return s
	}
	return s + "s"
}

// surprise lists what apt would remove: an install plan removes nothing (4.3).
func surprise(as []apt.Action) []string {
	var out []string
	for _, a := range as {
		if a.Op == "Remv" || a.Op == "Purg" {
			out = append(out, a.Name+" "+a.Old)
		}
	}
	return out
}

// same reports whether two simulations install the same packages.
func same(a, b []apt.Action) bool {
	key := func(as []apt.Action) string {
		var s []string
		for _, x := range as {
			s = append(s, x.Op+" "+x.Name+" "+x.Version)
		}
		sort.Strings(s)
		return strings.Join(s, "\n")
	}
	return key(a) == key(b)
}

func (a *app) install(names []string) int {
	if code := a.unfinished(); code != exitOK {
		return code
	}
	rs, code := a.load()
	if code != exitOK {
		return code
	}
	fmt.Fprintln(a.out.Out, a.out.Paint(ui.Green, "Catalog signature verified."))
	order, missing := resolve(rs, names)
	if missing != "" {
		a.errs.Fail("no recipe "+missing, "unchanged", "dovy search "+missing, "")
		return exitFailed
	}
	release, err := deb.Release("/etc/os-release")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "dovy runs on Debian stable", "")
		return exitFailed
	}
	inst, err := a.installed()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return exitFailed
	}
	p := &plan{versions: map[string]string{}}
	for _, r := range order {
		if !build.Buildable(r) {
			a.errs.Refuse(r.Name, "kind "+r.Kind+" is not built yet", "")
			return exitRefused
		}
		if !supports(r, release) {
			a.errs.Refuse(r.Name, fmt.Sprintf("supports Debian %v", r.Debian), fmt.Sprintf("Debian %d", release))
			return exitRefused
		}
		v := deb.Version(r, release)
		if inst[r.Name] == v {
			continue
		}
		p.recipes = append(p.recipes, r)
		p.versions[r.Package()] = v
	}
	if len(p.recipes) == 0 {
		fmt.Fprintln(a.out.Out, "Nothing to do: "+strings.Join(names, ", ")+" already installed.")
		return exitOK
	}

	// One dovy at a time (8.4). Before dovy's own .deb exists the folder may
	// be missing; then the lock is taken after the sudo prompt.
	var lock *os.File
	if _, err := os.Stat(state.Dir); err == nil {
		if lock, code = a.lock(); code != exitOK {
			return code
		}
		defer lock.Close()
	}

	tmp, err := os.MkdirTemp("", "dovy-")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer os.RemoveAll(tmp)

	// Resolve packages: apt simulates with control-only stubs (8.3).
	var stubs []string
	for _, r := range p.recipes {
		stage := filepath.Join(tmp, "stage", r.Name)
		if err := os.MkdirAll(stage, 0o755); err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
			return exitFailed
		}
		s, err := deb.Build(r, p.versions[r.Package()], stage, filepath.Join(tmp, "stubs"))
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check that dpkg-deb works", "")
			return exitFailed
		}
		stubs = append(stubs, s)
	}
	out, err := apt.Simulate(append([]string{"install"}, stubs...)...)
	if err != nil {
		a.errs.Refuse(strings.Join(names, " "), "apt cannot install it", err.Error())
		return exitRefused
	}
	p.actions = apt.Parse(out)
	if s := surprise(p.actions); len(s) > 0 {
		a.errs.Refuse(strings.Join(names, " "), "apt would remove packages not in the plan", strings.Join(s, ", "))
		return exitRefused
	}

	// Plan, confirm (12.1, 12.2).
	debian := map[string]string{}
	for _, x := range p.actions {
		if x.Op == "Inst" && x.Origin != "dovy" {
			debian[x.Name] = x.Version
		}
	}
	sources := map[string]int64{}
	for _, r := range p.recipes {
		for _, s := range r.Sources {
			if n := fetch.Size(s.URL); n > 0 {
				sources[r.Package()] += n
			}
		}
	}
	secs, counts, totals := p.sections(apt.Sizes(debian), sources)
	a.out.Blank()
	a.out.Plan(secs)
	a.out.Summary(counts, totals)
	a.out.Blank()
	if !a.out.Confirm(a.in) {
		fmt.Fprintln(a.out.Out, "Cancelled. Nothing changed.")
		return exitCancelled
	}

	// sudo once, kept fresh for the run (8.11).
	stop, err := a.sudo()
	if err != nil {
		a.errs.Fail("sudo: "+err.Error(), "unchanged", "dovy install again", "")
		return exitFailed
	}
	defer stop()
	if lock == nil {
		if err := a.root("install", "-d", "-m", "755", state.Dir); err != nil {
			a.errs.Fail(err.Error(), "unchanged", "dovy install again", "")
			return exitFailed
		}
		if lock, code = a.lock(); code != exitOK {
			return code
		}
		defer lock.Close()
	}
	logf, err := log.Open(tmp, time.Now())
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer logf.Close()
	logPath := filepath.Join(log.Dir, filepath.Base(logf.Name()))
	keepLog := func() {
		logf.Sync()
		a.root("install", "-D", "-m", "644", logf.Name(), logPath)
	}
	defer keepLog()
	fail := func(failed, system, next string) int {
		fmt.Fprintln(logf, "Failed: "+failed)
		a.errs.Fail(failed, system, next, logPath)
		return exitFailed
	}

	// Download (12.3).
	a.out.Rule("", "Download")
	cache, err := build.CacheDir()
	if err != nil {
		return fail(err.Error(), "unchanged", "set HOME or XDG_CACHE_HOME")
	}
	for _, r := range p.recipes {
		for _, s := range r.Sources {
			path, err := fetch.Get(s.URL, s.SHA256, filepath.Join(cache, "sources"))
			if err != nil {
				return fail(err.Error(), "unchanged", "check the network, then: dovy install "+strings.Join(names, " "))
			}
			fi, _ := os.Stat(path)
			a.out.Say(ui.Green, "Fetched:", fmt.Sprintf("%s [%s] sha256 ok", filepath.Base(s.URL), ui.Bytes(fi.Size())))
		}
	}
	if len(debian) > 0 {
		args := append(append([]string{"install", "--download-only", "-y"}, apt.Options...), pkgArgs(debian)...)
		if err := apt.Run(logf, a.aptLine(nil), args...); err != nil {
			return fail(err.Error(), "unchanged", "check the network, then: dovy install "+strings.Join(names, " "))
		}
	}
	a.out.Blank()

	// Build (12.3), into the local repository.
	var debs []string
	for _, r := range p.recipes {
		a.out.Rule("", "Build: "+r.Name)
		d, err := build.Recipe(r, release, cache)
		if err != nil {
			return fail(err.Error(), "unchanged", "report it to the recipe's maintainer: "+r.Maintainer)
		}
		fmt.Fprintln(logf, "Built: "+d)
		a.out.Say(ui.Green, "Built:", fmt.Sprintf("%s (%s)", r.Package(), p.versions[r.Package()]))
		debs = append(debs, d)
		a.out.Blank()
	}
	if err := repo.Publish(repo.Dir, debs, a.root); err != nil {
		return fail(err.Error(), "unchanged", "dovy install again")
	}

	// Journal, then one apt transaction (8.3).
	a.out.Rule("", "Install")
	j := &state.Journal{Command: "install", Started: time.Now().UTC()}
	for _, r := range p.recipes {
		j.Recipes = append(j.Recipes, r.Name)
	}
	for n, v := range p.versions {
		j.Packages = append(j.Packages, state.Package{Name: n, Version: v})
	}
	jp, err := j.Encode(tmp)
	if err == nil {
		err = a.root("install", "-m", "644", jp, state.JournalPath(state.Dir))
	}
	if err != nil {
		return fail(err.Error(), "unchanged", "dovy install again")
	}
	clear := func() { a.root("rm", "-f", state.JournalPath(state.Dir)) }
	if err := apt.Run(logf, a.aptLine(nil), append([]string{"update"}, apt.Options...)...); err != nil {
		clear()
		return fail(err.Error(), "unchanged", "check the network, then: dovy install "+strings.Join(names, " "))
	}
	dovy := pkgArgs(p.versions)
	out, err = apt.Simulate(append([]string{"install"}, dovy...)...)
	if err != nil || !same(apt.Parse(out), p.actions) {
		clear()
		return fail("apt's plan changed after the build", "unchanged; built packages kept in "+repo.Dir, "dovy install "+strings.Join(names, " "))
	}
	versions := map[string]string{}
	for _, x := range p.actions {
		versions[x.Name] = x.Version
	}
	if err := apt.Run(logf, a.aptLine(versions), append(append([]string{"install", "-y"}, apt.Options...), dovy...)...); err != nil {
		return fail(err.Error(), "unknown: the operation stopped during apt", "sudo dpkg --configure -a")
	}
	clear()
	a.out.Blank()

	// Notices (12.4).
	a.notices(p.recipes, versions)
	var done []string
	for _, r := range p.recipes {
		done = append(done, r.Name)
	}
	fmt.Fprintln(a.out.Out, a.out.Paint(ui.Green, "Done.")+" "+strings.Join(done, ", ")+" installed.")
	return exitOK
}

func supports(r *catalog.Recipe, release int) bool {
	for _, d := range r.Debian {
		if d == release {
			return true
		}
	}
	return false
}

// pkgArgs is name=version for apt-get, sorted.
func pkgArgs(m map[string]string) []string {
	var s []string
	for n, v := range m {
		s = append(s, n+"="+v)
	}
	sort.Strings(s)
	return s
}

// aptLine shows one line of apt-get output: tidy verbs from the status
// channel, fetch lines, and anything else a package prints (12.3).
func (a *app) aptLine(versions map[string]string) func(string) {
	last := ""
	say := func(color, verb, text string) {
		if verb+text != last { // apt repeats some lines
			a.out.Say(color, verb, text)
		}
		last = verb + text
	}
	return func(l string) {
		if s, ok := apt.ParseStatus(l); ok {
			if s.Verb == "" {
				return
			}
			color := ui.Green
			switch s.Verb {
			case "Removing:", "Purging:", "Error:":
				color = ui.Red
			}
			text := s.Package
			if v := versions[s.Package]; v != "" {
				text += " (" + v + ")"
			}
			say(color, s.Verb, text)
			return
		}
		if f, ok := apt.ParseFetch(l); ok {
			switch f.Kind {
			case "Get":
				if f.Package {
					size := f.Size
					if n := apt.Bytes(f.Size); n >= 0 {
						size = ui.Bytes(n)
					}
					say(ui.Green, "Fetched:", f.Text+" ["+size+"]")
				} else {
					say(ui.Green, "Updated:", f.Text)
				}
			case "Hit":
				say(ui.Dim, "No Change:", f.Text)
			case "Ign":
				say(ui.Dim, "Ignored:", f.Text)
			case "Err":
				say(ui.Red, "Error:", f.Text)
			}
			return
		}
		if !apt.Chatter(l) {
			a.out.Raw(l)
		}
	}
}

// notices repeats what needs the user: reboots and per-user steps.
func (a *app) notices(rs []*catalog.Recipe, versions map[string]string) {
	var reboot []string
	if b, err := os.ReadFile("/run/reboot-required.pkgs"); err == nil {
		for _, n := range strings.Fields(string(b)) {
			if _, ok := versions[n]; ok {
				reboot = append(reboot, n)
			}
		}
	}
	var notes []*catalog.Recipe
	for _, r := range rs {
		if r.UserNotes != "" {
			notes = append(notes, r)
		}
	}
	if len(reboot) == 0 && len(notes) == 0 {
		return
	}
	a.out.Rule(ui.Yellow, "Notices")
	if len(reboot) > 0 {
		fmt.Fprintln(a.out.Out, "  Reboot required by: "+strings.Join(reboot, ", "))
	}
	for _, r := range notes {
		fmt.Fprintln(a.out.Out, "  "+r.Name+", once per user:")
		fmt.Fprintln(a.out.Out, "    "+r.UserNotes)
	}
	a.out.Blank()
}

// unfinished refuses to start while an earlier operation's journal exists.
func (a *app) unfinished() int {
	j, err := state.Unfinished(state.Dir)
	if err != nil {
		a.errs.Fail("journal: "+err.Error(), "unknown", "sudo dpkg --configure -a", "")
		return exitFailed
	}
	if j != nil {
		a.errs.Fail(fmt.Sprintf("an earlier %s did not finish (%s)", j.Command, strings.Join(j.Recipes, ", ")),
			"possibly half-installed", "sudo dpkg --configure -a, then: sudo rm "+state.JournalPath(state.Dir), "")
		return exitFailed
	}
	return exitOK
}

func (a *app) lock() (*os.File, int) {
	f, err := state.Lock(state.Dir)
	if errors.Is(err, state.ErrBusy) {
		a.errs.Fail("another dovy is running", "unchanged", "wait for it to finish", "")
		return nil, exitFailed
	}
	if err != nil {
		a.errs.Fail("lock: "+err.Error(), "unchanged", "check "+state.Dir, "")
		return nil, exitFailed
	}
	return f, exitOK
}

// sudo asks for the password once and keeps the timestamp fresh until stop.
func (a *app) sudo() (stop func(), err error) {
	cmd := exec.Command("sudo", "-v")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				exec.Command("sudo", "-n", "-v").Run()
			}
		}
	}()
	return func() { close(done) }, nil
}
