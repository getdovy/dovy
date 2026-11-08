package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
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

// install plans recipes and their depends, with capabilities, pins and
// pairs (principles 1.4, 5).
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
	if err == nil {
		_, err = capability.Codename("/etc/os-release")
	}
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "dovy runs on Debian stable", "")
		return exitFailed
	}
	codename, _ := capability.Codename("/etc/os-release")
	inst, err := dpkgInstalled()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return exitFailed
	}
	byRecipe := map[string]string{}
	for p, i := range inst {
		if i.Recipe != "" {
			byRecipe[i.Recipe] = p
		}
	}
	t := &txn{verb: "install", release: release, versions: map[string]string{}, caps: map[string]bool{},
		removing: map[string]string{}, down: map[string]bool{}, inst: inst}
	needs := map[string][]string{} // capability → recipes
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
		if p, ok := byRecipe[r.Name]; ok && inst[p].Version == v {
			continue
		}
		t.names = append(t.names, r.Name)
		t.build = append(t.build, capability.Expand(r, rs, codename))
		t.versions[r.Package()] = v
		if r.UserNotes != "" {
			t.notes = append(t.notes, r)
		}
		for _, c := range r.Needs() {
			needs[c] = append(needs[c], r.Name)
		}
		if r.Backports {
			for _, p := range r.Packages {
				t.request = append(t.request, p+"/"+codename+"-backports")
			}
		}
	}
	if len(t.build) == 0 {
		fmt.Fprintln(a.out.Out, "Nothing to do: "+strings.Join(names, ", ")+" already installed.")
		return exitOK
	}
	tmp, err := os.MkdirTemp("", "dovy-")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer os.RemoveAll(tmp)
	t.tmp = tmp

	// Capabilities, built from the system's sources (5.1–5.4), and vendor
	// repositories, keys checked now (2.4, 5.10).
	t.inst = inst
	sim := newSimConfig()
	if err := t.capabilities(needs, codename, sim); err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /etc/apt and the network, then: dovy install "+strings.Join(names, " "), "")
		return exitFailed
	}
	using := map[string]bool{} // recipes installed or coming
	for r := range byRecipe {
		using[r] = true
	}
	users := map[*catalog.Repository][]string{}
	for _, r := range order {
		if r.Repo != nil && !r.Repo.Repack && contains(t.names, r.Name) {
			using[r.Name] = true
			users[r.Repo] = append(users[r.Repo], r.Name)
		}
	}
	// Repacked repositories (ADR 0014): the package is downloaded and
	// repacked now, and planned as itself.
	for _, r := range order {
		if r.Repo == nil || !r.Repo.Repack || !contains(t.names, r.Name) {
			continue
		}
		cache, err := build.CacheDir()
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "set HOME or XDG_CACHE_HOME", "")
			return exitFailed
		}
		d, v, refusal, err := t.repacked(r, cache)
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check the network, then: dovy install "+strings.Join(names, " "), "")
			return exitFailed
		}
		if refusal[0] != "" {
			a.errs.Refuse(r.Name, refusal[0], refusal[1])
			return exitRefused
		}
		if t.prebuilt == nil {
			t.prebuilt = map[string]string{}
		}
		t.prebuilt[r.Name] = d
		t.versions[r.Package()] = v
		t.system = append(t.system, ui.Pair{Item: capability.Host(r.Repo.URI) + ", repacked", Reason: r.Name})
	}
	for q, who := range users {
		refusal, err := t.repository(q, repoPackages(q, rs, using), nil, strings.Join(who, ", "), sim)
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check the network, then: dovy install "+strings.Join(names, " "), "")
			return exitFailed
		}
		if refusal[0] != "" {
			a.errs.Refuse(strings.Join(who, " "), refusal[0], refusal[1])
			return exitRefused
		}
	}
	if !sim.empty() {
		if err := t.withConfig(sim); err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check /etc/apt and the network, then: dovy install "+strings.Join(names, " "), "")
			return exitFailed
		}
	}

	// Pairs: the other half goes, and its backports packages return to
	// stable unless this half keeps them on backports (1.4, 4.6).
	for _, x := range t.build {
		r := rs[x.Name]
		if r == nil {
			continue
		}
		for _, c := range r.Conflicts {
			p, ok := byRecipe[c]
			if !ok {
				continue
			}
			t.removing[p] = "replaced by " + r.Name
			t.request = append(t.request, p+"-")
			if o := rs[c]; o != nil && o.Backports && !r.Backports {
				t.leaveBackports(o.Packages, nil, inst, nil)
			}
		}
	}
	return a.run(t)
}

// leaveBackports undoes a backports recipe (4.6): its packages, and the
// automatic backports packages they brought, return to stable; those with no
// stable version (a versioned kernel) or nothing left using them are
// removed. Packages that other backports recipes still use are skipped.
func (t *txn) leaveBackports(pinned, others []string, inst map[string]pkgInfo, leaving map[string]bool) {
	manual := map[string]bool{}
	if out, err := exec.Command("apt-mark", "showmanual").Output(); err == nil {
		for _, n := range strings.Fields(string(out)) {
			manual[n] = true
		}
	}
	keep := map[string]bool{}
	for _, p := range installedDeps(others) {
		keep[p] = true
	}
	var pkgs []string
	for _, p := range installedDeps(pinned) {
		i, ok := inst[p]
		if !ok || keep[p] || !strings.Contains(i.Version, "~bpo") {
			continue
		}
		if leaving[p] { // unused once the recipe goes: removed, named
			t.removing[p] = "from backports"
			t.request = append(t.request, p+"-")
			continue
		}
		if manual[p] && !contains(pinned, p) {
			continue // the user's own backports package
		}
		pkgs = append(pkgs, p)
	}
	stable := apt.Stable(t.opts, pkgs)
	for _, p := range pkgs {
		if v := stable[p]; v != "" {
			t.down[p] = true
			t.request = append(t.request, p+"="+v)
		} else {
			t.removing[p] = "from backports"
			t.request = append(t.request, p+"-")
		}
	}
}

// installedDeps is pkgs and everything installed they depend on.
func installedDeps(pkgs []string) []string {
	if len(pkgs) == 0 {
		return nil
	}
	args := append([]string{"depends", "--recurse", "--installed", "--no-recommends", "--no-suggests",
		"--no-conflicts", "--no-breaks", "--no-replaces", "--no-enhances"}, pkgs...)
	out, _ := exec.Command("apt-cache", args...).Output()
	seen := map[string]bool{}
	var deps []string
	for _, l := range strings.Split(string(out), "\n") {
		if l == "" || l[0] == ' ' || l[0] == '<' || seen[l] {
			continue
		}
		seen[l] = true
		deps = append(deps, l)
	}
	return deps
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func plural(n int, s string) string {
	if n == 1 {
		return s
	}
	return s + "s"
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
func (a *app) aptLine(versions map[string]string, warnings *[]string) func(string) {
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
		if strings.HasPrefix(l, "debconf: ") && warnings != nil {
			*warnings = append(*warnings, l) // a question answered with its default (8.11)
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
