// Package capability builds what recipes require from the system: Debian
// backports for now (principles 5.1, 5.4). Capabilities are packages, never
// recipes; recipe packages depend on them.
package capability

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getdovy/dovy/internal/catalog"
)

// Kind marks a capability package: X-Dovy-Capability, not X-Dovy-Recipe.
const Kind = "capability"

// Backports is the backports capability's package.
const Backports = "dovy-backports"

// Source is one apt source entry.
type Source struct {
	URIs, Suites, Components []string
	SignedBy                 string
}

// ParseDeb822 reads a .sources file.
func ParseDeb822(text string) []Source {
	var out []Source
	cur := map[string]string{}
	flush := func() {
		if cur["types"] != "" && strings.Contains(cur["types"], "deb") && !strings.EqualFold(cur["enabled"], "no") {
			out = append(out, Source{
				URIs: strings.Fields(cur["uris"]), Suites: strings.Fields(cur["suites"]),
				Components: strings.Fields(cur["components"]), SignedBy: cur["signed-by"],
			})
		}
		cur = map[string]string{}
	}
	key := ""
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.TrimSpace(l) == "":
			flush()
		case strings.HasPrefix(l, "#"):
		case l[0] == ' ' || l[0] == '\t':
			if key != "" {
				cur[key] += "\n" + strings.TrimSpace(l)
			}
		default:
			k, v, _ := strings.Cut(l, ":")
			key = strings.ToLower(strings.TrimSpace(k))
			cur[key] = strings.TrimSpace(v)
		}
	}
	flush()
	return out
}

// ParseList reads one-line entries: deb [options] uri suite components...
func ParseList(text string) []Source {
	var out []Source
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(strings.SplitN(l, "#", 2)[0])
		if len(f) < 3 || f[0] != "deb" {
			continue
		}
		f = f[1:]
		s := Source{}
		if strings.HasPrefix(f[0], "[") {
			for len(f) > 0 {
				o := strings.Trim(f[0], "[]")
				if v, ok := strings.CutPrefix(o, "signed-by="); ok {
					s.SignedBy = v
				}
				done := strings.HasSuffix(f[0], "]")
				f = f[1:]
				if done {
					break
				}
			}
		}
		if len(f) < 2 {
			continue
		}
		s.URIs, s.Suites, s.Components = f[:1], f[1:2], f[2:]
		out = append(out, s)
	}
	return out
}

// System reads the apt sources under etc (normally /etc/apt).
func System(etc string) []Source {
	var out []Source
	if b, err := os.ReadFile(filepath.Join(etc, "sources.list")); err == nil {
		out = append(out, ParseList(string(b))...)
	}
	files, _ := filepath.Glob(filepath.Join(etc, "sources.list.d", "*"))
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		switch {
		case strings.HasSuffix(f, ".sources"):
			out = append(out, ParseDeb822(string(b))...)
		case strings.HasSuffix(f, ".list"):
			out = append(out, ParseList(string(b))...)
		}
	}
	return out
}

// BackportsSource is the source dovy-backports ships: the system's Debian
// mirror, components and key, with the backports suite (5.4). present is
// true when the system already has backports; then dovy ships nothing (3.6).
func BackportsSource(srcs []Source, codename string) (text string, present bool, err error) {
	var main *Source
	for i, s := range srcs {
		for _, suite := range s.Suites {
			switch suite {
			case codename + "-backports":
				return "", true, nil
			case codename:
				if main == nil {
					main = &srcs[i]
				}
			}
		}
	}
	if main == nil || len(main.URIs) == 0 {
		return "", false, fmt.Errorf("no Debian source for %s in /etc/apt", codename)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Types: deb\nURIs: %s\nSuites: %s-backports\nComponents: %s\n", main.URIs[0], codename, strings.Join(main.Components, " "))
	if main.SignedBy != "" {
		fmt.Fprintf(&b, "Signed-By: %s\n", main.SignedBy)
	}
	return b.String(), false, nil
}

// BackportsRecipe is dovy-backports as a recipe to build. source is empty
// when the system already has backports.
func BackportsRecipe(source string) *catalog.Recipe {
	r := &catalog.Recipe{
		Name: "backports", Kind: Kind, Version: "1", Maintainer: "dovy",
		Description: "Debian backports source, for dovy recipes",
	}
	if source != "" {
		r.Files = []catalog.File{{Path: "/etc/apt/sources.list.d/dovy-backports.sources", Content: source}}
	} else {
		r.Description = "Debian backports, already configured on this system"
	}
	return r
}

// Expand returns r as it is built: a backports recipe gets its pin (5.4)
// and depends on dovy-backports; conflicts name the partner's package.
func Expand(r *catalog.Recipe, rs map[string]*catalog.Recipe, codename string) *catalog.Recipe {
	x := *r
	x.Packages = append([]string{}, r.Packages...)
	x.Files = append([]catalog.File{}, r.Files...)
	if r.Backports {
		x.Files = append(x.Files, catalog.File{
			Path:    "/etc/apt/preferences.d/dovy-" + r.Name + ".pref",
			Content: Pin(r.Packages, codename),
		})
		x.Packages = append(x.Packages, Backports)
	}
	x.Conflicts = nil
	for _, c := range r.Conflicts {
		if o := rs[c]; o != nil {
			x.Conflicts = append(x.Conflicts, o.Package())
		}
	}
	return &x
}

// Pin keeps exactly these packages on backports (5.4, 5.5).
func Pin(pkgs []string, codename string) string {
	return fmt.Sprintf("Package: %s\nPin: release n=%s-backports\nPin-Priority: 500\n", strings.Join(pkgs, " "), codename)
}

// Codename reads VERSION_CODENAME from an os-release file.
func Codename(osRelease string) (string, error) {
	f, err := os.Open(osRelease)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "VERSION_CODENAME="); ok {
			return strings.Trim(v, `"`), nil
		}
	}
	return "", fmt.Errorf("%s: no VERSION_CODENAME", osRelease)
}
