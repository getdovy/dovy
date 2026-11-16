package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/ui"
)

// upgrade is one plan for everything (topic 7): the catalog, dovy's
// packages, and Debian's dist-upgrade with its removals shown.
func (a *app) upgrade() int {
	if code := a.unfinished(); code != exitOK {
		return code
	}
	if code := a.update(); code != exitOK {
		return code
	}
	rs, code := a.load()
	if code != exitOK {
		return code
	}
	defer a.planning()()
	release, err := deb.Release("/etc/os-release")
	codename, cerr := capability.Codename("/etc/os-release")
	if err == nil {
		err = cerr
	}
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "dovy runs on Debian stable", "")
		return exitFailed
	}
	inst, err := dpkgInstalled()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return exitFailed
	}
	byRecipe := recipePackages(inst)
	t := &txn{verb: "upgrade", release: release, versions: map[string]string{}, caps: map[string]bool{},
		removing: map[string]string{}, down: map[string]bool{}, inst: inst}
	tmp, err := os.MkdirTemp("", "dovy-")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer os.RemoveAll(tmp)
	track(tmp)
	t.tmp = tmp
	cache, err := build.CacheDir()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "set HOME or XDG_CACHE_HOME", "")
		return exitFailed
	}

	// Installed recipes: rebuilt when their version changes (7.4, 7.8);
	// what cannot be upgraded is said, with what to do (7.5, 7.6, 6.7).
	needs := map[string][]string{}
	using := map[string]bool{}
	add := func(r *catalog.Recipe) {
		x := capability.Expand(r, rs, codename)
		t.replace(x, byRecipe, inst)
		t.names = append(t.names, r.Name)
		t.build = append(t.build, x)
		t.versions[r.Package()] = deb.Version(r, release)
	}
	// dovy first, alone (9.4): the new dovy plans the rest after a restart.
	if r := rs["dovy"]; r != nil && byRecipe["dovy"] != "" && supports(r, release) &&
		apt.Compare(deb.Version(r, release), inst[byRecipe["dovy"]].Version) > 0 {
		if os.Getenv(Restarted) != "" { // upgraded and restarted, yet still old: never loop
			a.errs.Fail("dovy is still "+inst[byRecipe["dovy"]].Version+" after upgrading itself to "+deb.Version(r, release),
				"unchanged", "install the newest dovy from https://github.com/getdovy/dovy/releases", "")
			return exitFailed
		}
		add(r)
		t.notices = append(t.notices, ui.Pair{Item: "dovy", Reason: "upgraded first, alone; it then plans the rest of this upgrade"})
		if code := a.run(t); code != exitOK {
			return code
		}
		return exitRestart
	}
	for _, name := range sortedKeys(byRecipe) {
		r := rs[name]
		switch {
		case r == nil:
			t.notices = append(t.notices, ui.Pair{Item: name, Reason: "dropped from the catalog: dovy remove " + name})
			continue
		case !supports(r, release):
			t.notices = append(t.notices, ui.Pair{Item: name, Reason: fmt.Sprintf("not built for Debian %d; kept as is until the recipe supports it, or: dovy remove %s", release, name)})
			continue
		}
		if why := unavailable(r, rs); why != "" {
			t.notices = append(t.notices, ui.Pair{Item: name, Reason: why + ": dovy remove " + name + " (and recipes that depend on it)"})
			continue
		}
		if r.Successor != "" {
			t.notices = append(t.notices, ui.Pair{Item: name, Reason: "succeeded by " + r.Successor + ": dovy install " + r.Successor + ", then: dovy remove " + name})
		}
		using[name] = true
		for _, c := range r.Needs() {
			needs[c] = append(needs[c], name)
		}
		if r.Repo != nil && r.Repo.Repack { // newer vendor version (ADR 0014)
			d, v, refusal, err := t.repacked(r, cache)
			if err != nil || refusal[0] != "" {
				t.notices = append(t.notices, ui.Pair{Item: name, Reason: "vendor check failed, kept as is: " + or(refusal[0], fmt.Sprint(err))})
				continue
			}
			if apt.Compare(v, inst[byRecipe[name]].Version) > 0 {
				add(r)
				t.versions[r.Package()] = v
				if t.prebuilt == nil {
					t.prebuilt = map[string]string{}
				}
				t.prebuilt[r.Name] = d
			}
			continue
		}
		if name == "dovy" && apt.Compare(inst[byRecipe[name]].Version, deb.Version(r, release)) > 0 {
			continue // a dovy newer than its recipe (installed from the release page) stays
		}
		if inst[byRecipe[name]].Version != deb.Version(r, release) {
			add(r)
		}
		// A bundle's new members and a recipe's new dependencies come with
		// it (6.8), and theirs in turn; pairs stand.
		next := append(append([]string{}, r.Members...), r.Depends...)
		for len(next) > 0 {
			m := next[0]
			next = next[1:]
			o := rs[m]
			if o == nil || byRecipe[m] != "" || contains(t.names, m) {
				continue
			}
			paired := false
			for _, c := range o.Conflicts {
				paired = paired || byRecipe[c] != ""
			}
			if !paired && supports(o, release) && unavailable(o, rs) == "" {
				add(o)
				for _, c := range o.Needs() {
					needs[c] = append(needs[c], m)
				}
				next = append(next, o.Depends...)
			} else if contains(r.Depends, m) {
				t.notices = append(t.notices, ui.Pair{Item: name, Reason: "now needs " + m + ", which cannot be installed here: dovy show " + m})
			}
		}
	}
	for _, x := range t.build {
		if build.Sourced(x) {
			build.Refresh = true // the sandbox's base is remade (ADR 0015)
		}
	}

	// Capabilities and repositories: content and keys checked again
	// (2.4, 8.5); a change rebuilds them.
	sim := newSimConfig()
	if err := t.capabilities(needs, codename, sim); err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /etc/apt and the network, then: dovy upgrade", "")
		return exitFailed
	}
	users := map[*catalog.Repository][]string{}
	for name := range using {
		if r := rs[name]; r.Repo != nil && !r.Repo.Repack {
			users[r.Repo] = append(users[r.Repo], name)
		}
	}
	for q, who := range users {
		sort.Strings(who)
		refusal, err := t.repository(q, repoPackages(q, rs, using), nil, strings.Join(who, ", "), sim)
		if err == nil && refusal[0] != "" {
			err = fmt.Errorf("%s: %s", refusal[0], refusal[1])
		}
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check the network, then: dovy upgrade", "")
			return exitFailed
		}
	}
	if !sim.empty() {
		if err := t.withConfig(sim); err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check /etc/apt and the network, then: dovy upgrade", "")
			return exitFailed
		}
	}

	// Debian's dist-upgrade, as requests in the one transaction (7.2).
	if code := t.distUpgrade(a); code != exitOK {
		return code
	}
	if len(t.build) == 0 && len(t.request) == 0 {
		fmt.Fprintln(a.out.Out, "Everything is up to date.")
		a.showNotices(t.notices)
		return exitOK
	}
	return a.run(t)
}

