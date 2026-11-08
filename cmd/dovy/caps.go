package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/fetch"
	"github.com/getdovy/dovy/internal/pgp"
	"github.com/getdovy/dovy/internal/ui"
)

// foreign reports whether dpkg already has the i386 architecture.
func foreign() bool {
	out, _ := exec.Command("dpkg", "--print-foreign-architectures").Output()
	return strings.Contains(" "+strings.Join(strings.Fields(string(out)), " ")+" ", " i386 ")
}

// simConfig is what the simulation's temporary apt configuration adds:
// sources and pins ("" removes one), keyrings, and i386.
type simConfig struct {
	sources, prefs, keys map[string]string
	i386                 bool
}

func newSimConfig() *simConfig {
	return &simConfig{sources: map[string]string{}, prefs: map[string]string{}, keys: map[string]string{}}
}

func (c *simConfig) empty() bool {
	return len(c.sources) == 0 && len(c.prefs) == 0 && !c.i386
}

// planCap adds a capability or repository package to build and install
// first, unless the installed one ships the same (5.1). It reports whether
// it was added.
func (t *txn) planCap(r *catalog.Recipe) (added, updated bool) {
	pkg := r.Package()
	i, installed := t.inst[pkg]
	if installed && i.Content == deb.ContentHash(r) {
		return false, false
	}
	if installed { // a new version, never reused (ADR 0010)
		n, _ := strconv.Atoi(strings.SplitN(i.Version, "-", 2)[0])
		r.Version = strconv.Itoa(n + 1)
	}
	t.build = append([]*catalog.Recipe{r}, t.build...)
	t.versions[pkg] = deb.Version(r, t.release)
	t.caps[pkg] = true
	return true, installed
}

// capabilities plans what t's recipes need (5.1–5.4): new capabilities, and
// installed ones whose content changed, built and installed first.
// needs maps a capability to the recipes asking for it.
func (t *txn) capabilities(needs map[string][]string, codename string, sim *simConfig) error {
	srcs := capability.System("/etc/apt")
	compText, added := capability.ComponentsSource(srcs, codename)
	_, compInstalled := t.inst[capability.Components]
	var extra []string
	if needs["components"] != nil || compInstalled {
		extra = added
	}
	for _, name := range []string{"components", "backports", "i386"} {
		if _, installed := t.inst[capability.Package(name)]; needs[name] == nil && !installed {
			continue
		}
		var r *catalog.Recipe
		switch name {
		case "components":
			r = capability.ComponentsRecipe(compText)
		case "backports":
			text, _, err := capability.BackportsSource(srcs, codename, extra)
			if err != nil {
				if needs[name] == nil {
					continue
				}
				return err
			}
			r = capability.BackportsRecipe(text)
		case "i386":
			r = capability.I386Recipe()
		}
		ok, updated := t.planCap(r)
		if !ok {
			continue
		}
		if name == "i386" {
			sim.i386 = !foreign()
		} else {
			file := "dovy-" + name + ".sources"
			sim.sources[file] = ""
			for _, f := range r.Files {
				sim.sources[file] = f.Content
			}
		}
		reason := strings.Join(needs[name], ", ")
		if updated {
			reason = "updated"
		}
		switch {
		case name == "i386":
			t.system = append(t.system, ui.Pair{Item: "i386 architecture", Reason: reason})
		case name == "components" && len(added) > 0:
			t.system = append(t.system, ui.Pair{Item: strings.Join(added, ", "), Reason: reason})
		case name == "backports":
			t.system = append(t.system, ui.Pair{Item: "Debian backports", Reason: reason})
		}
	}
	return nil
}

