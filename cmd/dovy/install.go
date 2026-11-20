package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/repo"
	"github.com/getdovy/dovy/internal/sandbox"
	"github.com/getdovy/dovy/internal/state"
	"github.com/getdovy/dovy/internal/ui"
)

// resolve adds depends and bundle members, dependencies first.
func resolve(rs map[string]*catalog.Recipe, names []string) ([]*catalog.Recipe, string) {
	var order []*catalog.Recipe
	seen := map[string]bool{}
	var visit func(n string) string
	visit = func(n string) string {
		if seen[n] {
			return ""
		}
		seen[n] = true
		r := rs[n]
		if r == nil {
			return n
		}
		for _, d := range append(append([]string{}, r.Depends...), r.Members...) {
			if missing := visit(d); missing != "" {
				return missing
			}
		}
		order = append(order, r)
		return ""
	}
	for _, n := range names {
		if missing := visit(n); missing != "" {
			return nil, missing
		}
	}
	return order, ""
}

func (a *app) install(args []string) int {
	var names, opts []string
	for _, x := range args {
		if strings.HasPrefix(x, "-") {
			opts = append(opts, x)
		} else {
			names = append(names, x)
		}
	}
	reinstall, restore := contains(opts, "--reinstall"), contains(opts, "--restore-config")
	retry := "dovy install " + strings.Join(args, " ")
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
	order, missing := resolve(rs, names)
	if missing != "" {
		a.errs.Fail("no recipe "+missing, "unchanged", "dovy search "+missing, "")
		return exitFailed
	}
	release, err := deb.Release("/etc/os-release")
	if err == nil {
		_, err = capability.Codename("/etc/os-release")
	}
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "dovy runs on Debian stable", "")
		return exitFailed
	}
	codename, _ := capability.Codename("/etc/os-release")
	inst, err := dpkgInstalled()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check that dpkg works", "")
		return exitFailed
	}
	byRecipe := recipePackages(inst)
	// Hardware first, without the network; dependents inherit.
	var refused []string
	why := ""
	for _, r := range order {
		if w := unavailable(r, rs); w != "" {
			refused, why = append(refused, r.Name), w
		}
	}
	if len(refused) > 0 {
		a.errs.Refuse(strings.Join(refused, ", "), why, "")
		return exitRefused
	}
	t := &txn{verb: "install", options: opts, restoring: restore, release: release, versions: map[string]string{}, caps: map[string]bool{},
		removing: map[string]string{}, down: map[string]bool{}, inst: inst}
	needs := map[string][]string{} // capability -> recipes
	// A bundle member whose other half is installed or asked for is skipped:
	// the user's choice stands.
	member := map[string]string{}
	for _, r := range order {
		for _, m := range r.Members {
			member[m] = r.Name
		}
	}
	var kept []*catalog.Recipe
	for _, r := range order {
		skip := ""
		if b := member[r.Name]; b != "" && !contains(names, r.Name) {
			for _, c := range r.Conflicts {
				if _, ok := byRecipe[c]; ok || contains(names, c) {
					skip = c
				}
			}
		}
		if skip != "" {
			t.system = append(t.system, ui.Pair{Item: r.Name + " skipped", Reason: skip + " installed (" + member[r.Name] + ")"})
			continue
		}
		kept = append(kept, r)
	}
	order = kept
	for _, r := range order {
		if build.Sourced(r) {
			if err := sandbox.Check(); err != nil {
				a.errs.Refuse(r.Name, err.Error(), "")
				return exitRefused
			}
		}
		if !supports(r, release) {
			a.errs.Refuse(r.Name, fmt.Sprintf("supports Debian %v", r.Debian), fmt.Sprintf("Debian %d", release))
			return exitRefused
		}
		v := deb.Version(r, release)
		again := false // installed as the catalog has it: only --reinstall goes on
		if p, ok := byRecipe[r.Name]; ok && inst[p].Version == v {
			if !reinstall || !contains(names, r.Name) {
				continue
			}
			again = true
		} else if restore && contains(names, r.Name) {
			// --force-confmiss would act on every package apt installs:
			// only a reinstall of what is there may restore config files.
			a.errs.Refuse(r.Name, "--restore-config needs it installed at the catalog's version", "dovy install "+r.Name+", then: dovy install --reinstall --restore-config "+r.Name)
			return exitRefused
		}
		t.names = append(t.names, r.Name)
		if build.Sourced(r) {
			why := "network off"
			if r.Network != "" {
				why = "network: " + r.Network
			}
			item := "built from source in the sandbox"
			if r.Local != nil {
				item, why = "built from your files in the sandbox", r.Local.Path+", read only; "+why
			}
			t.system = append(t.system, ui.Pair{Item: item, Reason: r.Name + " (" + why + ")"})
		}
		x := capability.Expand(r, rs, codename)
		t.replace(x, byRecipe, inst)
		if again {
			t.again(r, v, inst)
		}
		t.build = append(t.build, x)
		t.versions[r.Package()] = v
		if r.UserNotes != "" {
			t.notes = append(t.notes, r)
		}
		for _, c := range r.Needs() {
			needs[c] = append(needs[c], r.Name)
		}
		if r.Backports && !again { // a reinstall installs dovy's package only
			for _, p := range r.Packages {
				t.request = append(t.request, p+"/"+codename+"-backports")
			}
		}
	}
	if len(t.build) == 0 {
		fmt.Fprintln(a.out.Out, "Nothing to do: "+strings.Join(names, ", ")+" already installed.")
		return exitOK
	}
	t.sbNotes = secureBoot(t.build)
	tmp, err := os.MkdirTemp("", "dovy-")
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer os.RemoveAll(tmp)
	track(tmp)
	t.tmp = tmp

	// Capabilities are built from the system's sources; vendor repository keys
	// are checked now.
	t.inst = inst
	a.at("sources")
	sim := newSimConfig()
	if err := t.capabilities(needs, codename, sim); err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /etc/apt and the network, then: "+retry, "")
		return exitFailed
	}
	using := map[string]bool{} // recipes installed or coming
	for r := range byRecipe {
		using[r] = true
	}
	users := map[*catalog.Repository][]string{}
	for _, r := range order {
		if r.Repo != nil && !r.Repo.Repack && contains(t.names, r.Name) {
			using[r.Name] = true
			users[r.Repo] = append(users[r.Repo], r.Name)
		}
	}
	// Repacked packages are downloaded and repacked now, then planned as
	// themselves (ADR 0014).
	for _, r := range order {
		if r.Repo == nil || !r.Repo.Repack || !contains(t.names, r.Name) || t.fromRepo[r.Name] {
			continue
		}
		cache, err := build.CacheDir()
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "set HOME or XDG_CACHE_HOME", "")
			return exitFailed
		}
		a.at("fetching " + r.Name)
		d, v, refusal, err := t.repacked(r, cache)
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check the network, then: "+retry, "")
			return exitFailed
		}
		if refusal[0] != "" {
			a.errs.Refuse(r.Name, refusal[0], refusal[1])
			return exitRefused
		}
		if t.prebuilt == nil {
			t.prebuilt = map[string]string{}
		}
		t.prebuilt[r.Name] = d
		t.versions[r.Package()] = v
		t.system = append(t.system, ui.Pair{Item: capability.Host(r.Repo.URI) + ", repacked", Reason: r.Name})
	}
	for q, who := range users {
		refusal, err := t.repository(q, repoPackages(q, rs, using), nil, strings.Join(who, ", "), sim)
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check the network, then: "+retry, "")
			return exitFailed
		}
		if refusal[0] != "" {
			a.errs.Refuse(strings.Join(who, " "), refusal[0], refusal[1])
			return exitRefused
		}
	}
	if !sim.empty() {
		a.at("reading new sources")
		if err := t.withConfig(sim); err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check /etc/apt and the network, then: "+retry, "")
			return exitFailed
		}
	}

	// Pairs: the other half goes; its backports packages return to stable
	// unless this half keeps them on backports.
	for _, x := range t.build {
		r := rs[x.Name]
		if r == nil {
			continue
		}
		for _, c := range r.Conflicts {
			p, ok := byRecipe[c]
			if !ok {
				continue
			}
			t.removing[p] = "replaced by " + r.Name
			t.request = append(t.request, p+"-")
			x.Replaced = inherit(x, inst[p].Replaced)
			if o := rs[c]; o != nil {
				t.expectWith(o, inst)
			}
			if o := rs[c]; o != nil && o.Backports && !r.Backports {
				t.leaveBackports(o.Packages, nil, inst, nil)
			}
		}
	}
	return a.run(t)
}

