package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/log"
	"github.com/getdovy/dovy/internal/repo"
	"github.com/getdovy/dovy/internal/state"
	"github.com/getdovy/dovy/internal/ui"
)

// pkgInfo is an installed package as dpkg knows it.
type pkgInfo struct {
	Version, Recipe string
	Size            int64 // bytes
}

// dpkgInstalled lists installed packages with dovy's field (3.3).
func dpkgInstalled() (map[string]pkgInfo, error) {
	out, err := exec.Command("dpkg-query", "-W", "-f", "${Package}\t${Version}\t${Installed-Size}\t${X-Dovy-Recipe}\t${db:Status-Abbrev}\n").Output()
	if err != nil {
		return nil, fmt.Errorf("dpkg-query: %w", err)
	}
	m := map[string]pkgInfo{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 5 || !strings.HasPrefix(f[4], "ii") {
			continue
		}
		kib, _ := strconv.ParseInt(f[2], 10, 64)
		m[f[0]] = pkgInfo{f[1], f[3], kib * 1024}
	}
	return m, nil
}

// leavesConfig reports whether removing pkg leaves config behind (4.4):
// dpkg keeps conffiles, and keeps the package for its postrm purge step.
func leavesConfig(pkg string) bool {
	for _, n := range []string{pkg, pkg + ":amd64", pkg + ":i386"} {
		if fi, err := os.Stat("/var/lib/dpkg/info/" + n + ".conffiles"); err == nil && fi.Size() > 0 {
			return true
		}
		if _, err := os.Stat("/var/lib/dpkg/info/" + n + ".postrm"); err == nil {
			return true
		}
	}
	return false
}

// configOnly reports whether pkg is removed with its config left ("rc").
func configOnly(pkg string) bool {
	out, _ := exec.Command("dpkg-query", "-W", "-f", "${db:Status-Abbrev}", pkg).Output()
	return strings.HasPrefix(string(out), "rc")
}

