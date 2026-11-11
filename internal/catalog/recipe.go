// Package catalog reads, checks and verifies the recipe catalog
// (dovy-design/specs/recipe.md, specs/catalog.md).
package catalog

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Kinds.
const (
	UpstreamBuild    = "upstream-build"
	UpstreamBinary   = "upstream-binary"
	VendorRepository = "vendor-repository"
	TestingRebuild   = "testing-rebuild"
	Setup            = "setup"
	Bundle           = "bundle"
)

// Releases dovy supports, capabilities, and hardware checks built in.
var (
	releases     = map[int]bool{13: true}
	capabilities = map[string]bool{"i386": true, "components": true, "backports": true}
	hardware     = map[string]bool{"nvidia-turing": true}
)

type Source struct {
	URL    string `toml:"url"`
	SHA256 string `toml:"sha256"`
}

// Repository is one repositories/<name>.toml: a vendor's apt repository,
// shared by the recipes that name it (ADR 0013).
type Repository struct {
	Name         string   `toml:"name"`
	URI          string   `toml:"uri"`
	Suite        string   `toml:"suite"`
	Components   []string `toml:"components"`
	KeyURL       string   `toml:"key-url"`
	Fingerprints []string `toml:"fingerprints"`
	Maintainer   string   `toml:"maintainer"`
	Repack       bool     `toml:"repack"` // never given to apt; packages repacked (ADR 0014)
	Pin          *bool    `toml:"pin"`    // false: not pinned (NVIDIA, ADR 0017)
	Files        []File   `toml:"file"`
}

// Pinned reports whether the repository is pinned to its recipes' packages.
func (q *Repository) Pinned() bool { return q.Pin == nil || *q.Pin }

// Flat reports a flat repository: the suite is a path, with no components.
func (q *Repository) Flat() bool { return strings.HasSuffix(q.Suite, "/") }

type File struct {
	Path    string `toml:"path"`
	Content string `toml:"content"`
}

// Recipe is one recipes/<name>/recipe.toml.
type Recipe struct {
	Name        string   `toml:"name"`
	Description string   `toml:"description"`
	Kind        string   `toml:"kind"`
	Version     string   `toml:"version"`
	Revision    int      `toml:"revision"`
	Maintainer  string   `toml:"maintainer"`
	Debian      []int    `toml:"debian"`
	Requires    []string `toml:"requires"`
	Depends     []string `toml:"depends"`
	Conflicts   []string `toml:"conflicts"`
	Replaces    []string `toml:"replaces"`
	Hardware    string   `toml:"hardware"`
	SecureBoot  string   `toml:"secure-boot"`
	Network     string   `toml:"network"`
	Successor   string   `toml:"successor"`
	UserNotes   string   `toml:"user-notes"`

	Sources       []Source          `toml:"source"`
	Install       map[string]string `toml:"install"`
	Scripts       map[string]string `toml:"scripts"`
	Repository    string            `toml:"repository"`
	Repo          *Repository       `toml:"-"` // the repository it names
	SourcePackage string            `toml:"source-package"`
	Packages      []string          `toml:"packages"`
	Backports     bool              `toml:"backports"`
	Files         []File            `toml:"file"`
	Members       []string          `toml:"members"`
	BuildDepends  []string          `toml:"build-depends"`

	HasBuild   bool     `toml:"-"` // build.sh present
	Dir        string   `toml:"-"` // the recipe's folder
	Replaced   []string `toml:"-"` // set at install: "pkg manual|auto" (ADR 0016)
	Release    string   `toml:"-"` // catalog release it is built from
	Recommends []string `toml:"-"` // a bundle's member packages
}

// PinFile is the pin that keeps a same-name replacement on dovy's version
// (principles 7.3), or "" when r replaces no package of its own name.
func (r *Recipe) PinFile() (path, content string) {
	for _, p := range r.Replaces {
		if p == r.Package() {
			return "/etc/apt/preferences.d/dovy-" + r.Name + ".pref",
				"Package: " + p + "\nPin: release l=dovy\nPin-Priority: 1001\n"
		}
	}
	return "", ""
}

// Package is the name of the .deb dovy builds for r: software keeps its
// upstream name; setup, repository and bundle packages are dovy-<name>.
func (r *Recipe) Package() string {
	switch r.Kind {
	case UpstreamBuild, UpstreamBinary, TestingRebuild:
		return r.Name
	case VendorRepository:
		if r.Repo != nil && r.Repo.Repack { // the vendor's package, repacked
			return r.Name
		}
	}
	if strings.HasPrefix(r.Name, "dovy-") { // dovy-defaults
		return r.Name
	}
	return "dovy-" + r.Name
}

// Rev is the package revision (ADR 0010).
func (r *Recipe) Rev() int { return max(r.Revision, 1) }