// The other half took away the packages x also replaces, so x's removal must
// bring them back (ADR 0016).
func inherit(x *catalog.Recipe, replaced string) []string {
	out := append([]string{}, x.Replaced...)
	have := map[string]bool{}
	for _, e := range out {
		have[strings.Fields(e)[0]] = true
	}
	for _, e := range splitList(replaced) {
		f := strings.Fields(e)
		if len(f) == 2 && contains(x.Replaces, f[0]) && !have[f[0]] {
			have[f[0]] = true
			out = append(out, e)
		}
	}
	return out
}

// localRepo is where a reinstall looks for its copies; tests change it.
var localRepo = repo.Dir

// Copies in the local repository are reused when intact, else rebuilt. Debian's
// and vendors' packages the recipe brings are apt's to reinstall.
func (t *txn) again(r *catalog.Recipe, v string, inst map[string]pkgInfo) {
	if t.reinstall == nil {
		t.reinstall, t.fromRepo = map[string]bool{}, map[string]bool{}
	}
	copies := map[string]string{}
	whole := true
	var mine []string
	for p, i := range inst {
		if i.Recipe != r.Name || i.Version != v {
			continue
		}
		name := strings.SplitN(p, ":", 2)[0]
		t.reinstall[name] = true
		mine = append(mine, name)
		if d, ok := repo.Find(localRepo, name, v); ok {
			copies[name] = d
		} else {
			whole = false
		}
	}
	if t.restoring {
		t.restoreConfig(r, mine)
	}
	if !whole || copies[r.Package()] == "" {
		return
	}
	t.fromRepo[r.Name] = true
	if t.prebuilt == nil {
		t.prebuilt = map[string]string{}
	}
	t.prebuilt[r.Name] = copies[r.Package()]
	for name, d := range copies {
		if name == r.Package() {
			continue
		}
		if t.parts == nil {
			t.parts = map[string]string{}
		}
		t.parts[name], t.versions[name] = d, v
	}
}

