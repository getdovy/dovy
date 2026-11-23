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

// simConfig extends the simulation's temporary apt configuration; a "" source or pin removes one.
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

// planCap skips a package whose installed version ships the same content.
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

// Capabilities are built and installed first. needs maps a capability to the
// recipes asking for it.
func (t *txn) capabilities(needs map[string][]string, codename string, sim *simConfig) error {
	srcs := capability.System("/etc/apt")
	capability.Resolve(srcs, t.listsDir()) // sources naming stable count as the release's
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

// repoPackages is q's pin list: packages of the recipes using q that stay or
// come installed (ADR 0013).
func repoPackages(q *catalog.Repository, rs map[string]*catalog.Recipe, using map[string]bool) []string {
	set := map[string]bool{}
	for name := range using {
		if r := rs[name]; r != nil && r.Repo == q {
			for _, p := range r.Packages {
				set[p] = true
			}
		}
	}
	pkgs := make([]string, 0, len(set))
	for p := range set {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	return pkgs
}

// key, when given, comes from the installed keyring; otherwise it is fetched
// live. Either is checked by fingerprint (ADR 0013).
func (t *txn) repository(q *catalog.Repository, pkgs []string, key []byte, users string, sim *simConfig) (refusal [2]string, err error) {
	host := capability.Host(q.URI)
	if s := capability.UsedBy(capability.System("/etc/apt"), host); s != nil {
		return [2]string{host + " is already configured outside dovy", s.File + "; remove it, then try again"}, nil
	}
	if key == nil {
		if key, err = fetch.Bytes(q.KeyURL, 1<<20); err != nil {
			return refusal, err
		}
	}
	k, err := pgp.Read(key)
	if err != nil {
		return [2]string{"vendor key unreadable", err.Error()}, nil
	}
	if !k.Matches(q.Fingerprints) {
		return [2]string{q.Name + " key fingerprints differ from the catalog", strings.Join(k.Fingerprints, ", ")}, nil
	}
	ok, updated := t.planCap(capability.RepoRecipe(q, k.Binary, pkgs))
	if !ok {
		return refusal, nil
	}
	if sim != nil {
		name := "dovy-" + q.Name
		sim.keys[name+".pgp"] = string(k.Binary)
		sim.sources[name+".sources"] = capability.RepoSource(q, "@KEYS@/"+name+".pgp", pkgs)
		if q.Pinned() {
			sim.prefs[name+".pref"] = capability.RepoPin(q, pkgs)
		}
	}
	if updated {
		users = "updated: " + strings.Join(pkgs, ", ")
	}
	item := host + " repository"
	if !q.Pinned() {
		item += ", not pinned"
		t.unpinned = append(t.unpinned, host)
	}
	t.system = append(t.system, ui.Pair{Item: item, Reason: users})
	return refusal, nil
}

// Every i386 package is purged in a final step, then dovy-i386's postrm
// removes the architecture.
func (t *txn) leaveI386(stubs []string) error {
	if _, ok := t.removing[capability.I386]; !ok && !t.unused[capability.I386] {
		return nil
	}
	t.final = map[string]bool{capability.I386: true}
	if _, err := os.Stat(capability.I386Added); err != nil {
		return nil // i386 was there before dovy: it stays
	}
	var own []string
	for n := range manualPkgs() {
		if strings.HasSuffix(n, ":i386") {
			own = append(own, n)
		}
	}
	if len(own) > 0 {
		sort.Strings(own)
		t.kept = append(t.kept, ui.Pair{Item: "i386 architecture", Reason: strings.Join(own, ", ") + " installed by the user"})
		return nil
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

// The repository is read as the user through a temporary apt configuration,
// never added to the system's apt (ADR 0014).
func (t *txn) repacked(r *catalog.Recipe, cache string) (path, version string, refusal [2]string, err error) {
	q := r.Repo
	host := capability.Host(q.URI)
	if s := capability.UsedBy(capability.System("/etc/apt"), host); s != nil {
		return "", "", [2]string{host + " is already configured outside dovy", s.File + "; remove it, then try again"}, nil
	}
	b, err := fetch.Bytes(q.KeyURL, 1<<20)
	if err != nil {
		return "", "", refusal, err
	}
	k, err := pgp.Read(b)
	if err != nil {
		return "", "", [2]string{"vendor key unreadable", err.Error()}, nil
	}
	if !k.Matches(q.Fingerprints) {
		return "", "", [2]string{q.Name + " key fingerprints differ from the catalog", strings.Join(k.Fingerprints, ", ")}, nil
	}
	dir := filepath.Join(t.tmp, "repack", q.Name)
	parts, lists, dl := filepath.Join(dir, "parts"), filepath.Join(dir, "lists"), filepath.Join(cache, "vendor", q.Name)
	for _, d := range []string{parts, filepath.Join(lists, "partial"), filepath.Join(dir, "cache", "archives", "partial"), dl} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", "", refusal, err
		}
	}
	keyring := filepath.Join(dir, "key.pgp")
	if err := os.WriteFile(keyring, k.Binary, 0o644); err != nil {
		return "", "", refusal, err
	}
	if err := os.WriteFile(filepath.Join(parts, q.Name+".sources"), []byte(capability.RepoSource(q, keyring, nil)), 0o644); err != nil {
		return "", "", refusal, err
	}
	// No dpkg status: an installed repack (2.105.0-1~dovy13) sorts above
	// the vendor version (2.105.0), becomes the candidate and has no source here.
	opts := []string{"-q", "-o", "Dir::Etc::SourceList=/dev/null", "-o", "Dir::Etc::SourceParts=" + parts,
		"-o", "Dir::Etc::PreferencesParts=" + parts, "-o", "Dir::State::Lists=" + lists,
		"-o", "Dir::State::status=/dev/null",
		"-o", "Dir::Cache=" + filepath.Join(dir, "cache"), "-o", "Debug::NoLocking=1"}
	for _, args := range [][]string{{"update"}, {"download", r.Name}} {
		cmd := exec.Command("apt-get", append(append([]string{}, opts...), args...)...)
		cmd.Dir = dl
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", "", refusal, fmt.Errorf("%s repository: apt-get %s: %s", q.Name, args[0], strings.TrimSpace(string(out)))
		}
	}
	got, _ := filepath.Glob(filepath.Join(dl, r.Name+"_*.deb"))
	newest := ""
	for _, g := range got {
		v, _ := exec.Command("dpkg-deb", "-f", g, "Version").Output()
		if ver := strings.TrimSpace(string(v)); newest == "" || apt.Compare(ver, version) > 0 {
			newest, version = g, ver
		}
	}
	if newest == "" {
		return "", "", refusal, fmt.Errorf("%s repository: no %s downloaded", q.Name, r.Name)
	}
	if apt.Compare(version, r.Version) < 0 {
		return "", "", [2]string{"the repository offers " + version + ", older than the reviewed " + r.Version, host}, nil
	}
	version = fmt.Sprintf("%s-%d~dovy%d", version, r.Rev(), t.release) // ADR 0010
	path, err = build.Repack(r, newest, version, cache)
	return path, version, refusal, err
}
