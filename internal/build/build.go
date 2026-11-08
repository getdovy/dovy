// Package build turns a recipe into a .deb in dovy's cache.
package build

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/fetch"
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

// Buildable reports whether this dovy builds r's kind.
func Buildable(r *catalog.Recipe) bool {
	switch r.Kind {
	case catalog.UpstreamBinary, catalog.Setup, catalog.VendorRepository, capability.Kind:
		return true
	}
	return false
}

// Recipe downloads, unpacks and packs r for a Debian release; it returns
// the .deb's path under cache/debs.
func Recipe(r *catalog.Recipe, release int, cache string) (string, error) {
	if !Buildable(r) {
		return "", fmt.Errorf("%s: kind %s is not built yet", r.Name, r.Kind)
	}
	work := filepath.Join(cache, "build", r.Name)
	if err := os.RemoveAll(work); err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	if IsDeb(r) {
		return vendor(r, release, cache, work)
	}
	src, stage := filepath.Join(work, "src"), filepath.Join(work, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", err
	}
	for _, s := range r.Sources {
		if !strings.HasSuffix(s.URL, ".tar.gz") {
			return "", fmt.Errorf("%s: only .tar.gz sources for now", s.URL)
		}
		p, err := fetch.Get(s.URL, s.SHA256, filepath.Join(cache, "sources"))
		if err != nil {
			return "", err
		}
		if err := unpack.TarGz(p, src); err != nil {
			return "", fmt.Errorf("%s: %w", s.URL, err)
		}
	}
	if err := deb.Stage(r, src, stage); err != nil {
		return "", fmt.Errorf("%s: %w", r.Name, err)
	}
	return deb.Build(r, deb.Version(r, release), stage, filepath.Join(cache, "debs"))
}

// IsDeb reports a vendor .deb recipe.
func IsDeb(r *catalog.Recipe) bool {
	return r.Kind == catalog.UpstreamBinary && len(r.Sources) == 1 && strings.HasSuffix(r.Sources[0].URL, ".deb")
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
	version := deb.Version(r, release)
	fields, err := Restamp(string(b), r.Package(), version, r.Name)
	if err != nil {
		return "", fmt.Errorf("%s: %w", r.Name, err)
	}
	if err := os.WriteFile(ctl, []byte(fields), 0o644); err != nil {
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
	d := filepath.Join(out, fmt.Sprintf("%s_%s_%s.deb", r.Package(), version, arch))
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

func field(control, name string) string {
	for _, l := range strings.Split(control, "\n") {
		if v, ok := strings.CutPrefix(l, name+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