// Needs is the capabilities r requires, stated or implied: backports by
// `backports = true`, i386 by any :i386 package.
func (r *Recipe) Needs() []string {
	need := map[string]bool{}
	for _, c := range r.Requires {
		need[c] = true
	}
	if r.Backports {
		need["backports"] = true
	}
	for _, p := range r.Packages {
		if strings.HasSuffix(p, ":i386") {
			need["i386"] = true
		}
	}
	return sorted(need)
}

// scriptNames a recipe may replace; config and templates may only be
// dropped, with their package's debconf questions.
var scriptNames = map[string]bool{"preinst": true, "postinst": true, "prerm": true, "postrm": true, "config": true, "templates": true}

var (
	pkgRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*(:i386)?$`)
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*$`)
	hexRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fprRe  = regexp.MustCompile(`^([0-9A-F]{40}|[0-9A-F]{64})$`)
)

// DebianHas reports which of names are Debian stable packages.
type DebianHas func(names []string) (map[string]bool, error)

// Load reads and checks every recipe under dir/recipes. Any problem refuses
// the whole catalog; the error lists all of them.
func Load(dir string, debian DebianHas) (map[string]*Recipe, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "recipes", "*", "recipe.toml"))
	if err != nil {
		return nil, err
	}
	rs := map[string]*Recipe{}
	var errs []error
	for _, p := range paths {
		folder := filepath.Base(filepath.Dir(p))
		r, err := parse(p)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", folder, err))
			continue
		}
		if r.Name != folder {
			errs = append(errs, fmt.Errorf("%s: name %q does not match the folder", folder, r.Name))
			continue
		}
		rs[r.Name] = r
	}
	repos, rerrs := loadRepositories(dir)
	errs = append(errs, rerrs...)
	for _, name := range sorted(rs) {
		for _, e := range check(rs[name]) {
			errs = append(errs, fmt.Errorf("%s: %s", name, e))
		}
		if r := rs[name]; r.Repository != "" {
			switch r.Repo = repos[r.Repository]; {
			case r.Repo == nil:
				errs = append(errs, fmt.Errorf("%s: no repository %q", name, r.Repository))
			case r.Repo.Repack && len(r.Packages) != 1:
				errs = append(errs, fmt.Errorf("%s: a repacked repository's recipe names one package, its own", name))
			case r.Repo.Repack && r.Packages[0] != r.Name:
				errs = append(errs, fmt.Errorf("%s: repacked package %q must be the recipe's name", name, r.Packages[0]))
			case !r.Repo.Repack && len(r.Scripts) > 0:
				errs = append(errs, fmt.Errorf("%s: scripts: only for a repacked repository (ADR 0014)", name))
			}
		}
	}
	errs = append(errs, crossCheck(rs)...)
	if len(errs) == 0 && debian != nil {
		errs = append(errs, checkReplaces(rs, debian)...)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return rs, nil
}

