package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/fetch"
	"github.com/getdovy/dovy/internal/log"
	"github.com/getdovy/dovy/internal/overlap"
	"github.com/getdovy/dovy/internal/repo"
	"github.com/getdovy/dovy/internal/state"
	"github.com/getdovy/dovy/internal/ui"
)

// Set in test builds: -ldflags "-X main.kernelRelease=7.2.12+bpo-amd64".
var kernelRelease = ""

// runningKernel is the running kernel's release, as `uname -r` prints it.
func runningKernel() string {
	if kernelRelease != "" {
		return kernelRelease
	}
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(b))
}

// txn is one dovy operation: what to build, what apt is asked, and what the
// plan allows apt to do (principles 8.3).
type txn struct {
	verb     string            // install, remove
	names    []string          // the recipes asked for
	release  int               // Debian release, for versions
	build    []*catalog.Recipe // recipes and capabilities to build, in order
	versions map[string]string // dovy packages to install → version
	caps     map[string]bool   // capability packages, installed first (8.3.7)
	request  []string          // more apt-get install arguments: pkg/suite, pkg=ver, pkg-
	removing map[string]string // packages the plan removes → note
	down     map[string]bool   // packages the plan downgrades (4.6)
	opts     []string          // apt options while a new source is not installed
	inst     map[string]pkgInfo
	system   []ui.Pair                            // System changes (12.2)
	kept     []ui.Pair                            // Kept (12.2)
	keepFor  func(gone map[string]bool) []ui.Pair // Kept, once apt's plan is known
	notes    []*catalog.Recipe                    // per-user steps after install
	prebuilt map[string]string                    // recipe → .deb already built (vendor .debs, repacks)
	parts    map[string]string                    // package → .deb of a recipe with several (ADR 0018), installed as dependencies
	leftover string                               // notice after remove (4.7)
	restored map[string]string                    // replaced packages coming back → mark (ADR 0016)
	unpinned []string                             // hosts of unpinned repositories (ADR 0017)
	sbNotes  []ui.Pair                            // Secure Boot notes (1.4)
	notices  []ui.Pair                            // what needs the user, after the run

	tmp      string
	actions  []apt.Action
	unused   map[string]bool
	final    map[string]bool // purged after the transaction: i386 packages, then dovy-i386 (4.8)
	warnings []string        // repeated in Notices (12.4)
}

// sourceOpts keeps only the source and list options, for root's download.
func (t *txn) sourceOpts() []string {
	var o []string
	for i := 0; i+1 < len(t.opts); i += 2 {
		if v := t.opts[i+1]; strings.HasPrefix(v, "Dir::Etc::SourceParts=") || strings.HasPrefix(v, "Dir::Etc::PreferencesParts=") ||
			strings.HasPrefix(v, "Dir::State::Lists=") || strings.HasPrefix(v, "APT::Architectures") {
			o = append(o, t.opts[i], t.opts[i+1])
		}
	}
	return o
}

