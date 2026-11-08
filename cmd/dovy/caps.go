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
	"github.com/getdovy/dovy/internal/ui"
)

// foreign reports whether dpkg already has the i386 architecture.
func foreign() bool {
	out, _ := exec.Command("dpkg", "--print-foreign-architectures").Output()
	return strings.Contains(" "+strings.Join(strings.Fields(string(out)), " ")+" ", " i386 ")
}

// capabilities plans what t's recipes need (5.1–5.4): new capabilities, and
// installed ones whose content changed, built and installed first.
// needs maps a capability to the recipes asking for it.
func (t *txn) capabilities(needs map[string][]string, codename string) error {
	srcs := capability.System("/etc/apt")
	compText, added := capability.ComponentsSource(srcs, codename)
	_, compInstalled := t.inst[capability.Components]
	var extra []string
	if needs["components"] != nil || compInstalled {
		extra = added
	}
	files := map[string]string{} // new sources for the simulation; "" removes
	arch := false
	for _, name := range []string{"components", "backports", "i386"} {
		pkg := capability.Package(name)
		i, installed := t.inst[pkg]
		if needs[name] == nil && !installed {
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
			arch = !foreign()
		}
		if installed && i.Content == deb.ContentHash(r) {
			continue
		}
		if installed { // a new version, never reused (ADR 0010)
			n, _ := strconv.Atoi(strings.SplitN(i.Version, "-", 2)[0])
			r.Version = strconv.Itoa(n + 1)
		}
		t.build = append([]*catalog.Recipe{r}, t.build...)
		t.versions[pkg] = deb.Version(r, t.release)
		t.caps[pkg] = true
		file := "dovy-" + name + ".sources"
		if name != "i386" {
			files[file] = ""
			for _, f := range r.Files {
				files[file] = f.Content
			}
		}
		reason := strings.Join(needs[name], ", ")
		if installed {
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
	if len(files) == 0 && !arch {
		return nil
	}
	return t.withConfig(files, arch)
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