func parse(path string) (*Recipe, error) {
	r := &Recipe{}
	md, err := toml.DecodeFile(path, r)
	if err != nil {
		return nil, err
	}
	if u := md.Undecoded(); len(u) > 0 {
		keys := make([]string, len(u))
		for i, k := range u {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("unknown fields: %s", strings.Join(keys, ", "))
	}
	r.Dir = filepath.Dir(path)
	_, err = os.Stat(filepath.Join(r.Dir, "build.sh"))
	r.HasBuild = err == nil
	return r, nil
}

// check runs the checks that need only the recipe itself.
func check(r *Recipe) []string {
	var p []string
	add := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }

	if !nameRe.MatchString(r.Name) {
		add("name must match %s", nameRe)
	}
	if r.Description == "" || strings.Contains(r.Description, "\n") || strings.HasSuffix(r.Description, ".") {
		add("description: one line, no final period")
	}
	if r.Version == "" || strings.EqualFold(r.Version, "latest") {
		add("version: an exact version")
	}
	if r.Revision < 0 {
		add("revision: 1 or more")
	}
	if r.Maintainer == "" {
		add("maintainer missing")
	}
	if len(r.Debian) == 0 {
		add("debian: releases missing")
	}
	for _, d := range r.Debian {
		if !releases[d] {
			add("debian: %d not supported", d)
		}
	}
	for _, c := range r.Requires {
		if !capabilities[c] {
			add("requires: unknown capability %q", c)
		}
	}
	if r.Hardware != "" && !hardware[r.Hardware] {
		add("hardware: unknown check %q", r.Hardware)
	}
	if r.SecureBoot != "" && r.SecureBoot != "dkms" && r.SecureBoot != "unsigned-kernel" {
		add("secure-boot: dkms or unsigned-kernel")
	}
	for _, s := range r.Sources {
		if !strings.HasPrefix(s.URL, "https://") {
			add("source %s: https only", s.URL)
		}
		if !hexRe.MatchString(s.SHA256) {
			add("source %s: sha256 must be 64 lowercase hex characters", s.URL)
		}
	}
	for _, dst := range sorted(r.Install) {
		src := r.Install[dst]
		if !strings.HasPrefix(dst, "/usr/") || filepath.Clean(dst) != dst {
			add("install %s: an absolute path under /usr/", dst)
		}
		if src == "" || filepath.IsAbs(src) || filepath.Clean(src) != src || strings.HasPrefix(src, "..") {
			add("install %s: source %q must be relative, without ..", dst, src)
		}
	}
	for _, k := range sorted(r.Scripts) {
		if !scriptNames[k] {
			add("scripts: unknown script %q", k)
		}
	}
	for _, k := range []string{"config", "templates"} {
		if v, ok := r.Scripts[k]; ok && v != "" {
			add("scripts: %s can only be dropped (\"\")", k)
		}
	}
	if len(r.Scripts) > 0 && r.Kind == UpstreamBinary && (len(r.Sources) != 1 || !strings.HasSuffix(r.Sources[0].URL, ".deb")) {
		add("scripts: only for one vendor .deb source")
	}
	for _, f := range r.Files {
		if !dropIn(f.Path) {
			add("file %s: only drop-ins under /etc/**/*.d/", f.Path)
		}
	}
	pkgs := append(append(append([]string{}, r.Packages...), r.BuildDepends...), r.Replaces...)
	for _, n := range pkgs {
		if !pkgRe.MatchString(n) {
			add("package %q: an exact package name", n)
		}
	}

	// What each kind needs, and what it may not carry.
	has := map[string]bool{
		"source":         len(r.Sources) > 0,
		"install":        len(r.Install) > 0,
		"scripts":        len(r.Scripts) > 0,
		"repository":     r.Repository != "",
		"source-package": r.SourcePackage != "",
		"packages":       len(r.Packages) > 0,
		"backports":      r.Backports,
		"file":           len(r.Files) > 0,
		"members":        len(r.Members) > 0,
		"build.sh":       r.HasBuild,
		"build-depends":  len(r.BuildDepends) > 0,
	}
	allowed := map[string][]string{
		UpstreamBuild:    {"source", "build.sh", "build-depends", "packages"},
		UpstreamBinary:   {"source", "install", "scripts"},
		VendorRepository: {"repository", "packages", "scripts"},
		TestingRebuild:   {"source-package", "source", "build.sh", "packages"},
		Setup:            {"packages", "backports", "file"},
		Bundle:           {"members"},
	}
	ok, known := allowed[r.Kind]
	if !known {
		add("kind: unknown %q", r.Kind)
		return p
	}
	for _, f := range sorted(has) {
		if has[f] && !contains(ok, f) {
			add("%s: not used by kind %s", f, r.Kind)
		}
	}
	switch r.Kind {
	case UpstreamBuild:
		if !has["source"] || !has["build.sh"] {
			add("upstream-build needs [[source]] and build.sh")
		}
		for _, s := range r.Sources {
			if !strings.HasSuffix(s.URL, ".tar.gz") {
				add("source %s: .tar.gz only", s.URL)
			}
		}
	case UpstreamBinary:
		if !has["source"] {
			add("upstream-binary needs [[source]]")
		}
	case VendorRepository:
		if r.Repository == "" || len(r.Packages) == 0 {
			add("vendor-repository needs repository and packages")
		}
	case TestingRebuild:
		if !has["source-package"] {
			add("testing-rebuild needs source-package")
		}
		if len(r.Sources) != 1 || !strings.HasSuffix(r.Sources[0].URL, ".dsc") {
			add("testing-rebuild needs one [[source]]: the .dsc")
		} else if b := path.Base(r.Sources[0].URL); !strings.HasPrefix(b, r.SourcePackage+"_"+r.Version+"-") {
			add("source %s: expected %s_%s-<debian revision>.dsc", b, r.SourcePackage, r.Version)
		}
	case Setup:
		if !has["packages"] && !has["file"] {
			add("setup needs packages or [[file]]")
		}
		if r.Backports && !has["packages"] {
			add("backports needs packages")
		}
	case Bundle:
		if !has["members"] {
			add("bundle needs members")
		}
	}
	return p
}

// dropIn: an absolute path under /etc inside a *.d folder, no "..".
func dropIn(p string) bool {
	if !strings.HasPrefix(p, "/etc/") || filepath.Clean(p) != p {
		return false
	}
	// tuned's override: a profile here replaces Debian's of the same name.
	if m, _ := filepath.Match("/etc/tuned/profiles/*/tuned.conf", p); m {
		return true
	}
	parts := strings.Split(p, "/")
	for _, d := range parts[2 : len(parts)-1] {
		if strings.HasSuffix(d, ".d") {
			return true
		}
	}
	return false
}

