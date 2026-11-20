package main

import (
	"crypto/md5"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/ui"
)

type pkgInfo struct {
	Version, Recipe, Capability, Content, Replaced string
	Size                                           int64 // bytes
}

// dpkgInstalled lists installed packages with dovy's field.
func dpkgInstalled() (map[string]pkgInfo, error) {
	out, err := exec.Command("dpkg-query", "-W", "-f", "${binary:Package}\t${Version}\t${Installed-Size}\t${X-Dovy-Recipe}\t${X-Dovy-Capability}\t${X-Dovy-Content}\t${X-Dovy-Replaced}\t${db:Status-Abbrev}\n").Output()
	if err != nil {
		return nil, fmt.Errorf("dpkg-query: %w", err)
	}
	m := map[string]pkgInfo{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 8 || !strings.HasPrefix(f[7], "ii") {
			continue
		}
		kib, _ := strconv.ParseInt(f[2], 10, 64)
		m[f[0]] = pkgInfo{f[1], f[3], f[4], f[5], f[6], kib * 1024}
	}
	return m, nil
}

// A recipe shipping several packages maps to the one named after it (ADR 0018).
func recipePackages(inst map[string]pkgInfo) map[string]string {
	m := map[string]string{}
	for p, i := range inst {
		if i.Recipe == "" {
			continue
		}
		if _, ok := m[i.Recipe]; !ok || p == i.Recipe || p == "dovy-"+i.Recipe {
			m[i.Recipe] = p
		}
	}
	return m
}

// dpkg adds the native architecture to Multi-Arch: same packages (mesa-va-drivers:amd64).
func installedAs(inst map[string]pkgInfo, p string) (pkgInfo, bool) {
	if i, ok := inst[p]; ok {
		return i, true
	}
	i, ok := inst[p+":amd64"]
	return i, ok
}

// dpkg records each conffile's md5; a deleted file is no loss.
func changedConfig(pkg string) bool {
	out, _ := exec.Command("dpkg-query", "-W", "-f", "${Conffiles}", pkg).Output()
	for _, c := range parseConffiles(string(out)) {
		b, err := os.ReadFile(c.path)
		if err == nil && fmt.Sprintf("%x", md5.Sum(b)) != c.md5 {
			return true
		}
	}
	return false
}

type conffile struct{ path, md5 string }

// parseConffiles reads dpkg's Conffiles field: " /etc/x <md5> [obsolete]".
func parseConffiles(s string) []conffile {
	var cs []conffile
	for _, l := range strings.Split(s, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && strings.HasPrefix(f[0], "/") && len(f[1]) == 32 {
			cs = append(cs, conffile{f[0], f[1]})
		}
	}
	return cs
}

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