// repository plans a vendor-repository recipe's package dovy-repo-<name>
// (2.4, 5.10): the key fetched now and checked by fingerprint, no other
// source for the host, then source, key and pin for the simulation.
// refusal is set when the recipe cannot go ahead.
func (t *txn) repository(r *catalog.Recipe, sim *simConfig) (refusal [2]string, err error) {
	q := r.Repository
	host := capability.Host(q.URI)
	if s := capability.UsedBy(capability.System("/etc/apt"), host); s != nil {
		return [2]string{host + " is already configured outside dovy", s.File + "; remove it, then: dovy install " + r.Name}, nil
	}
	b, err := fetch.Bytes(q.KeyURL, 1<<20)
	if err != nil {
		return refusal, err
	}
	key, err := pgp.Read(b)
	if err != nil {
		return [2]string{"vendor key unreadable", err.Error()}, nil
	}
	if want := strings.ToUpper(strings.ReplaceAll(q.Fingerprint, " ", "")); key.Fingerprint != want {
		return [2]string{"vendor key fingerprint differs from the recipe", key.Fingerprint}, nil
	}
	rr := capability.RepoRecipe(r, key.Binary)
	ok, updated := t.planCap(rr)
	if !ok {
		return refusal, nil
	}
	name := "dovy-" + r.Name
	sim.keys[name+".pgp"] = string(key.Binary)
	sim.sources[name+".sources"] = capability.RepoSource(q, "@KEYS@/"+name+".pgp")
	sim.prefs[name+".pref"] = capability.RepoPin(q)
	reason := r.Name
	if updated {
		reason = "updated"
	}
	t.system = append(t.system, ui.Pair{Item: host + " repository", Reason: reason})
	return refusal, nil
}

// leaveI386 plans i386's removal when dovy-i386 goes (4.8): dovy added i386
// and nothing of the user's needs it, so every i386 package is purged in a
// final step, then dovy-i386's postrm removes the architecture.
func (t *txn) leaveI386(stubs []string) error {
	if _, ok := t.removing[capability.I386]; !ok && !t.unused[capability.I386] {
		return nil
	}
	t.final = map[string]bool{capability.I386: true}
	if _, err := os.Stat(capability.I386Added); err != nil {
		return nil // i386 was there before dovy: it stays (3.6)
	}
	if out, err := exec.Command("apt-mark", "showmanual").Output(); err == nil {
		var own []string
		for _, n := range strings.Fields(string(out)) {
			if strings.HasSuffix(n, ":i386") {
				own = append(own, n)
			}
		}
		if len(own) > 0 {
			t.kept = append(t.kept, ui.Pair{Item: "i386 architecture", Reason: strings.Join(own, ", ") + " installed by the user"})
			return nil
		}
	}
	out, _ := exec.Command("dpkg-query", "-W", "-f", "${binary:Package}\t${db:Status-Abbrev}\n").Output()
	var i386 []string
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) == 2 && strings.HasSuffix(f[0], ":i386") {
			t.final[f[0]] = true
			if strings.HasPrefix(f[1], "ii") && !t.unused[f[0]] {
				if _, gone := t.removing[f[0]]; !gone {
					i386 = append(i386, f[0])
				}
			}
		}
	}
	if len(i386) == 0 {
		return nil
	}
	// Anything else apt would remove with them is the user's: i386 stays.
	trial := append(append([]string{}, t.request...), suffix(i386, "-")...)
	sim, err := apt.Simulate(t.opts, append([]string{"install", "--allow-downgrades"}, append(stubs, trial...)...)...)
	if err != nil {
		return err
	}
	var needed []string
	for _, x := range apt.Parse(sim) {
		if (x.Op == "Remv" || x.Op == "Purg") && !strings.HasSuffix(x.Name, ":i386") && !t.unused[x.Name] {
			if _, ok := t.removing[x.Name]; !ok {
				needed = append(needed, x.Name)
			}
		}
	}
	if len(needed) > 0 {
		for _, p := range i386 {
			delete(t.final, p)
		}
		t.kept = append(t.kept, ui.Pair{Item: "i386 architecture", Reason: "needed by " + strings.Join(needed, ", ")})
		return nil
	}
	for _, p := range i386 {
		t.removing[p] = "i386, purged"
	}
	t.request = trial
	return nil
}

func suffix(names []string, s string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n + s
	}
	return out
}

// pkgName strips an apt-get argument to the package: foo/suite, foo=1, foo-.
func pkgName(arg string) string {
	n := strings.FieldsFunc(arg, func(c rune) bool { return c == '/' || c == '=' })[0]
	return strings.TrimSuffix(n, "-")
}