// crossCheck: references exist, conflicts go both ways, no dependency cycles.
func crossCheck(rs map[string]*Recipe) []error {
	var errs []error
	ref := func(from, field, to string) {
		if rs[to] == nil {
			errs = append(errs, fmt.Errorf("%s: %s: no recipe %q", from, field, to))
		}
	}
	for _, name := range sorted(rs) {
		r := rs[name]
		for _, d := range r.Depends {
			ref(name, "depends", d)
		}
		for _, m := range r.Members {
			ref(name, "members", m)
		}
		if r.Successor != "" {
			ref(name, "successor", r.Successor)
		}
		for _, c := range r.Conflicts {
			ref(name, "conflicts", c)
			if o := rs[c]; o != nil && !contains(o.Conflicts, name) {
				errs = append(errs, fmt.Errorf("%s: conflicts with %s, but %s does not list %s", name, c, c, name))
			}
		}
	}
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(n string, path []string)
	visit = func(n string, path []string) {
		switch state[n] {
		case 1:
			errs = append(errs, fmt.Errorf("dependency cycle: %s", strings.Join(append(path, n), " → ")))
			return
		case 2:
			return
		}
		state[n] = 1
		if r := rs[n]; r != nil {
			for _, d := range append(append([]string{}, r.Depends...), r.Members...) {
				visit(d, append(path, n))
			}
		}
		state[n] = 2
	}
	for _, name := range sorted(rs) {
		visit(name, nil)
	}
	return errs
}

// checkReplaces: a package name Debian already uses must be in replaces.
func checkReplaces(rs map[string]*Recipe, debian DebianHas) []error {
	var names []string
	for _, n := range sorted(rs) {
		if p := rs[n].Package(); p == n {
			names = append(names, p)
		}
	}
	in, err := debian(names)
	if err != nil {
		return []error{fmt.Errorf("asking apt for Debian's packages: %w", err)}
	}
	var errs []error
	for _, n := range names {
		if in[n] && !contains(rs[n].Replaces, n) {
			errs = append(errs, fmt.Errorf("%s: Debian has a package %s; list it in replaces", n, n))
		}
	}
	return errs
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func sorted[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

// loadRepositories reads and checks repositories/*.toml (ADR 0013).
func loadRepositories(dir string) (map[string]*Repository, []error) {
	paths, _ := filepath.Glob(filepath.Join(dir, "repositories", "*.toml"))
	repos := map[string]*Repository{}
	hosts := map[string]string{} // one repository per host (5.10)
	var errs []error
	for _, p := range paths {
		base := strings.TrimSuffix(filepath.Base(p), ".toml")
		q := &Repository{}
		md, err := toml.DecodeFile(p, q)
		if err == nil {
			if u := md.Undecoded(); len(u) > 0 {
				err = fmt.Errorf("unknown fields: %v", u)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("repository %s: %w", base, err))
			continue
		}
		var bad []string
		add := func(f string, a ...any) { bad = append(bad, fmt.Sprintf(f, a...)) }
		if q.Name != base {
			add("name %q does not match the file", q.Name)
		}
		u, err := url.Parse(q.URI)
		if err != nil || u.Scheme != "https" || u.Host == "" || !strings.HasPrefix(q.KeyURL, "https://") {
			add("uri and key-url: https")
		}
		if q.Suite == "" || q.Maintainer == "" {
			add("suite and maintainer required")
		}
		if q.Flat() != (len(q.Components) == 0) {
			add("components: none for a flat suite (ending in /), else at least one")
		}
		if len(q.Fingerprints) == 0 {
			add("fingerprints required")
		}
		if q.Pin != nil && *q.Pin {
			add("pin: only false; repositories are pinned by default")
		}
		if !q.Pinned() && q.Repack {
			add("pin = false and repack: one or the other")
		}
		for _, f := range q.Fingerprints {
			if !fprRe.MatchString(f) {
				add("fingerprint %s: 40 or 64 uppercase hex characters", f)
			}
		}
		for _, f := range q.Files {
			// Only to configure the vendor's own scripts (2.6).
			if d, name := filepath.Split(f.Path); d != "/etc/default/" || !nameRe.MatchString(name) {
				add("file %s: only /etc/default/<name>", f.Path)
			}
		}
		if u != nil && u.Host != "" {
			if o, ok := hosts[u.Host]; ok {
				add("%s already has a repository: %s", u.Host, o)
			}
			hosts[u.Host] = base
		}
		for _, b := range bad {
			errs = append(errs, fmt.Errorf("repository %s: %s", base, b))
		}
		repos[base] = q
	}
	return repos, errs
}
