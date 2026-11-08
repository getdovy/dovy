package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/repo"
	"github.com/getdovy/dovy/internal/ui"
)

// app is what the commands need; tests replace any of it.
type app struct {
	out, errs *ui.UI
	dir       string                            // local catalog
	url       string                            // where updates come from
	signers   []byte                            // trusted keys
	installed func() (map[string]string, error) // recipe → installed version
	in        io.Reader                         // answers to Continue?
	root      repo.Root                         // runs a command as root
}

func newApp(out, errs *ui.UI) (*app, error) {
	dir, err := catalog.Dir()
	if err != nil {
		return nil, err
	}
	signers := catalog.Signers(catalog.KeysDir, func(s string) { errs.Say(ui.Yellow, "Warning:", s) })
	return &app{out: out, errs: errs, dir: dir, url: catalog.URL, signers: signers,
		installed: installed, in: os.Stdin, root: repo.Sudo}, nil
}

// installed asks dpkg which dovy packages are installed.
func installed() (map[string]string, error) {
	out, err := exec.Command("dpkg-query", "-W", "-f", "${X-Dovy-Recipe}\t${Version}\t${db:Status-Abbrev}\n").Output()
	if err != nil {
		return nil, fmt.Errorf("dpkg-query: %w", err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Split(l, "\t")
		if len(f) == 3 && f[0] != "" && strings.HasPrefix(f[2], "ii") {
			m[f[0]] = f[1]
		}
	}
	return m, nil
}

// load verifies and reads the catalog (every run, topic 8 rule 2).
func (a *app) load() (map[string]*catalog.Recipe, int) {
	if err := catalog.Verify(a.dir, a.signers); err != nil {
		a.errs.Fail("catalog check: "+err.Error(), "unchanged", "dovy update", "")
		return nil, exitFailed
	}
	rs, err := catalog.Load(a.dir, nil)
	if err != nil {
		a.errs.Fail("catalog check: "+err.Error(), "unchanged", "dovy update", "")
		return nil, exitFailed
	}
	return rs, exitOK
}

func (a *app) update() int {
	c, err := catalog.Update(a.dir, a.url, a.signers)
	if err != nil {
		sys, next := "unchanged; the previous catalog is kept", "check the network, then: dovy update"
		if _, e := os.Stat(a.dir); e != nil {
			sys = "unchanged; no catalog yet"
		}
		if errors.Is(err, catalog.ErrUntrusted) {
			next = "check the maintainer keys in " + catalog.KeysDir
		}
		a.errs.Fail("update: "+err.Error(), sys, next, "")
		return exitFailed
	}
	for _, t := range c.Skipped {
		a.errs.Say(ui.Yellow, "Warning:", t+" skipped: not signed by a trusted key")
	}
	if len(c.Added)+len(c.Updated)+len(c.Dropped) == 0 {
		fmt.Fprintln(a.out.Out, "Catalog verified: "+c.Tag+". Nothing new.")
		return exitOK
	}
	a.out.Rule("", "Catalog updated")
	a.out.Say(ui.Bold, "Release:", c.Tag)
	rs, _ := catalog.Load(a.dir, nil)
	for _, n := range c.Added {
		a.out.Say(ui.Green, "Added:", n+" "+rs[n].Version)
	}
	for _, u := range c.Updated {
		a.out.Say(ui.Cyan, "Updated:", u.Name+" "+u.Old+" → "+u.New)
	}
	for _, n := range c.Dropped {
		a.out.Say(ui.Yellow, "Dropped:", n)
	}
	return exitOK
}

// row is one recipe as list and search show it.
type row struct {
	name, version, status, note, description string
}

func (a *app) rows() ([]row, int) {
	rs, code := a.load()
	if code != exitOK {
		return nil, code
	}
	inst, err := a.installed()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return nil, exitFailed
	}
	var out []row
	for n, r := range rs {
		x := row{name: n, version: r.Version, status: "available", note: note(r), description: r.Description}
		if v, ok := inst[n]; ok {
			x.version, x.status = v, "installed"
			if upstream(v) != r.Version {
				x.status = "update " + r.Version
			}
		}
		out = append(out, x)
	}
	for n, v := range inst {
		if rs[n] == nil {
			out = append(out, row{name: n, version: v, status: "orphaned", note: "dropped from the catalog"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, exitOK
}

// upstream strips the Debian revision: 1.0.0.85-1~dovy1 → 1.0.0.85.
func upstream(v string) string {
	if i := strings.LastIndex(v, "-"); i > 0 {
		v = v[:i]
	}
	if i := strings.Index(v, ":"); i >= 0 {
		v = v[i+1:]
	}
	return v
}

func note(r *catalog.Recipe) string {
	switch {
	case len(r.Conflicts) > 0:
		return "pair: " + strings.Join(r.Conflicts, ", ")
	case r.Repository != nil:
		if u, err := url.Parse(r.Repository.URI); err == nil {
			return u.Host + " repository"
		}
	}
	return ""
}

func (a *app) list(args []string) int {
	rows, code := a.rows()
	if code != exitOK {
		return code
	}
	filter := ""
	if len(args) == 1 {
		filter = args[0]
	}
	keep := map[string]func(row) bool{
		"":            func(row) bool { return true },
		"--installed": func(r row) bool { return r.status != "available" && r.status != "not available" },
		"--updates":   func(r row) bool { return strings.HasPrefix(r.status, "update") },
		"--orphaned":  func(r row) bool { return r.status == "orphaned" },
	}[filter]
	var grid [][4]string
	for _, r := range rows {
		if keep(r) {
			grid = append(grid, [4]string{r.name, r.version, r.status, r.note})
		}
	}
	title := map[string]string{"": "Catalog", "--installed": "Installed", "--updates": "Updates", "--orphaned": "Orphaned"}[filter]
	a.out.Grid(fmt.Sprintf("%s (%d)", title, len(grid)), [4]string{"Recipe", "Version", "Status", "Note"}, grid)
	hint := map[string]string{"--updates": "Apply: dovy upgrade", "--orphaned": "Remove: dovy remove <recipe>"}[filter]
	if hint != "" && len(grid) > 0 {
		a.out.Blank()
		fmt.Fprintln(a.out.Out, a.out.Paint(ui.Dim, hint))
	}
	return exitOK
}

func (a *app) search(word string) int {
	rows, code := a.rows()
	if code != exitOK {
		return code
	}
	w := strings.ToLower(word)
	var grid [][4]string
	for _, r := range rows {
		if strings.Contains(r.name, w) || strings.Contains(strings.ToLower(r.description), w) {
			grid = append(grid, [4]string{r.name, r.version, r.status, r.description})
		}
	}
	if len(grid) == 0 {
		fmt.Fprintf(a.out.Out, "No recipe matches %q.\n", word)
		return exitOK
	}
	title := fmt.Sprintf("%d results", len(grid))
	if len(grid) == 1 {
		title = "1 result"
	}
	a.out.Grid(title, [4]string{"Recipe", "Version", "Status", "Description"}, grid)
	return exitOK
}

func (a *app) show(args []string) int {
	name, opt := "", ""
	for _, x := range args {
		if strings.HasPrefix(x, "-") {
			opt = x
		} else {
			name = x
		}
	}
	rs, code := a.load()
	if code != exitOK {
		return code
	}
	r := rs[name]
	if r == nil {
		a.errs.Fail("no recipe "+name, "unchanged", "dovy search "+name, "")
		return exitFailed
	}
	inst, err := a.installed()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return exitFailed
	}
	switch opt {
	case "--recipe":
		b, err := os.ReadFile(filepath.Join(a.dir, "recipes", name, "recipe.toml"))
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "dovy update", "")
			return exitFailed
		}
		a.out.Rule("", name+"/recipe.toml")
		io.WriteString(a.out.Out, string(b))
		return exitOK
	case "--diff":
		if _, ok := inst[name]; !ok {
			a.errs.Fail(name+" is not installed; nothing to compare", "unchanged", "dovy show "+name+" --recipe", "")
			return exitFailed
		}
		a.errs.Fail("show --diff is not built yet", "unchanged", "milestone 2 (dovy-design/build-order.md)", "")
		return exitFailed
	}
	version := r.Version
	if v, ok := inst[name]; ok {
		version = v + " (installed)"
		if upstream(v) != r.Version {
			version += ", " + r.Version + " available"
		}
	}
	a.out.Rule("", name)
	a.out.KV("Description", r.Description)
	a.out.KV("Version", version)
	a.out.KV("Kind", r.Kind)
	a.out.KV("Source", source(r))
	a.out.KV("Requires", or(strings.Join(r.Requires, ", "), "nothing"))
	if len(r.Depends) > 0 {
		a.out.KV("Depends", strings.Join(r.Depends, ", "))
	}
	a.out.KV("Replaces", or(strings.Join(r.Replaces, ", "), "nothing"))
	a.out.KV("Hardware", or(r.Hardware, "any"))
	a.out.KV("Debian", strings.Trim(strings.Join(strings.Fields(fmt.Sprint(r.Debian)), ", "), "[]"))
	a.out.KV("Maintainer", r.Maintainer)
	if r.UserNotes != "" {
		a.out.Blank()
		a.out.Rule("", "Per-user steps")
		for _, l := range strings.Split(strings.TrimSpace(r.UserNotes), "\n") {
			fmt.Fprintln(a.out.Out, "  "+l)
		}
	}
	return exitOK
}

func source(r *catalog.Recipe) string {
	switch {
	case len(r.Sources) > 0:
		return r.Sources[0].URL
	case r.Repository != nil:
		return r.Repository.URI
	case r.SourcePackage != "":
		return "Debian testing: " + r.SourcePackage
	case len(r.Packages) > 0:
		return "Debian: " + strings.Join(r.Packages, " ")
	case len(r.Members) > 0:
		return "bundle: " + strings.Join(r.Members, " ")
	}
	return "files only"
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