// An edited config file is moved to <file>.dpkg-old before apt runs; dpkg
// recreates every missing one.
func (t *txn) restoreConfig(r *catalog.Recipe, pkgs []string) {
	_, edited := verify(pkgs)
	if len(edited) == 0 {
		return
	}
	sort.Strings(edited)
	t.configs = append(t.configs, edited...)
	t.system = append(t.system, ui.Pair{Item: "config restored: " + strings.Join(edited, ", "), Reason: r.Name + "; edited copies kept as <file>.dpkg-old"})
}

// A recipe already installed keeps its record of replaced packages (ADR 0016).
func (t *txn) replace(r *catalog.Recipe, byRecipe map[string]string, inst map[string]pkgInfo) {
	if p, ok := byRecipe[r.Name]; ok {
		r.Replaced = splitList(inst[p].Replaced)
		return
	}
	manual := manualPkgs()
	for _, p := range r.Replaces {
		i, ok := installedAs(inst, p) // a Multi-Arch: same package is name:amd64 to dpkg
		if !ok || i.Recipe != "" {
			continue
		}
		mark := "auto"
		if manual[p] {
			mark = "manual"
		}
		r.Replaced = append(r.Replaced, p+" "+mark)
		if p != r.Package() {
			t.removing[p] = "replaced by " + r.Name
			t.request = append(t.request, p+"-")
		}
	}
}

// apt may remove r's automatic packages by a conflict rather than as unused.
// Manual ones stay surprises.
func (t *txn) expectWith(r *catalog.Recipe, inst map[string]pkgInfo) {
	manual := manualPkgs()
	for _, p := range r.Packages {
		if _, ok := installedAs(inst, p); ok && !manual[p] && t.removing[p] == "" {
			t.removing[p] = "with " + r.Name
		}
	}
}

// Asked of apt once per run: the plan is made before anything changes.
var manualPkgs = sync.OnceValue(func() map[string]bool {
	m := map[string]bool{}
	if out, err := exec.Command("apt-mark", "showmanual").Output(); err == nil {
		for _, n := range strings.Fields(string(out)) {
			m[n] = true
		}
	}
	return m
})

func splitList(v string) []string {
	var s []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			s = append(s, x)
		}
	}
	return s
}

