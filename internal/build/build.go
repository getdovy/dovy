// Package build turns a recipe into a .deb in dovy's cache.
package build

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/fetch"
	"github.com/getdovy/dovy/internal/sandbox"
	"github.com/getdovy/dovy/internal/unpack"
)

// CacheDir is $XDG_CACHE_HOME/dovy.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "dovy"), nil
}

// Recipe downloads, unpacks and packs r for a Debian release; it returns
// the .deb's path under cache/debs.
func Recipe(r *catalog.Recipe, release int, cache string) (string, error) {
	work := filepath.Join(cache, "build", r.Name)
	if err := sandbox.Remove(work); err != nil {
		return "", err
	}
	defer sandbox.Remove(work)
	switch {
	case IsDeb(r) && len(r.Sources) > 1:
		return "", fmt.Errorf("%s: several packages: use Parts", r.Name)
	case IsDeb(r):
		return vendor(r, release, cache, work)
	case r.Kind == catalog.UpstreamBuild:
		return upstream(r, release, cache, work)
	case r.Kind == catalog.TestingRebuild:
		if err := os.MkdirAll(work, 0o755); err != nil {
			return "", err
		}
		return rebuild(r, release, cache, work)
	}
	src, stage := filepath.Join(work, "src"), filepath.Join(work, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", err
	}
	for _, s := range r.Sources {
		p, err := fetch.Get(s.URL, s.SHA256, filepath.Join(cache, "sources"))
		if err != nil {
			return "", err
		}
		if err := unpack.Archive(p, s.URL, src); err != nil {
			return "", fmt.Errorf("%s: %w", s.URL, err)
		}
	}
	if err := deb.Stage(r, src, stage); err != nil {
		return "", fmt.Errorf("%s: %w", r.Name, err)
	}
	return deb.Build(r, deb.Version(r, release), stage, filepath.Join(cache, "debs"))
}

// IsDeb reports a vendor .deb recipe: every source a .deb.
func IsDeb(r *catalog.Recipe) bool {
	if r.Kind != catalog.UpstreamBinary || len(r.Sources) == 0 {
		return false
	}
	for _, s := range r.Sources {
		if !strings.HasSuffix(s.URL, ".deb") {
			return false
		}
	}
	return true
}

// Part is one repacked vendor package of a recipe with several (ADR 0018).
type Part struct{ Package, Version, Path string }

// Parts repacks each .deb source of r under its own name, then builds r's
// own package depending on each at its exact version (ADR 0018). It
// returns the parts and r's package.
func Parts(r *catalog.Recipe, release int, cache string) ([]Part, string, error) {
	work := filepath.Join(cache, "build", r.Name)
	if err := os.RemoveAll(work); err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(work)
	version := deb.Version(r, release)
	var parts []Part
	var depends []string
	seen := map[string]bool{}
	for i, s := range r.Sources {
		p, err := fetch.Get(s.URL, s.SHA256, filepath.Join(cache, "sources"))
		if err != nil {
			return nil, "", err
		}
		d, err := repack(r, p, "", version, cache, filepath.Join(work, fmt.Sprint(i)))
		if err != nil {
			return nil, "", err
		}
		out, err := exec.Command("dpkg-deb", "-f", d, "Package").Output()
		if err != nil {
			return nil, "", err
		}
		name := strings.TrimSpace(string(out))
		if seen[name] || name == r.Package() {
			return nil, "", fmt.Errorf("%s: package %s given twice", r.Name, name)
		}
		seen[name] = true
		parts = append(parts, Part{name, version, d})
		depends = append(depends, name+" (= "+version+")")
	}
	stage := filepath.Join(work, "meta")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, "", err
	}
	meta, err := deb.BuildWith(r, version, stage, filepath.Join(cache, "debs"), depends)
	return parts, meta, err
}

// vendor repacks a vendor .deb (principles 6.2, 2.6): dovy's version and
// X-Dovy-Recipe (3.3, ADR 0010), the recipe's scripts in place of the
// vendor's where it gives them (2.7).
func vendor(r *catalog.Recipe, release int, cache, work string) (string, error) {
	s := r.Sources[0]
	p, err := fetch.Get(s.URL, s.SHA256, filepath.Join(cache, "sources"))
	if err != nil {
		return "", err
	}
	return repack(r, p, r.Package(), deb.Version(r, release), cache, work)
}

// Repack repacks a vendor .deb downloaded from a repacked repository
// (ADR 0014) as version, into cache/debs.
func Repack(r *catalog.Recipe, path, version, cache string) (string, error) {
	work := filepath.Join(cache, "build", r.Name)
	if err := os.RemoveAll(work); err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	return repack(r, path, r.Package(), version, cache, work)
}

