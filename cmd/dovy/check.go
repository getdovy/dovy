package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/state"
	"github.com/getdovy/dovy/internal/ui"
)

// check reports and changes nothing (8.6): one OK or Problem line per
// check, with the fix (12.8).
func (a *app) check() int {
	problems := 0
	ok := func(what string) { a.out.Say(ui.Green, "OK:", what) }
	bad := func(what, fix string) {
		problems++
		a.out.Say(ui.Red, "Problem:", what)
		fmt.Fprintln(a.out.Out, "            fix: "+fix)
	}

	var rs map[string]*catalog.Recipe
	if err := catalog.Verify(a.dir, a.signers); err != nil {
		bad("catalog: "+err.Error(), "dovy update")
	} else if rs, _ = catalog.Load(a.dir, nil); rs == nil {
		bad("catalog: recipes fail their checks", "dovy update")
	} else {
		tag, _ := catalog.Tag(a.dir)
		ok("catalog " + tag + " signed by a trusted key")
	}

	if j, err := state.Unfinished(state.Dir); err != nil || j != nil {
		what := "journal unreadable"
		if j != nil {
			what = "unfinished: dovy " + j.Command + " " + strings.Join(j.Recipes, " ")
		}
		bad(what, "run any dovy command: it offers to recover")
	} else {
		ok("no unfinished operation")
	}

	inst, err := dpkgInstalled()
	if err != nil {
		bad("dpkg: "+err.Error(), "sudo dpkg --configure -a")
		return exitFailed
	}
	var ours []string
	for p, i := range inst {
		if i.Recipe != "" || i.Capability != "" {
			ours = append(ours, p)
		}
	}
	sort.Strings(ours)
	altered, edited := verify(ours)
	if len(altered) > 0 {
		bad("dovy's files changed by hand: "+strings.Join(altered, ", "), "reinstall the recipe: dovy remove <recipe>, then dovy install <recipe>")
	} else {
		ok(fmt.Sprintf("files of %d dovy packages as installed", len(ours)))
	}
	if len(edited) > 0 {
		a.out.Say(ui.Dim, "Kept:", "edited config files: "+strings.Join(edited, ", ")+" (8.5)")
	}

	if d := duplicates(); len(d) > 0 {
		bad("apt sources configured more than once: "+strings.Join(d, "; "), "remove the duplicate entries outside /etc/apt/sources.list.d/dovy-*")
	} else {
		ok("no duplicate apt sources")
	}

	if rs != nil {
		release, _ := deb.Release("/etc/os-release")
		var hw, rel []string
		for _, i := range inst {
			r := rs[i.Recipe]
			if i.Recipe == "" || r == nil {
				continue
			}
			if why := unavailable(r, rs); why != "" {
				hw = append(hw, i.Recipe+" ("+why+")")
			}
			if !supports(r, release) {
				rel = append(rel, i.Recipe)
			}
		}
		sort.Strings(hw)
		sort.Strings(rel)
		if len(hw) > 0 {
			bad("hardware checks fail: "+strings.Join(hw, ", "), "dovy remove <recipe>")
		} else {
			ok("installed recipes pass their hardware checks")
		}
		if len(rel) > 0 {
			bad(fmt.Sprintf("not built for Debian %d: %s", release, strings.Join(rel, ", ")), "wait for the recipe to support it, or dovy remove <recipe>")
		} else {
			ok(fmt.Sprintf("installed recipes support Debian %d", release))
		}
	}
	if problems > 0 {
		return exitFailed
	}
	return exitOK
}

// verify runs dpkg --verify: files changed by hand, and edited conffiles
// (kept by design).
func verify(pkgs []string) (altered, edited []string) {
	if len(pkgs) == 0 {
		return nil, nil
	}
	out, _ := exec.Command("dpkg", append([]string{"--verify"}, pkgs...)...).Output()
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) == 3 && f[1] == "c":
			edited = append(edited, f[2])
		case len(f) == 2:
			altered = append(altered, f[1])
		}
	}
	return altered, edited
}

// duplicates lists apt's "configured multiple times" warnings.
func duplicates() []string {
	out, _ := exec.Command("apt-cache", "-o", "Debug::NoLocking=1", "policy").CombinedOutput()
	var d []string
	seen := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "is configured multiple times") && !seen[l] {
			seen[l] = true
			d = append(d, strings.TrimPrefix(strings.TrimSpace(l), "W: "))
		}
	}
	return d
}
