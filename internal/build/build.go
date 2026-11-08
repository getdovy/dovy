// Package build turns a recipe into a .deb in dovy's cache.
package build

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	return r.Kind == catalog.UpstreamBinary || r.Kind == catalog.Setup
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