// unused skips what was already unused before: never a system-wide autoremove.
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
	if contains(names, "dovy") {
		a.errs.Refuse("dovy", "dovy does not remove itself", "remove it with apt: sudo apt remove dovy")
		return exitRefused
	}
	if code := a.unfinished(); code != exitOK {
		return code
	}
	rs, code := a.load()
	if code != exitOK {
		return code
	}
	fmt.Fprintln(a.out.Out, a.out.Paint(ui.Green, "Catalog signature verified."))
	go manualPkgs() // ready by the time the plan needs it
	defer a.planning()()
	inst, err := dpkgInstalled()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return exitFailed
	}
	byRecipe := recipePackages(inst)
	release, err := deb.Release("/etc/os-release")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "dovy runs on Debian stable", "")
		return exitFailed
	}
	t := &txn{verb: "remove", names: names, release: release, versions: map[string]string{}, caps: map[string]bool{},
		removing: map[string]string{}, down: map[string]bool{}, inst: inst}
	for _, n := range names {
		p, ok := byRecipe[n]
		if !ok {
			a.errs.Fail(n+" is not installed", "unchanged", "dovy list --installed", "")
			return exitFailed
		}
		if r := rs[n]; r != nil && r.Repo != nil && !r.Repo.Pinned() {
			// NVIDIA: nothing comes back; nouveau takes over (ADR 0017).
			t.removing[p] = "purged"
			t.request = append(t.request, p+"-")
			t.leftover = "Without NVIDIA's driver the machine uses nouveau after a reboot. Debian's driver, if wanted: sudo apt install nvidia-driver"
			t.system = append(t.system, ui.Pair{Item: "graphics driver", Reason: "nouveau after a reboot; Debian's driver is not restored"})
			continue
		}
		t.restore(p, inst[p])
		if r := rs[n]; r != nil {
			t.expectWith(r, inst)
		}
		// The recipe's other packages go by name: apt never counts the newest
		// kernel as unused (ADR 0018).
		for q, i := range inst {
			if i.Recipe == n && q != p {
				t.removing[q] = "with " + n
				t.request = append(t.request, q+"-")
			}
		}
	}
	// A recipe still needed by another installed recipe stays.
	for r, p := range byRecipe {
		if _, gone := t.removing[p]; gone || rs[r] == nil {
			continue
		}
		for _, d := range rs[r].Depends {
			if _, ok := byRecipe[d]; ok && contains(names, d) {
				a.errs.Refuse(d, "needed by "+r, "")
				return exitRefused
			}
		}
	}
	// A repository other installed recipes still use is rebuilt without the
	// removed recipes' packages, from its installed key (ADR 0013).
	staying := map[string]bool{}
	for r := range byRecipe {
		if !contains(names, r) {
			staying[r] = true
		}
	}
	rebuilt := map[*catalog.Repository]bool{}
	for _, n := range names {
		if rs[n] == nil || rs[n].Repo == nil || rs[n].Repo.Repack || rebuilt[rs[n].Repo] {
			continue
		}
		q := rs[n].Repo
		rebuilt[q] = true
		pkgs := repoPackages(q, rs, staying)
		if len(pkgs) == 0 {
			continue // the last recipe using it: dovy-repo-<name> goes, unused
		}
		key, err := os.ReadFile(capability.Keyring(q.Name))
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "dovy install "+n+" again, which rebuilds its repository", "")
			return exitFailed
		}
		refusal, err := t.repository(q, pkgs, key, "", nil)
		if err == nil && refusal[0] != "" {
			err = fmt.Errorf("%s: %s", refusal[0], refusal[1])
		}
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "dovy check", "")
			return exitFailed
		}
	}

	tmp, err := os.MkdirTemp("", "dovy-")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer os.RemoveAll(tmp)
	track(tmp)
	t.tmp = tmp

	// Backports packages that stay return to stable; those left unused go with
	// the recipe.
	out, err := apt.Simulate(apt.Unused, t.args(nil)...)
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

	t.keepFor = func(gone map[string]bool) []ui.Pair { return a.kept(names, rs, inst, gone) }
	var notes []string
	for _, n := range names {
		if r := rs[n]; r != nil && r.UserNotes != "" {
			notes = append(notes, n)
		}
	}
	if len(notes) > 0 {
		t.leftover = "Per-user steps done for " + strings.Join(notes, ", ") + " are not undone; they do nothing now."
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

// A replaced package with another name is reinstalled; the same name returns
// to Debian's version, with its mark (ADR 0016).
func (t *txn) restore(p string, i pkgInfo) {
	back := map[string]string{}
	for _, x := range splitList(i.Replaced) {
		if f := strings.Fields(x); len(f) == 2 {
			back[f[0]] = f[1]
		}
	}
	if t.restored == nil {
		t.restored = map[string]string{}
	}
	if mark, ok := back[p]; ok {
		if v := apt.Stable(nil, []string{p})[p]; v != "" {
			t.down[p] = true
			t.restored[p] = mark
			t.request = append(t.request, p+"="+v)
		}
		delete(back, p)
	}
	if !t.down[p] {
		t.removing[p] = "purged"
		t.request = append(t.request, p+"-")
	}
	for _, n := range sortedKeys(back) {
		t.restored[n] = back[n]
		t.request = append(t.request, n)
	}
}

func sortedKeys(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func (a *app) kept(names []string, rs map[string]*catalog.Recipe, inst map[string]pkgInfo, removing map[string]bool) []ui.Pair {
	manual := manualPkgs()
	var kept []ui.Pair
	seen := map[string]bool{}
	for _, n := range names {
		r := rs[n]
		if r == nil {
			continue
		}
		for _, m := range r.Members { // a bundle's members are recipes of their own
			if o := rs[m]; o != nil && !contains(names, m) {
				if _, ok := inst[o.Package()]; ok {
					kept = append(kept, ui.Pair{Item: m, Reason: "member of " + n + "; remove it on its own"})
				}
			}
		}
		for _, d := range r.Depends { // came with it, a recipe of its own
			if o := rs[d]; o != nil && !contains(names, d) {
				if _, ok := inst[o.Package()]; ok {
					kept = append(kept, ui.Pair{Item: d, Reason: "a recipe of its own; remove it on its own"})
				}
			}
		}
		for _, p := range r.Packages {
			if removing[p] || seen[p] {
				continue
			}
			seen[p] = true
			if _, ok := installedAs(inst, p); !ok {
				continue
			}
			reason := "needed by another package"
			if manual[p] {
				reason = "installed by the user"
			}
			kept = append(kept, ui.Pair{Item: p, Reason: reason})
		}
	}
	var going []string
	for p := range removing {
		going = append(going, p)
	}
	sort.Strings(going)
	for _, p := range going {
		if i, _ := installedAs(inst, p); i.Recipe == "" && i.Capability == "" && changedConfig(p) {
			kept = append(kept, ui.Pair{Item: p, Reason: "removed; the config you changed stays"})
		}
	}
	return kept
}
