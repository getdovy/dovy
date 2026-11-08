package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/ui"
)

// pkgInfo is an installed package as dpkg knows it.
type pkgInfo struct {
	Version, Recipe, Capability string
	Size                        int64 // bytes
}

// dpkgInstalled lists installed packages with dovy's field (3.3).
func dpkgInstalled() (map[string]pkgInfo, error) {
	out, err := exec.Command("dpkg-query", "-W", "-f", "${Package}\t${Version}\t${Installed-Size}\t${X-Dovy-Recipe}\t${X-Dovy-Capability}\t${db:Status-Abbrev}\n").Output()
	if err != nil {
		return nil, fmt.Errorf("dpkg-query: %w", err)
	}
	m := map[string]pkgInfo{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 6 || !strings.HasPrefix(f[5], "ii") {
			continue
		}
		kib, _ := strconv.ParseInt(f[2], 10, 64)
		m[f[0]] = pkgInfo{f[1], f[3], f[4], kib * 1024}
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
	t := &txn{verb: "remove", names: names, versions: map[string]string{}, removing: map[string]string{},
		down: map[string]bool{}, inst: inst}
	for _, n := range names {
		p, ok := byRecipe[n]
		if !ok {
			a.errs.Fail(n+" is not installed", "unchanged", "dovy list --installed", "")
			return exitFailed
		}
		t.removing[p] = "purged"
		t.request = append(t.request, p+"-")
	}
	// A recipe still needed by another installed recipe stays (4.8).
	for r, p := range byRecipe {
		if _, gone := t.removing[p]; gone || rs[r] == nil {
			continue
		}
		for _, d := range rs[r].Depends {
			if p2, ok := byRecipe[d]; ok && t.removing[p2] != "" {
				a.errs.Refuse(d, "needed by "+r, "")
				return exitRefused
			}
		}
	}
	tmp, err := os.MkdirTemp("", "dovy-")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer os.RemoveAll(tmp)
	t.tmp = tmp

	// Backports packages that stay installed return to stable (4.6); those
	// left unused go with the recipe (4.1).
	out, err := apt.Simulate(nil, t.args(nil)...)
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that apt works: sudo apt-get check", "")
		return exitFailed
	}
	leaving := map[string]bool{}
	for _, n := range apt.NoLongerRequired(out) {
		leaving[n] = true
	}
	var others []string // packages of backports recipes that stay
	for r, p := range byRecipe {
		if _, gone := t.removing[p]; !gone && rs[r] != nil && rs[r].Backports {
			others = append(others, rs[r].Packages...)
		}
	}
	for _, n := range names {
		if r := rs[n]; r != nil && r.Backports {
			t.leaveBackports(r.Packages, others, inst, leaving)
		}
	}

	removing := map[string]bool{}
	for p := range t.removing {
		removing[p] = true
	}
	t.kept = a.kept(names, rs, inst, removing)
	var notes []string
	for _, n := range names {
		if r := rs[n]; r != nil && r.UserNotes != "" {
			notes = append(notes, n)
		}
	}
	if len(notes) > 0 {
		t.leftover = "Per-user lines added for " + strings.Join(notes, ", ") + " stay in ~/.bashrc; they do nothing now."
	}
	code = a.run(t)
	if code == exitOK {
		if cache, err := build.CacheDir(); err == nil {
			for _, n := range names {
				if r := rs[n]; r != nil {
					for _, s := range r.Sources {
						os.Remove(filepath.Join(cache, "sources", s.SHA256))
					}
				}
			}
		}
	}
	return code
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
