// Package catalog reads, checks and verifies the recipe catalog
// (dovy-design/specs/recipe.md, specs/catalog.md).
package catalog

import (
	"errors"
	"fmt"
	"os"
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

type Repository struct {
	URI         string   `toml:"uri"`
	Suite       string   `toml:"suite"`
	Components  []string `toml:"components"`
	KeyURL      string   `toml:"key-url"`
	Fingerprint string   `toml:"fingerprint"`
	Packages    []string `toml:"packages"`
}

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
	Network     string   `toml:"network"`
	Successor   string   `toml:"successor"`
	UserNotes   string   `toml:"user-notes"`

	Sources       []Source          `toml:"source"`
	Install       map[string]string `toml:"install"`
	Scripts       map[string]string `toml:"scripts"`
	Repository    *Repository       `toml:"repository"`
	SourcePackage string            `toml:"source-package"`
	Packages      []string          `toml:"packages"`
	Backports     bool              `toml:"backports"`
	Files         []File            `toml:"file"`
	Members       []string          `toml:"members"`

	HasBuild bool `toml:"-"` // build.sh present
}

// Package is the name of the .deb dovy builds for r: software keeps its
// upstream name; setup, repository and bundle packages are dovy-<name>.
func (r *Recipe) Package() string {
	switch r.Kind {
	case UpstreamBuild, UpstreamBinary, TestingRebuild:
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

var scriptNames = map[string]bool{"preinst": true, "postinst": true, "prerm": true, "postrm": true}

var (
	pkgRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*(:i386)?$`)
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*$`)
	hexRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fprRe  = regexp.MustCompile(`^[0-9A-F]{40}$`)
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
	for _, name := range sorted(rs) {
		for _, e := range check(rs[name]) {
			errs = append(errs, fmt.Errorf("%s: %s", name, e))
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
	_, err = os.Stat(filepath.Join(filepath.Dir(path), "build.sh"))
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
	if len(r.Scripts) > 0 && (len(r.Sources) != 1 || !strings.HasSuffix(r.Sources[0].URL, ".deb")) {
		add("scripts: only for one vendor .deb source")
	}
	for _, f := range r.Files {
		if !dropIn(f.Path) {
			add("file %s: only drop-ins under /etc/**/*.d/", f.Path)
		}
	}
	pkgs := append([]string{}, r.Packages...)
	if r.Repository != nil {
		pkgs = append(pkgs, r.Repository.Packages...)
	}
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
		"repository":     r.Repository != nil,
		"source-package": r.SourcePackage != "",
		"packages":       len(r.Packages) > 0,
		"backports":      r.Backports,
		"file":           len(r.Files) > 0,
		"members":        len(r.Members) > 0,
		"build.sh":       r.HasBuild,
	}
	allowed := map[string][]string{
		UpstreamBuild:    {"source", "build.sh"},
		UpstreamBinary:   {"source", "install", "scripts"},
		VendorRepository: {"repository"},
		TestingRebuild:   {"source-package", "build.sh"},
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
	case UpstreamBinary:
		if !has["source"] {
			add("upstream-binary needs [[source]]")
		}
	case VendorRepository:
		if q := r.Repository; q != nil {
			if !strings.HasPrefix(q.URI, "https://") || q.Suite == "" || len(q.Components) == 0 || !strings.HasPrefix(q.KeyURL, "https://") || len(q.Packages) == 0 {
				add("repository: uri and key-url (https), suite, components, packages")
			}
			if !fprRe.MatchString(q.Fingerprint) {
				add("repository: fingerprint must be 40 uppercase hex characters")
			}
		} else {
			add("vendor-repository needs [repository]")
		}
	case TestingRebuild:
		if !has["source-package"] {
			add("testing-rebuild needs source-package")
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