// repack restamps a vendor .deb as dovy's; want is the package name it
// must have, or "" for any (a part, ADR 0018).
func repack(r *catalog.Recipe, p, want, version, cache, work string) (string, error) {
	raw := filepath.Join(work, "raw")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}
	if b, err := exec.Command("dpkg-deb", "-R", p, raw).CombinedOutput(); err != nil {
		return "", fmt.Errorf("dpkg-deb -R: %s", strings.TrimSpace(string(b)))
	}
	ctl := filepath.Join(raw, "DEBIAN", "control")
	b, err := os.ReadFile(ctl)
	if err != nil {
		return "", err
	}
	pkg := want
	if pkg == "" {
		pkg = field(string(b), "Package")
	}
	fields, err := Restamp(string(b), pkg, version, r.Name)
	if err != nil {
		return "", fmt.Errorf("%s: %w", r.Name, err)
	}
	if want != "" {
		// What the vendor's Depends miss, the recipe's packages add.
		switch {
		case IsDeb(r):
			fields = merge(fields, "Depends", r.Packages)
		case r.Repo != nil && r.Repo.Repack && len(r.Packages) > 1: // the first is its own
			fields = merge(fields, "Depends", r.Packages[1:])
		}
		fields = merge(fields, "Conflicts", deb.Conflicts(r)) + deb.ReplacedField(r)
	}
	if r.Release != "" {
		fields += "X-Dovy-Release: " + r.Release + "\n"
	}
	if want != "" {
		if err := writePin(r, raw); err != nil {
			return "", err
		}
		if err := writeDefaults(r, raw); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(ctl, []byte(fields), 0o644); err != nil {
		return "", err
	}
	if err := tidyConffiles(raw); err != nil {
		return "", err
	}
	for name, body := range r.Scripts {
		path := filepath.Join(raw, "DEBIAN", name)
		if body == "" {
			err = os.Remove(path)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		} else {
			err = os.WriteFile(path, []byte(body), 0o755)
		}
		if err != nil {
			return "", err
		}
	}
	arch := field(fields, "Architecture")
	out := filepath.Join(cache, "debs")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	d := filepath.Join(out, fmt.Sprintf("%s_%s_%s.deb", pkg, version, arch))
	if b, err := exec.Command("dpkg-deb", "--root-owner-group", "-b", raw, d).CombinedOutput(); err != nil {
		return "", fmt.Errorf("dpkg-deb -b: %s", strings.TrimSpace(string(b)))
	}
	return d, nil
}

// Restamp sets Version and X-Dovy-Recipe in a vendor control file; the
// package must be the one the recipe names.
func Restamp(control, pkg, version, recipe string) (string, error) {
	if got := field(control, "Package"); got != pkg {
		return "", fmt.Errorf("vendor package is %q, recipe expects %q", got, pkg)
	}
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(control, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "Version:"):
			l = "Version: " + version
		case strings.HasPrefix(l, "X-Dovy-Recipe:"):
			continue
		}
		b.WriteString(l + "\n")
	}
	b.WriteString("X-Dovy-Recipe: " + recipe + "\n")
	return b.String(), nil
}

// writePin adds the same-name replacement pin (7.3) under root.
func writePin(r *catalog.Recipe, root string) error {
	path, content := r.PinFile()
	if path == "" {
		return nil
	}
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// writeDefaults adds the recipe's /etc/default files, which configure the
// vendor's own scripts (2.6), as conffiles.
func writeDefaults(r *catalog.Recipe, raw string) error {
	if len(r.Files) == 0 {
		return nil
	}
	cf := filepath.Join(raw, "DEBIAN", "conffiles")
	old, _ := os.ReadFile(cf)
	list := string(old)
	for _, f := range r.Files {
		p := filepath.Join(raw, f.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(strings.TrimPrefix(f.Content, "\n")), 0o644); err != nil {
			return err
		}
		if !strings.Contains("\n"+list, "\n"+f.Path+"\n") {
			list += f.Path + "\n"
		}
	}
	return os.WriteFile(cf, []byte(list), 0o644)
}

// tidyConffiles drops blank lines from a vendor's conffiles, which
// dpkg-deb -b refuses, and the file when nothing is left.
func tidyConffiles(raw string) error {
	cf := filepath.Join(raw, "DEBIAN", "conffiles")
	b, err := os.ReadFile(cf)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var list string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			list += l + "\n"
		}
	}
	if list == "" {
		return os.Remove(cf)
	}
	return os.WriteFile(cf, []byte(list), 0o644)
}

// merge adds names to a control file's relation field.
func merge(control, name string, names []string) string {
	if len(names) == 0 {
		return control
	}
	var b strings.Builder
	done := false
	for _, l := range strings.Split(strings.TrimRight(control, "\n"), "\n") {
		if v, ok := strings.CutPrefix(l, name+":"); ok {
			// In front: the field may go on over the next lines.
			l = name + ": " + strings.Join(names, ", ")
			if v = strings.TrimSpace(v); v != "" {
				l += ", " + v
			} else {
				l += ","
			}
			done = true
		}
		b.WriteString(l + "\n")
	}
	if !done {
		b.WriteString(name + ": " + strings.Join(names, ", ") + "\n")
	}
	return b.String()
}

func field(control, name string) string {
	for _, l := range strings.Split(control, "\n") {
		if v, ok := strings.CutPrefix(l, name+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