// distUpgrade adds apt's dist-upgrade to t: upgrades and new packages as
// pkg=version, removals as pkg- with their reason. It never removes dovy's
// own packages.
func (t *txn) distUpgrade(a *app) int {
	out, err := apt.Simulate(t.opts, "dist-upgrade")
	if err != nil {
		a.errs.Refuse("upgrade", "apt cannot upgrade", err.Error())
		return exitRefused
	}
	reqs, removing, dovy := upgradeRequests(apt.Parse(out), t.versions, t.inst)
	if len(dovy) > 0 {
		a.errs.Refuse("upgrade", "Debian's upgrade would remove dovy's packages", strings.Join(dovy, ", "))
		return exitRefused
	}
	t.request = append(t.request, reqs...)
	for p, why := range removing {
		t.removing[p] = why
	}
	return exitOK
}

// upgradeRequests turns a dist-upgrade simulation into install arguments.
func upgradeRequests(as []apt.Action, ours map[string]string, inst map[string]pkgInfo) (reqs []string, removing map[string]string, dovy []string) {
	removing = map[string]string{}
	for _, x := range as {
		if ours[x.Name] != "" {
			continue
		}
		switch x.Op {
		case "Inst":
			reqs = append(reqs, x.Name+"="+x.Version)
		case "Remv", "Purg":
			if i := inst[x.Name]; i.Recipe != "" || i.Capability != "" {
				dovy = append(dovy, x.Name)
				continue
			}
			reqs = append(reqs, x.Name+"-")
			removing[x.Name] = "removed by Debian's upgrade"
		}
	}
	return reqs, removing, dovy
}

// showNotices prints what needs the user after a run (12.4).
func (a *app) showNotices(n []ui.Pair) {
	if len(n) == 0 {
		return
	}
	a.out.Blank()
	a.out.Pairs(ui.Yellow, "Notices", n)
}