// unused is what removing pkgs leaves unused, apart from what apt already
// considered unused before: never a system-wide autoremove (4.1).
func unused(before, after []string, pkgs map[string]bool) []string {
	was := map[string]bool{}
	for _, n := range before {
		was[n] = true
	}
	var out []string
	for _, n := range after {
		if !was[n] && !pkgs[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (a *app) remove(names []string) int {
	if code := a.unfinished(); code != exitOK {
		return code
	}
	rs, code := a.load()
	if code != exitOK {
		return code
	}
	fmt.Fprintln(a.out.Out, a.out.Paint(ui.Green, "Catalog signature verified."))
	inst, err := dpkgInstalled()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return exitFailed
	}
	byRecipe := map[string]string{} // recipe → package
	for p, i := range inst {
		if i.Recipe != "" {
			byRecipe[i.Recipe] = p
		}
	}
	pkgs := map[string]bool{}
	var dovy []string
	for _, n := range names {
		p, ok := byRecipe[n]
		if !ok {
			a.errs.Fail(n+" is not installed", "unchanged", "dovy list --installed", "")
			return exitFailed
		}
		pkgs[p] = true
		dovy = append(dovy, p)
	}
	sort.Strings(dovy)
	// A recipe still needed by another installed recipe stays (4.8).
	for r, p := range byRecipe {
		if pkgs[p] || rs[r] == nil {
			continue
		}
		for _, d := range rs[r].Depends {
			if p2, ok := byRecipe[d]; ok && pkgs[p2] {
				a.errs.Refuse(d, "needed by "+r, "")
				return exitRefused
			}
		}
	}

	// Resolve: what removing dovy's packages leaves unused.
	base, err := apt.Simulate("autoremove")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that apt works: sudo apt-get check", "")
		return exitFailed
	}
	var before []string
	for _, x := range apt.Parse(base) {
		before = append(before, x.Name)
	}
	out, err := apt.Simulate(append([]string{"remove"}, dovy...)...)
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that apt works: sudo apt-get check", "")
		return exitFailed
	}
	deps := unused(before, apt.NoLongerRequired(out), pkgs)
	all := append(append([]string{}, dovy...), deps...)

	// apt's own plan for the transaction must match exactly (4.3).
	out, err = apt.Simulate(append([]string{"remove"}, all...)...)
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that apt works: sudo apt-get check", "")
		return exitFailed
	}
	want := map[string]bool{}
	for _, n := range all {
		want[n] = true
	}
	var extra []string
	for _, x := range apt.Parse(out) {
		if x.Op == "Inst" || !want[x.Name] {
			extra = append(extra, x.Op+" "+x.Name)
		}
	}
	if len(extra) > 0 {
		a.errs.Refuse(strings.Join(names, " "), "apt would change packages not in the plan", strings.Join(extra, ", "))
		return exitRefused
	}

	// Plan (12.1, 12.2).
	var freed int64
	row := func(p, note string) ui.Row {
		i := inst[p]
		freed += i.Size
		src := "Debian"
		if i.Recipe != "" {
			src = "dovy"
		}
		return ui.Row{Name: p, Version: i.Version, Source: src, Size: ui.Bytes(i.Size), Note: note}
	}
	var rm, rmDeps []ui.Row
	for _, p := range dovy {
		rm = append(rm, row(p, "purged"))
	}
	configs := 0
	for _, p := range deps {
		rmDeps = append(rmDeps, row(p, ""))
		if leavesConfig(p) {
			configs++
		}
	}
	secs := []ui.Section{{Color: ui.Red, Title: "Removing", Rows: rm}}
	counts := []ui.Count{{Verb: "Remove", N: len(rm), Unit: plural(len(rm), "package")}}
	if len(rmDeps) > 0 {
		secs = append(secs, ui.Section{Color: ui.Red, Title: "Removing unused dependencies", Rows: rmDeps})
		counts = append(counts, ui.Count{Verb: "Dependencies", N: len(rmDeps), Unit: plural(len(rmDeps), "package")})
	}
	kept := a.kept(names, rs, inst, want)
	if configs > 0 {
		kept = append(kept, ui.Pair{Item: fmt.Sprintf("config of %d %s", configs, plural(configs, "package")), Reason: "in /etc; apt purge removes it"})
	}
	a.out.Blank()
	a.out.Plan(secs)
	if len(kept) > 0 {
		a.out.Pairs(ui.Cyan, "Kept", kept)
	}
	a.out.Summary(counts, []ui.Pair{{Item: "Disk freed", Reason: ui.Bytes(freed)}})
	a.out.Blank()
	if !a.out.Confirm(a.in) {
		fmt.Fprintln(a.out.Out, "Cancelled. Nothing changed.")
		return exitCancelled
	}

	stop, err := a.sudo()
	if err != nil {
		a.errs.Fail("sudo: "+err.Error(), "unchanged", "dovy remove again", "")
		return exitFailed
	}
	defer stop()
	lock, code := a.lock()
	if code != exitOK {
		return code
	}
	defer lock.Close()
	tmp, err := os.MkdirTemp("", "dovy-")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer os.RemoveAll(tmp)
	logf, err := log.Open(tmp, time.Now())
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer logf.Close()
	logPath := filepath.Join(log.Dir, filepath.Base(logf.Name()))
	defer func() {
		logf.Sync()
		a.root("install", "-D", "-m", "644", logf.Name(), logPath)
	}()

	// Journal, one apt transaction, then dovy's config files (4.4).
	a.out.Rule("", "Remove")
	j := &state.Journal{Command: "remove", Recipes: names, Started: time.Now().UTC()}
	for _, p := range all {
		j.Packages = append(j.Packages, state.Package{Name: p, Version: inst[p].Version})
	}
	jp, err := j.Encode(tmp)
	if err == nil {
		err = a.root("install", "-m", "644", jp, state.JournalPath(state.Dir))
	}
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "dovy remove again", logPath)
		return exitFailed
	}
	versions := map[string]string{}
	for _, p := range all {
		versions[p] = inst[p].Version
	}
	if err := apt.Run(logf, a.aptLine(versions), append(append([]string{"remove", "-y"}, apt.Options...), all...)...); err != nil {
		a.errs.Fail(err.Error(), "unknown: the operation stopped during apt", "sudo dpkg --configure -a", logPath)
		return exitFailed
	}
	for _, p := range dovy {
		if !configOnly(p) {
			continue
		}
		fmt.Fprintln(logf, "dpkg --purge "+p)
		cmd := exec.Command("sudo", "--", "dpkg", "--purge", p)
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Run(); err != nil {
			a.errs.Fail(err.Error(), "removed; config of "+p+" left", "sudo dpkg --purge "+p, logPath)
			return exitFailed
		}
		a.out.Say(ui.Red, "Purging:", p+" ("+inst[p].Version+")")
	}
	a.root("rm", "-f", state.JournalPath(state.Dir))
	if err := repo.Update(repo.Dir, nil, dovy, a.root); err != nil {
		a.out.Say(ui.Yellow, "Warning:", "local repository: "+err.Error())
	}
	if cache, err := build.CacheDir(); err == nil {
		for _, n := range names {
			if r := rs[n]; r != nil {
				for _, s := range r.Sources {
					os.Remove(filepath.Join(cache, "sources", s.SHA256))
				}
			}
		}
	}
	a.out.Blank()

	var notes []string
	for _, n := range names {
		if r := rs[n]; r != nil && r.UserNotes != "" {
			notes = append(notes, n)
		}
	}
	if len(notes) > 0 {
		a.out.Rule(ui.Yellow, "Notices")
		fmt.Fprintln(a.out.Out, "  Per-user lines added for "+strings.Join(notes, ", ")+" stay in ~/.bashrc; they do nothing now.")
		a.out.Blank()
	}
	fmt.Fprintln(a.out.Out, a.out.Paint(ui.Green, "Done.")+" "+strings.Join(names, ", ")+" removed.")
	return exitOK
}

// kept lists the recipes' Debian packages that stay, and why (12.2).
func (a *app) kept(names []string, rs map[string]*catalog.Recipe, inst map[string]pkgInfo, removing map[string]bool) []ui.Pair {
	manual := map[string]bool{}
	if out, err := exec.Command("apt-mark", "showmanual").Output(); err == nil {
		for _, n := range strings.Fields(string(out)) {
			manual[n] = true
		}
	}
	var kept []ui.Pair
	seen := map[string]bool{}
	for _, n := range names {
		r := rs[n]
		if r == nil {
			continue
		}
		for _, p := range r.Packages {
			if removing[p] || seen[p] {
				continue
			}
			seen[p] = true
			if _, ok := inst[p]; !ok {
				continue
			}
			reason := "needed by another package"
			if manual[p] {
				reason = "installed by the user"
			}
			kept = append(kept, ui.Pair{Item: p, Reason: reason})
		}
	}
	return kept
}