// withConfig sets up a temporary apt configuration as the user: the
// system's sources, pins and lists, with c's changes, so apt can plan before
// anything changes (8.3.3).
func (t *txn) withConfig(c *simConfig) error {
	dir := filepath.Join(t.tmp, "apt")
	parts, prefs, keys, lists := filepath.Join(dir, "parts"), filepath.Join(dir, "prefs"), filepath.Join(dir, "keys"), filepath.Join(dir, "lists")
	for _, d := range []string{parts, prefs, keys, filepath.Join(lists, "partial"), filepath.Join(dir, "cache", "archives", "partial")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	copyAll := `cp /etc/apt/sources.list.d/* "$1"/ 2>/dev/null; cp /etc/apt/preferences.d/* "$2"/ 2>/dev/null; cp /var/lib/apt/lists/*_* "$3"/ 2>/dev/null; true`
	if err := exec.Command("sh", "-c", copyAll, "sh", parts, prefs, lists).Run(); err != nil {
		return err
	}
	write := func(dir string, files map[string]string) error {
		for name, text := range files {
			p := filepath.Join(dir, name)
			if text == "" {
				os.Remove(p)
			} else if err := os.WriteFile(p, []byte(strings.ReplaceAll(text, "@KEYS@", keys)), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	for d, files := range map[string]map[string]string{parts: c.sources, prefs: c.prefs, keys: c.keys} {
		if err := write(d, files); err != nil {
			return err
		}
	}
	t.opts = []string{
		"-o", "Dir::Etc::SourceParts=" + parts, "-o", "Dir::Etc::PreferencesParts=" + prefs,
		"-o", "Dir::State::Lists=" + lists,
		"-o", "Dir::Cache=" + filepath.Join(dir, "cache"), "-o", "Debug::NoLocking=1",
	}
	if c.i386 {
		t.opts = append(t.opts, "-o", "APT::Architectures::=amd64", "-o", "APT::Architectures::=i386")
	}
	// Only what is new is fetched: the system's lists are already there.
	// A new architecture needs every source again.
	update := append([]string{"-q"}, t.opts...)
	if !c.i386 {
		fresh := filepath.Join(dir, "fresh")
		if err := os.MkdirAll(fresh, 0o755); err != nil {
			return err
		}
		if err := write(fresh, c.sources); err != nil {
			return err
		}
		if added, _ := os.ReadDir(fresh); len(added) == 0 {
			return nil
		}
		update = append(update, "-o", "Dir::Etc::SourceList=/dev/null", "-o", "Dir::Etc::SourceParts="+fresh, "-o", "APT::Get::List-Cleanup=0")
	}
	out, err := exec.Command("apt-get", append(update, "update")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("apt-get update with dovy's capabilities: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (t *txn) args(stubs []string) []string {
	a := []string{"install", "--allow-downgrades"}
	return append(append(a, stubs...), t.request...)
}

// asked is the packages t.request names. A package only expected to go
// (expectWith) is not among them: apt must still be asked.
func (t *txn) asked() map[string]bool {
	m := map[string]bool{}
	for _, r := range t.request {
		m[pkgName(r)] = true
	}
	return m
}

// simulate resolves the plan with apt, adding what it leaves unused (4.1).
func (t *txn) simulate(stubs []string) error {
	var base string
	var berr error
	done := make(chan struct{})
	go func() { base, berr = apt.Simulate(t.opts, "autoremove"); close(done) }() // independent: at once
	out, err := apt.Simulate(t.opts, t.args(stubs)...)
	if <-done; err == nil {
		err = berr
	}
	if err != nil {
		return err
	}
	var before []string
	for _, x := range apt.Parse(base) {
		before = append(before, x.Name)
	}
	t.unused = map[string]bool{}
	if extra := unused(before, apt.NoLongerRequired(out), t.asked()); len(extra) > 0 {
		for _, p := range extra {
			if t.removing[p] == "" { // else the recipe's own package, already noted
				t.unused[p] = true
			}
			t.request = append(t.request, p+"-")
		}
		if out, err = apt.Simulate(t.opts, t.args(stubs)...); err != nil {
			return err
		}
	}
	t.actions = apt.Parse(out)
	asked := len(t.request)
	if err := t.leaveI386(stubs); err != nil {
		return err
	}
	if len(t.request) != asked {
		if out, err = apt.Simulate(t.opts, t.args(stubs)...); err != nil {
			return err
		}
		t.actions = apt.Parse(out)
	}
	return nil
}

// check lists what apt would do beyond the plan (4.3); kernel is the running
// kernel's package when the plan removes it (5.8).
func (t *txn) check() (surprises []string, kernel string) {
	running := runningKernel()
	for _, x := range t.actions {
		switch x.Op {
		case "Remv", "Purg":
			if x.Name == "linux-image-"+running || x.Name == "linux-image-"+running+"-unsigned" {
				kernel = x.Name
			}
			if _, ok := t.removing[x.Name]; !ok && !t.unused[x.Name] {
				surprises = append(surprises, "removes "+x.Name+" "+x.Old)
			}
		case "Inst":
			if x.Old != "" && apt.Compare(x.Version, x.Old) < 0 && !t.down[x.Name] {
				surprises = append(surprises, "downgrades "+x.Name+" "+x.Old+" → "+x.Version)
			}
		}
	}
	return surprises, kernel
}

// sections turns the plan into the tables of topic 12, rule 1.
func (t *txn) sections(sizes map[string]apt.Size, sources map[string]int64) ([]ui.Section, []ui.Count, []ui.Pair) {
	var ins, deps, down, rm, rmDeps []ui.Row
	var download, disk int64
	for _, x := range t.actions {
		switch {
		case x.Op == "Inst" && t.versions[x.Name] != "":
			size := "—"
			if n := sources[x.Name]; n > 0 {
				size, download, disk = ui.Bytes(n), download+n, disk+n
			}
			note := ""
			switch {
			case t.caps[x.Name]:
				note = "capability"
			case x.Old != "" && t.inst[x.Name].Recipe == "":
				note = "replaces " + x.Old
			}
			ins = append(ins, ui.Row{Name: x.Name, Version: x.Version, Source: "dovy", Size: size, Note: note})
		case x.Op == "Inst":
			size := "—"
			if s, ok := sizes[x.Name]; ok {
				size, download, disk = ui.Bytes(s.Download), download+s.Download, disk+s.Installed
				if x.Old != "" { // replaces the installed version's files
					disk -= t.inst[x.Name].Size
				}
			}
			row := ui.Row{Name: x.Name, Version: x.Version, Source: x.Origin, Size: size}
			switch {
			case x.Old != "" && apt.Compare(x.Version, x.Old) < 0:
				row.Note = "from " + x.Old
				down = append(down, row)
				continue
			case x.Old != "":
				row.Note = "upgrades " + x.Old
			case t.restored[x.Name] != "":
				row.Note = "restored"
			}
			deps = append(deps, row)
		case x.Op == "Remv" || x.Op == "Purg":
			i, ok := t.inst[x.Name]
			if !ok { // multiarch libraries: dpkg names them with their arch
				i = t.inst[x.Name+":amd64"]
			}
			disk -= i.Size
			src := "Debian"
			switch {
			case i.Recipe != "" || i.Capability != "":
				src = "dovy"
			case strings.Contains(x.Old, "~bpo"):
				src = "Backports"
			}
			row := ui.Row{Name: x.Name, Version: x.Old, Source: src, Size: ui.Bytes(i.Size), Note: t.removing[x.Name]}
			if t.unused[x.Name] {
				rmDeps = append(rmDeps, row)
			} else {
				rm = append(rm, row)
			}
		}
	}
	var secs []ui.Section
	var counts []ui.Count
	add := func(color, title, verb string, rows []ui.Row) {
		if len(rows) > 0 {
			secs = append(secs, ui.Section{Color: color, Title: title, Rows: rows})
			counts = append(counts, ui.Count{Verb: verb, N: len(rows), Unit: plural(len(rows), "package")})
		}
	}
	add(ui.Green, "Installing", "Install", ins)
	add(ui.Green, "Installing dependencies", "Dependencies", deps)
	add(ui.Yellow, "Downgrading", "Downgrade", down)
	add(ui.Red, "Removing", "Remove", rm)
	add(ui.Red, "Removing unused dependencies", "Unused", rmDeps)
	var totals []ui.Pair
	if download > 0 {
		totals = append(totals, ui.Pair{Item: "Download", Reason: ui.Bytes(download)})
	}
	if disk >= 0 {
		totals = append(totals, ui.Pair{Item: "Disk required", Reason: ui.Bytes(disk)})
	} else {
		totals = append(totals, ui.Pair{Item: "Disk freed", Reason: ui.Bytes(-disk)})
	}
	return secs, counts, totals
}

// sourceSizes is each package's download size: all servers asked at once;
// a file already in the cache answers for itself.
func sourceSizes(rs []*catalog.Recipe) map[string]int64 {
	cache, _ := build.CacheDir()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sizes := map[string]int64{}
	for _, r := range rs {
		for _, s := range r.Sources {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var n int64
				if fi, err := os.Stat(filepath.Join(cache, "sources", s.SHA256)); cache != "" && err == nil {
					n = fi.Size()
				} else {
					n = fetch.Size(s.URL)
				}
				if n > 0 {
					mu.Lock()
					sizes[r.Package()] += n
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	return sizes
}

// same reports whether two simulations do the same, ignoring packages in skip.
func same(a, b []apt.Action, skip map[string]bool) bool {
	key := func(as []apt.Action) string {
		var s []string
		for _, x := range as {
			if !skip[x.Name] {
				s = append(s, x.Op+" "+x.Name+" "+x.Version)
			}
		}
		sort.Strings(s)
		return strings.Join(s, "\n")
	}
	return key(a) == key(b)
}

// run plans, confirms and carries out t (8.3).
func (a *app) run(t *txn) int {
	what := strings.Join(t.names, " ")
	again := "dovy " + t.verb + " " + what
	sized := make(chan map[string]int64, 1)
	go func() { sized <- sourceSizes(t.build) }() // while apt plans

	// Resolve packages: apt simulates with control-only stubs; a source
	// build's stub depends on the recipe's packages. A vendor .deb's
	// dependencies are its own: it is fetched and repacked now, into the
	// user's cache, and planned as itself.
	var stubs []string
	for _, r := range t.build {
		if d := t.prebuilt[r.Name]; d != "" {
			stubs = append(stubs, d)
			continue
		}
		if build.IsDeb(r) && len(r.Sources) > 1 { // several packages (ADR 0018)
			cache, err := build.CacheDir()
			var parts []build.Part
			var meta string
			if err == nil {
				parts, meta, err = build.Parts(r, t.release, cache)
			}
			if err != nil {
				a.errs.Fail(err.Error(), "unchanged", "check the network, then: "+again, "")
				return exitFailed
			}
			if t.prebuilt == nil {
				t.prebuilt = map[string]string{}
			}
			t.prebuilt[r.Name] = meta
			stubs = append(stubs, meta)
			if t.parts == nil {
				t.parts = map[string]string{}
			}
			for _, p := range parts {
				t.parts[p.Package] = p.Path
				t.versions[p.Package] = p.Version
				stubs = append(stubs, p.Path)
			}
			continue
		}
		if build.IsDeb(r) {
			cache, err := build.CacheDir()
			if err == nil {
				var d string
				if d, err = build.Recipe(r, t.release, cache); err == nil {
					if t.prebuilt == nil {
						t.prebuilt = map[string]string{}
					}
					t.prebuilt[r.Name] = d
					stubs = append(stubs, d)
					continue
				}
			}
			a.errs.Fail(err.Error(), "unchanged", "check the network, then: "+again, "")
			return exitFailed
		}
		stage := filepath.Join(t.tmp, "stage", r.Name)
		if err := os.MkdirAll(stage, 0o755); err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
			return exitFailed
		}
		s, err := deb.Build(r, t.versions[r.Package()], stage, filepath.Join(t.tmp, "stubs"))
		if err != nil {
			a.errs.Fail(err.Error(), "unchanged", "check that dpkg-deb works", "")
			return exitFailed
		}
		stubs = append(stubs, s)
	}
	if err := t.simulate(stubs); err != nil {
		a.errs.Refuse(what, "apt cannot do it", err.Error())
		return exitRefused
	}
	if why, err := t.turing(); err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check the network, then: "+again, "")
		return exitFailed
	} else if why != "" {
		reason, found, _ := strings.Cut(why, "|")
		a.errs.Refuse(what, reason, found)
		return exitRefused
	}
	t.platform()
	surprises, kernel := t.check()
	if kernel != "" {
		a.errs.Refuse(what, "removes the running kernel ("+kernel+")", "reboot into another kernel, then: "+again)
		return exitRefused
	}
	if len(surprises) > 0 {
		a.errs.Refuse(what, "apt would change packages beyond the plan", strings.Join(surprises, ", "))
		return exitRefused
	}
	if t.keepFor != nil {
		gone := map[string]bool{}
		for _, x := range t.actions {
			if x.Op == "Remv" || x.Op == "Purg" {
				gone[x.Name] = true
			}
		}
		for p := range t.final {
			gone[p] = true
		}
		t.kept = append(t.keepFor(gone), t.kept...)
	}

	// Plan, confirm (12.1, 12.2).
	debian := map[string]string{}
	for _, x := range t.actions {
		if x.Op == "Inst" && t.versions[x.Name] == "" {
			debian[x.Name] = x.Version
		}
	}
	sources := <-sized
	secs, counts, totals := t.sections(apt.Sizes(t.opts, debian), sources)
	if len(secs) == 0 {
		fmt.Fprintln(a.out.Out, "Nothing to do.")
		return exitOK
	}
	a.out.Blank()
	a.out.Plan(secs)
	if len(t.system) > 0 {
		a.out.Pairs(ui.Cyan, "System changes", t.system)
	}
	if len(t.kept) > 0 {
		a.out.Pairs(ui.Cyan, "Kept", t.kept)
	}
	if len(t.sbNotes) > 0 {
		a.out.Pairs(ui.Yellow, "Notes", t.sbNotes)
	}
	a.out.Summary(counts, totals)
	a.out.Blank()
	if !a.out.Confirm(a.in) {
		fmt.Fprintln(a.out.Out, "Cancelled. Nothing changed.")
		return exitCancelled
	}
	a.out.Blank()

	// sudo once, kept fresh for the run (8.11); one dovy at a time (8.4).
	stop, err := a.sudo()
	if err != nil {
		a.errs.Fail("sudo: "+err.Error(), "unchanged", again, "")
		return exitFailed
	}
	defer stop()
	if _, err := os.Stat(state.Dir); err != nil {
		if err := a.root("install", "-d", "-m", "755", state.Dir); err != nil {
			a.errs.Fail(err.Error(), "unchanged", again, "")
			return exitFailed
		}
	}
	lock, code := a.lock()
	if code != exitOK {
		return code
	}
	defer lock.Close()
	// The release this run used; an older one later is a rollback.
	if tag, err := catalog.Tag(a.dir); err == nil {
		if rec := catalog.Recorded(); rec == "" || catalog.Older(rec, tag) {
			p := filepath.Join(t.tmp, "catalog-release")
			if os.WriteFile(p, []byte(tag+"\n"), 0o644) == nil {
				a.root("install", "-m", "644", p, catalog.RecordPath)
			}
		}
	}
	logf, err := log.Open(t.tmp, time.Now())
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "check /tmp", "")
		return exitFailed
	}
	defer logf.Close()
	logPath := filepath.Join(log.Dir, filepath.Base(logf.Name()))
	defer func() {
		logf.Sync()
		a.root("install", "-D", "-m", "644", logf.Name(), logPath)
	}()
	fail := func(failed, system, next string) int {
		fmt.Fprintln(logf, "Failed: "+failed)
		a.errs.Fail(failed, system, next, logPath)
		return exitFailed
	}
	manual := manualPkgs()
	// apt-mark's own lines go to the log.
	mark := func(args ...string) error {
		cmd := exec.Command("sudo", append([]string{"--", "apt-mark"}, args...)...)
		cmd.Stdout, cmd.Stderr = logf, logf
		return cmd.Run()
	}

	// Download (12.3).
	cache, err := build.CacheDir()
	if err != nil {
		return fail(err.Error(), "unchanged", "set HOME or XDG_CACHE_HOME")
	}
	if len(sources) > 0 || len(debian) > 0 {
		a.out.Rule("", "Download")
		for _, r := range t.build {
			for _, s := range r.Sources {
				path, err := fetch.Get(s.URL, s.SHA256, filepath.Join(cache, "sources"))
				if err != nil {
					return fail(err.Error(), "unchanged", "check the network, then: "+again)
				}
				fi, _ := os.Stat(path)
				a.out.Say(ui.Green, "Fetched:", fmt.Sprintf("%s [%s] sha256 ok", filepath.Base(s.URL), ui.Bytes(fi.Size())))
			}
		}
		if len(debian) > 0 {
			args := append(append(append([]string{"install", "--download-only", "--allow-downgrades", "-y"}, apt.Options...), t.sourceOpts()...), pkgArgs(debian)...)
			if err := apt.Run(logf, a.aptLine(nil, nil), args...); err != nil {
				return fail(err.Error(), "unchanged", "check the network, then: "+again)
			}
		}
		a.out.Blank()
	}

	// Build (12.3), into the local repository.
	var debs []string
	for _, r := range t.build {
		a.out.Rule("", "Build: "+r.Name)
		d := t.prebuilt[r.Name]
		if d == "" {
			if build.Sourced(r) {
				a.out.Say(ui.Green, "Building:", "in the sandbox; output in "+build.LogPath(cache, r.Name))
			}
			if d, err = build.Recipe(r, t.release, cache); err != nil {
				return fail(err.Error(), "unchanged", "report it to the recipe's maintainer: "+r.Maintainer)
			}
			if missing := t.unplanned(d); len(missing) > 0 {
				return fail(r.Name+" needs "+strings.Join(missing, ", ")+", not in the plan", "unchanged; the build is kept in "+filepath.Join(cache, "debs"),
					"report it to the recipe's maintainer ("+r.Maintainer+"): add them to packages")
			}
		}
		fmt.Fprintln(logf, "Built: "+d)
		a.out.Say(ui.Green, "Built:", fmt.Sprintf("%s (%s)", r.Package(), t.versions[r.Package()]))
		debs = append(debs, d)
		a.out.Blank()
	}
	for _, p := range sortedKeys(t.parts) {
		debs = append(debs, t.parts[p])
	}
	if conflicts, err := t.overlaps(debs); err != nil {
		return fail(err.Error(), "unchanged", again)
	} else if len(conflicts) > 0 {
		fmt.Fprintln(logf, "Refused: "+strings.Join(conflicts, "; "))
		a.errs.Refuse(what, "files belong to packages that stay; the recipe must list them in replaces", strings.Join(conflicts, "; "))
		return exitRefused
	}
	if len(debs) > 0 {
		if err := repo.Publish(repo.Dir, debs, a.root); err != nil {
			return fail(err.Error(), "unchanged", again)
		}
	}

	if apt.Interrupted() { // Ctrl-C during the download (8.10)
		fmt.Fprintln(a.out.Out, "Cancelled. Nothing changed.")
		return exitCancelled
	}

	// Journal, capabilities, apt update, one transaction (8.3).
	holding.Store(true)
	defer holding.Store(false)
	a.out.Rule("", strings.ToUpper(t.verb[:1])+t.verb[1:])
	j := &state.Journal{Command: t.verb, Recipes: t.names, Started: time.Now().UTC()}
	for _, x := range t.actions {
		j.Packages = append(j.Packages, state.Package{Name: x.Name, Version: x.Version})
	}
	jp, err := j.Encode(t.tmp)
	if err == nil {
		err = a.root("install", "-m", "644", jp, state.JournalPath(state.Dir))
	}
	if err != nil {
		return fail(err.Error(), "unchanged", again)
	}
	clear := func() { a.root("rm", "-f", state.JournalPath(state.Dir)) }
	versions := map[string]string{}
	for _, x := range t.actions {
		versions[x.Name] = x.Version
		if x.Version == "" {
			versions[x.Name] = x.Old
		}
	}
	update := func() error {
		return apt.Run(logf, a.aptLine(nil, nil), append([]string{"update"}, apt.Options...)...)
	}
	if len(debs) > 0 || len(t.caps) > 0 {
		if err := update(); err != nil {
			clear()
			return fail(err.Error(), "unchanged", "check the network, then: "+again)
		}
	}
	if len(t.caps) > 0 {
		var caps, names []string
		for p := range t.caps {
			caps, names = append(caps, p+"="+t.versions[p]), append(names, p)
		}
		sort.Strings(caps)
		sort.Strings(names)
		if err := apt.Run(logf, a.aptLine(versions, nil), append(append([]string{"install", "-y"}, apt.Options...), caps...)...); err != nil {
			return fail(err.Error(), "unknown: the operation stopped during apt", "sudo dpkg --configure -a")
		}
		// Capabilities come and go with the recipes that need them (5.1).
		if err := mark(append([]string{"auto"}, names...)...); err != nil {
			return fail(err.Error(), "capabilities installed", "sudo apt-mark auto "+strings.Join(names, " "))
		}
		if err := update(); err != nil {
			return fail(err.Error(), "capabilities installed", "check the network, then: "+again)
		}
	}
	var dovy []string
	for p, v := range t.versions {
		if !t.caps[p] && t.parts[p] == "" { // parts come as dependencies, automatic
			dovy = append(dovy, p+"="+v)
		}
	}
	sort.Strings(dovy)
	args := dovy
	for _, r := range t.request {
		if !t.final[pkgName(r)] {
			args = append(args, r)
		}
	}
	skip := map[string]bool{}
	for p := range t.caps {
		skip[p] = true
	}
	for p := range t.final {
		skip[p] = true
	}
	out, err := apt.Simulate(nil, append([]string{"install", "--allow-downgrades"}, args...)...)
	if err != nil || !same(apt.Parse(out), t.actions, skip) {
		clear()
		return fail("apt's plan changed after the build", "unchanged; built packages kept in "+repo.Dir, again)
	}
	if code := a.preconfigure(t, debs, logf); code != exitOK {
		clear()
		return code
	}
	if err := apt.Run(logf, a.aptLine(versions, &t.warnings), append(append([]string{"install", "-y", "--allow-downgrades"}, apt.Options...), args...)...); err != nil {
		return fail(err.Error(), "unknown: the operation stopped during apt", "run dovy again: it offers to recover")
	}
	if apt.Interrupted() {
		return fail("stopped after the apt transaction (Ctrl-C)", "apt transaction complete; later steps not run", "run dovy again: it finishes the plan")
	}

	// dovy's own packages are purged (4.4), sources before the next apt
	// update; the repository drops them.
	var gone []string
	all := map[string]string{}
	for p := range t.removing {
		all[p] = ""
	}
	for p := range t.unused {
		all[p] = ""
	}
	for p := range t.down { // dovy's package returned to Debian's version
		if t.inst[p].Recipe != "" {
			gone = append(gone, p)
		}
	}
	for _, p := range sortedKeys(all) {
		i := t.inst[p]
		if i.Recipe == "" && i.Capability == "" {
			continue
		}
		gone = append(gone, p)
		if configOnly(p) {
			cmd := exec.Command("sudo", "--", "dpkg", "--purge", p)
			cmd.Stdout, cmd.Stderr = logf, logf
			if err := cmd.Run(); err != nil {
				return fail(err.Error(), "removed; config of "+p+" left", "sudo dpkg --purge "+p)
			}
			a.out.Say(ui.Red, "Purging:", p+" ("+i.Version+")")
		}
	}

	// i386 packages, then dovy-i386, whose postrm removes the architecture
	// once none is left (4.8).
	if len(t.final) > 0 {
		var i386 []string
		known, _ := exec.Command("dpkg-query", "-W", "-f", "${binary:Package}\n").Output()
		for _, p := range strings.Fields(string(known)) {
			if t.final[p] && p != capability.I386 {
				i386 = append(i386, p)
			}
		}
		sort.Strings(i386)
		for _, step := range [][]string{i386, {capability.I386}} {
			if len(step) == 0 {
				continue
			}
			pargs := append(append([]string{"purge", "-y", "--allow-remove-essential"}, apt.Options...), step...)
			line := a.aptLine(versions, &t.warnings)
			quiet := func(l string) {
				if !apt.Forced(l) {
					line(l)
				}
			}
			if err := apt.Run(logf, quiet, pargs...); err != nil {
				return fail(err.Error(), "unknown: the operation stopped during apt", "sudo dpkg --configure -a")
			}
		}
		if err := update(); err != nil {
			a.out.Say(ui.Yellow, "Warning:", "apt-get update: "+err.Error())
		}
	}

	// Packages named only to pick a version stay automatic (3.4).
	var auto []string
	for _, r := range t.request {
		n := strings.FieldsFunc(r, func(c rune) bool { return c == '/' || c == '=' })[0]
		if !strings.HasSuffix(r, "-") && !manual[n] {
			auto = append(auto, n)
		}
	}
	marks := map[string][]string{}
	for _, n := range auto {
		if t.restored[n] == "" {
			marks["auto"] = append(marks["auto"], n)
		}
	}
	for _, n := range sortedKeys(t.restored) { // as before the recipe (ADR 0016)
		marks[t.restored[n]] = append(marks[t.restored[n]], n)
	}
	for _, m := range []string{"auto", "manual"} {
		if len(marks[m]) == 0 {
			continue
		}
		if err := mark(append([]string{m}, marks[m]...)...); err != nil {
			a.out.Say(ui.Yellow, "Warning:", "could not mark "+strings.Join(marks[m], ", ")+" "+m)
		}
	}

	if len(gone) > 0 {
		if err := repo.Update(repo.Dir, nil, gone, a.root); err != nil {
			a.out.Say(ui.Yellow, "Warning:", "local repository: "+err.Error())
		}
	}
	clear()
	a.out.Blank()
	if len(t.warnings) > 0 {
		a.out.Rule(ui.Yellow, "Notices")
		for _, w := range t.warnings {
			a.out.Say(ui.Yellow, "Warning:", w)
		}
		a.out.Blank()
	}
	if len(t.sbNotes) > 0 {
		a.out.Pairs(ui.Yellow, "Notices", t.sbNotes)
		a.out.Blank()
	}
	a.notices(t.notes, versions)
	if t.leftover != "" {
		a.out.Rule(ui.Yellow, "Notices")
		fmt.Fprintln(a.out.Out, "  "+t.leftover)
		a.out.Blank()
	}
	a.showNotices(t.notices)
	verb := map[string]string{"install": "installed", "remove": "removed", "upgrade": "upgraded"}[t.verb]
	if len(t.names) == 0 {
		t.names = []string{"Debian packages"}
	}
	fmt.Fprintln(a.out.Out, a.out.Paint(ui.Green, "Done.")+" "+strings.Join(t.names, ", ")+" "+verb+".")
	return exitOK
}

// overlaps checks the files of every package from outside Debian against
// installed packages that stay (ADR 0016): dovy's builds, and vendor
// packages apt just downloaded.
func (t *txn) overlaps(built []string) ([]string, error) {
	paths := append([]string{}, built...)
	leaving := map[string]bool{}
	for _, x := range t.actions {
		switch {
		case x.Op == "Remv" || x.Op == "Purg":
			leaving[strings.SplitN(x.Name, ":", 2)[0]] = true
		case x.Op == "Inst" && t.versions[x.Name] == "" && x.Origin != "Backports" && !strings.HasPrefix(x.Origin, "Debian"):
			paths = append(paths, archived(x)...)
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	owners, err := overlap.Owners("/var/lib/dpkg/info")
	if err != nil {
		return nil, err
	}
	var debs []*overlap.Deb
	for _, p := range paths {
		d, err := overlap.Read(p)
		if err != nil {
			return nil, err
		}
		debs = append(debs, d)
	}
	var out []string
	for _, c := range overlap.Check(debs, owners, leaving, overlap.Diverted()) {
		out = append(out, c.String())
	}
	return out, nil
}

// unplanned lists what a built package depends on that is neither installed
// nor in the plan: the recipe's packages missed it, so apt's plan would
// change after confirmation.
func (t *txn) unplanned(deb string) []string {
	out, err := exec.Command("dpkg-deb", "-f", deb, "Depends", "Pre-Depends").Output()
	if err != nil {
		return []string{"(unreadable Depends: " + err.Error() + ")"}
	}
	have := map[string]bool{}
	for p := range t.inst {
		have[strings.SplitN(p, ":", 2)[0]] = true
	}
	for _, x := range t.actions {
		if x.Op == "Inst" {
			have[strings.SplitN(x.Name, ":", 2)[0]] = true
		}
	}
	var missing []string
	for _, l := range strings.Split(string(out), "\n") {
		_, v, _ := strings.Cut(l, ":")
		for _, rel := range strings.Split(v, ",") {
			ok := false
			for _, alt := range strings.Split(rel, "|") {
				if f := strings.Fields(alt); len(f) > 0 && have[strings.SplitN(f[0], ":", 2)[0]] {
					ok = true
				}
			}
			if f := strings.Fields(rel); !ok && len(f) > 0 {
				missing = append(missing, f[0])
			}
		}
	}
	return missing
}

// archived is the .deb apt downloaded for x.
func archived(x apt.Action) []string {
	name, arch, _ := strings.Cut(x.Name, ":")
	if arch == "" {
		arch = "*"
	}
	v := strings.ReplaceAll(x.Version, ":", "%3a")
	m, _ := filepath.Glob(filepath.Join("/var/cache/apt/archives", name+"_"+v+"_"+arch+".deb"))
	return m
}

// preconfigure asks packages' questions before the install, on the terminal
// (8.11): dpkg-preconfigure on the exact .debs. The install then runs
// non-interactively with the answers.
func (a *app) preconfigure(t *txn, built []string, logf *os.File) int {
	debs := append([]string{}, built...)
	for _, x := range t.actions {
		if x.Op != "Inst" || t.versions[x.Name] != "" {
			continue
		}
		debs = append(debs, archived(x)...)
	}
	if len(debs) == 0 {
		return exitOK
	}
	cmd := exec.Command("sudo", append([]string{"--", "dpkg-preconfigure"}, debs...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, logf
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(logf, "dpkg-preconfigure: "+err.Error())
		t.warnings = append(t.warnings, "package questions: "+err.Error()+"; defaults used")
	}
	return exitOK
}