// Packages with no stable version (a versioned kernel) or nothing left using
// them are removed; those other backports recipes use are skipped.
func (t *txn) leaveBackports(pinned, others []string, inst map[string]pkgInfo, leaving map[string]bool) {
	manual := manualPkgs()
	keep := map[string]bool{}
	for _, p := range installedDeps(others) {
		keep[p] = true
	}
	var pkgs []string
	for _, p := range installedDeps(pinned) {
		i, ok := installedAs(inst, p)
		if !ok || keep[p] || !strings.Contains(i.Version, "~bpo") {
			continue
		}
		if leaving[p] { // unused once the recipe goes: removed, named
			t.removing[p] = "from backports"
			t.request = append(t.request, p+"-")
			continue
		}
		if manual[p] && !contains(pinned, p) {
			continue // the user's own backports package
		}
		pkgs = append(pkgs, p)
	}
	stable := apt.Stable(t.opts, pkgs)
	for _, p := range pkgs {
		if v := stable[p]; v != "" {
			t.down[p] = true
			t.request = append(t.request, p+"="+v)
		} else {
			t.removing[p] = "from backports"
			t.request = append(t.request, p+"-")
		}
	}
}

func installedDeps(pkgs []string) []string {
	if len(pkgs) == 0 {
		return nil
	}
	args := append([]string{"depends", "--recurse", "--installed", "--no-recommends", "--no-suggests",
		"--no-conflicts", "--no-breaks", "--no-replaces", "--no-enhances"}, pkgs...)
	out, _ := exec.Command("apt-cache", args...).Output()
	seen := map[string]bool{}
	var deps []string
	for _, l := range strings.Split(string(out), "\n") {
		if l == "" || l[0] == ' ' || l[0] == '<' || seen[l] {
			continue
		}
		seen[l] = true
		deps = append(deps, l)
	}
	return deps
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func plural(n int, s string) string {
	if n == 1 {
		return s
	}
	return s + "s"
}

func supports(r *catalog.Recipe, release int) bool {
	for _, d := range r.Debian {
		if d == release {
			return true
		}
	}
	return false
}

func pkgArgs(m map[string]string) []string {
	var s []string
	for n, v := range m {
		s = append(s, n+"="+v)
	}
	sort.Strings(s)
	return s
}

// aptLine tidies status-channel lines and shows anything else a package prints.
func (a *app) aptLine(versions map[string]string, warnings *[]string) func(string) {
	last := ""
	say := func(color, verb, text string) {
		if verb+text != last { // apt repeats some lines
			a.out.Say(color, verb, text)
		}
		last = verb + text
	}
	return func(l string) {
		if s, ok := apt.ParseStatus(l); ok {
			if s.Percent >= 0 && s.Download == a.dl.Load() { // the live line's bar
				a.pct.Store(int64(s.Percent))
			}
			if s.Package != "" && !s.Download {
				a.pkg.Store(&s.Package)
			}
			if s.Verb == "" {
				return
			}
			color := ui.Green
			switch s.Verb {
			case "Removing:", "Purging:", "Error:":
				color = ui.Red
			}
			text := s.Package
			if v := versions[s.Package]; v != "" {
				text += " (" + v + ")"
			}
			say(color, s.Verb, text)
			return
		}
		if f, ok := apt.ParseFetch(l); ok {
			switch f.Kind {
			case "Get":
				if name, _, _ := strings.Cut(f.Text, " "); f.Package {
					a.pkg.Store(&name)
				}
				if f.Package {
					size := f.Size
					if n := apt.Bytes(f.Size); n >= 0 {
						size = ui.Bytes(n)
					}
					say(ui.Green, "Fetched:", f.Text+" ["+size+"]")
				} else {
					say(ui.Green, "Updated:", f.Text)
				}
			case "Hit":
				say(ui.Dim, "No Change:", f.Text)
			case "Ign":
				if !strings.HasPrefix(f.Text, "file:"+repo.Dir+" ") { // unsigned by design (ADR 0011)
					say(ui.Dim, "Ignored:", f.Text)
				}
			case "Err":
				say(ui.Red, "Error:", f.Text)
			}
			return
		}
		if strings.HasPrefix(l, "debconf: ") && warnings != nil {
			*warnings = append(*warnings, l) // a question answered with its default
		}
		if !apt.Chatter(l) {
			a.out.Raw(l)
		}
	}
}

func notInstalled(pkg string) bool {
	out, _ := exec.Command("dpkg-query", "-W", "-f", "${db:Status-Abbrev}", pkg).Output()
	return !strings.HasPrefix(string(out), "ii")
}

func (a *app) notices(rs []*catalog.Recipe, versions map[string]string) {
	var reboot []string
	if b, err := os.ReadFile("/run/reboot-required.pkgs"); err == nil {
		for _, n := range strings.Fields(string(b)) {
			if _, ok := versions[n]; ok && !notInstalled(n) {
				reboot = append(reboot, n)
			}
		}
	}
	var notes []*catalog.Recipe
	for _, r := range rs {
		if r.UserNotes != "" {
			notes = append(notes, r)
		}
	}
	if len(reboot) == 0 && len(notes) == 0 {
		return
	}
	a.out.Rule(ui.Yellow, "Notices")
	if len(reboot) > 0 {
		fmt.Fprintln(a.out.Out, "  Reboot required by: "+strings.Join(reboot, ", "))
	}
	for _, r := range notes {
		fmt.Fprintln(a.out.Out, "  "+r.Name+", once per user:")
		fmt.Fprintln(a.out.Out, "    "+r.UserNotes)
	}
	a.out.Blank()
}

// unfinished lets dpkg recover once confirmed, then offers the earlier plan.
// exitOK means the current command may go on.
func (a *app) unfinished() int {
	j, err := state.Unfinished(state.Dir)
	if err != nil {
		a.errs.Fail("journal: "+err.Error(), "unknown", "sudo dpkg --configure -a", "")
		return exitFailed
	}
	if j == nil {
		return exitOK
	}
	what := j.Command + " " + strings.Join(j.Recipes, " ")
	a.out.Rule(ui.Yellow, "Unfinished")
	a.out.KV("Operation", "dovy "+what+", started "+j.Started.Local().Format("2006-01-02 15:04"))
	a.out.KV("Recovery", "sudo dpkg --configure -a, then apt-get -f install, then the plan again")
	a.out.Blank()
	if !a.out.Confirm(a.in) {
		fmt.Fprintln(a.out.Out, "Cancelled. Nothing changed.")
		return exitCancelled
	}
	stop, err := a.sudo()
	if err != nil {
		a.errs.Fail("sudo: "+err.Error(), "possibly half-installed", "dovy "+what, "")
		return exitFailed
	}
	defer stop()
	if err := a.root("dpkg", "--configure", "-a"); err != nil {
		a.errs.Fail("dpkg --configure -a: "+err.Error(), "possibly half-installed", "sudo dpkg --configure -a", "")
		return exitFailed
	}
	if err := apt.Run(io.Discard, a.aptLine(nil, nil), append([]string{"install", "-f", "-y"}, apt.Options...)...); err != nil {
		a.errs.Fail(err.Error(), "possibly half-installed", "sudo apt-get -f install", "")
		return exitFailed
	}
	if err := a.root("rm", "-f", state.JournalPath(state.Dir)); err != nil {
		a.errs.Fail(err.Error(), "dpkg recovered; journal left", "sudo rm "+state.JournalPath(state.Dir), "")
		return exitFailed
	}
	a.out.Say(ui.Green, "Recovered:", "dpkg is consistent. Planning dovy "+what+" again.")
	a.out.Blank()
	switch j.Command {
	case "install":
		return a.install(append(append([]string{}, j.Options...), j.Recipes...))
	case "remove":
		return a.remove(j.Recipes)
	case "upgrade":
		return a.upgrade()
	}
	return exitOK
}

func (a *app) lock() (*os.File, int) {
	f, err := state.Lock(state.Dir)
	if errors.Is(err, state.ErrBusy) {
		a.errs.Fail("another dovy is running", "unchanged", "wait for it to finish", "")
		return nil, exitFailed
	}
	if err != nil {
		a.errs.Fail("lock: "+err.Error(), "unchanged", "check "+state.Dir, "")
		return nil, exitFailed
	}
	return f, exitOK
}

// sudo keeps the timestamp fresh until stop.
func (a *app) sudo() (stop func(), err error) {
	cmd := exec.Command("sudo", "-v")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				exec.Command("sudo", "-n", "-v").Run()
			}
		}
	}()
	return func() { close(done) }, nil
}
