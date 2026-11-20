package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/state"
	"github.com/getdovy/dovy/internal/ui"
)

// check only reports; it changes nothing.
func (a *app) check() int {
	problems := 0
	ok := func(what string) { a.out.Say(ui.Green, "OK:", what) }
	bad := func(what, fix string) {
		problems++
		a.out.Say(ui.Red, "Problem:", what)
		a.out.Under("fix: " + fix)
	}

	var rs map[string]*catalog.Recipe
	if err := catalog.Verify(a.dir, a.signers); err != nil {
		bad("catalog: "+err.Error(), "dovy update")
	} else if rs, _ = catalog.Load(a.dir, nil); rs == nil {
		bad("catalog: recipes fail their checks", "dovy update")
	} else {
		tag, _ := catalog.Tag(a.dir)
		ok("catalog " + tag + " signed by a trusted key")
		localInputs(rs)
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
	// A vendor's package runs the vendor's scripts, which may rewrite its files:
	// reported, not a problem.
	var vendor []string
	for _, p := range ours {
		if r := rs[inst[p].Recipe]; r != nil && (build.IsDeb(r) || r.Repo != nil && r.Repo.Repack) {
			vendor = append(vendor, p)
		}
	}
	altered, edited := verify(ours)
	theirs, _ := verify(vendor)
	changed := map[string]bool{}
	for _, f := range theirs {
		changed[f] = true
	}
	var byHand []string
	for _, f := range altered {
		if !changed[f] {
			byHand = append(byHand, f)
		}
	}
	if len(byHand) > 0 {
		bad("dovy's files changed by hand: "+brief(byHand), "dovy install --reinstall <recipe>")
	} else {
		ok(fmt.Sprintf("files of %d dovy %s as installed", len(ours), plural(len(ours), "package")))
	}
	if len(theirs) > 0 {
		a.out.Say(ui.Dim, "Note:", fmt.Sprintf("%d files changed by vendor packages' own scripts: %s", len(theirs), brief(theirs)))
	}
	if len(edited) > 0 {
		a.out.Say(ui.Dim, "Kept:", "edited config files: "+strings.Join(edited, ", ")+" (8.5); dovy's version back: dovy install --reinstall --restore-config <recipe>")
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
			// A local-build's folder may go: what it installed stays.
			if why := unavailable(r, rs); why != "" && r.Kind != catalog.LocalBuild {
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
	if fontsMS(inst) {
		if dirs := unstripped(fontList()); len(dirs) > 0 {
			folder := "~/Downloads/fonts-ms"
			if r := rs["fonts-windows"]; r != nil && r.Local != nil {
				folder = r.Local.Path
			}
			bad("Microsoft fonts whose Arabic is too small, outside dovy: "+brief(dirs),
				"move them to "+folder+", then: dovy install fonts-windows (or dovy upgrade when it is installed)")
		} else {
			ok("Microsoft fonts: none with their small Arabic")
		}
	}
	if problems > 0 {
		return exitFailed
	}
	return exitOK
}

// smallArabic are the families whose Arabic fonts-windows strips; keep in sync
// with its build.sh.
var smallArabic = map[string]bool{"Arial": true, "Times New Roman": true, "Courier New": true, "Calibri": true,
	"Microsoft Sans Serif": true, "Tahoma": true, "Segoe UI": true}

// Microsoft fonts are dovy's to check only with fonts-ms or fonts-windows installed.
func fontsMS(inst map[string]pkgInfo) bool {
	for _, i := range inst {
		if i.Recipe == "fonts-ms" || i.Recipe == "fonts-windows" {
			return true
		}
	}
	return false
}

// fontList lines are "file\tfamilies\tlanguages"; fonts that fonts-ms hides are
// not listed.
func fontList() string {
	out, _ := exec.Command("fc-list", "-f", "%{file}\t%{family}\t%{lang}\n").Output()
	return string(out)
}

// Collections (.ttc) are skipped: fonts-windows does not strip them either.
func unstripped(list string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, l := range strings.Split(list, "\n") {
		f := strings.Split(l, "\t")
		if len(f) != 3 || strings.HasSuffix(strings.ToLower(f[0]), ".ttc") {
			continue
		}
		ours := false
		for _, fam := range strings.Split(f[1], ",") {
			ours = ours || smallArabic[fam]
		}
		hasAr := false
		for _, lang := range strings.Split(f[2], "|") {
			hasAr = hasAr || lang == "ar"
		}
		if d := filepath.Dir(f[0]); ours && hasAr && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	sort.Strings(dirs)
	return dirs
}

func brief(s []string) string {
	if len(s) <= 5 {
		return strings.Join(s, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(s[:5], ", "), len(s)-5)
}

// Edited conffiles are returned apart: they are kept by design.
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
