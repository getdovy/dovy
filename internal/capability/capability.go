// Package capability builds what recipes require from the system: Debian
// backports for now (principles 5.1, 5.4). Capabilities are packages, never
// recipes; recipe packages depend on them.
package capability

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getdovy/dovy/internal/catalog"
)

// Kind marks a capability package: X-Dovy-Capability, not X-Dovy-Recipe.
const Kind = "capability"

// Capability packages.
const (
	Backports  = "dovy-backports"
	Components = "dovy-components"
	I386       = "dovy-i386"
)

// Package names a capability's package: backports -> dovy-backports.
func Package(name string) string { return "dovy-" + name }

// nonFree is what the components capability adds when missing (5.3).
var nonFree = []string{"contrib", "non-free", "non-free-firmware"}

// Source is one apt source entry.
type Source struct {
	URIs, Suites, Components []string
	SignedBy                 string
	File                     string // where it was read
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
		for _, s := range ParseList(string(b)) {
			s.File = filepath.Join(etc, "sources.list")
			out = append(out, s)
		}
	}
	files, _ := filepath.Glob(filepath.Join(etc, "sources.list.d", "*"))
	sort.Strings(files)
	for _, f := range files {
		if strings.HasPrefix(filepath.Base(f), "dovy-") {
			continue // dovy's own capabilities: computed, not read
		}
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var got []Source
		switch {
		case strings.HasSuffix(f, ".sources"):
			got = ParseDeb822(string(b))
		case strings.HasSuffix(f, ".list"):
			got = ParseList(string(b))
		}
		for i := range got {
			got[i].File = f
		}
		out = append(out, got...)
	}
	return out
}

// BackportsSource is the source dovy-backports ships: the system's Debian
// mirror, components and key, with the backports suite (5.4). present is
// true when the system already has backports; then dovy ships nothing (3.6).
func BackportsSource(srcs []Source, codename string, extra []string) (text string, present bool, err error) {
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
	comps := append([]string{}, main.Components...)
	for _, c := range extra {
		if !contains(comps, c) {
			comps = append(comps, c)
		}
	}
	fmt.Fprintf(&b, "Types: deb\nURIs: %s\nSuites: %s-backports\nComponents: %s\n", main.URIs[0], codename, strings.Join(comps, " "))
	if k := signedBy(*main, "dovy-backports.sources"); k != "" {
		fmt.Fprintf(&b, "Signed-By: %s\n", k)
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
	}
	for _, c := range r.Needs() {
		x.Packages = append(x.Packages, Package(c))
	}
	if r.Repo != nil && !r.Repo.Repack { // its source, key and pin are the repository package's
		x.Packages = append(x.Packages, Package("repo-"+r.Repo.Name))
	}
	x.Recommends = nil
	for _, m := range r.Members {
		if o := rs[m]; o != nil {
			x.Recommends = append(x.Recommends, o.Package())
		}
	}
	x.Conflicts = nil
	for _, c := range r.Conflicts {
		if o := rs[c]; o != nil {
			x.Conflicts = append(x.Conflicts, o.Package())
		}
	}
	return &x
}

// Pin keeps exactly these packages on backports (5.4, 5.5), on every
// architecture: apt matches a bare name on the native one only, so the
// i386 copies of a pinned library would stay on stable and never match
// the amd64 version (steam's and NVIDIA's 32-bit libraries).
func Pin(pkgs []string, codename string) string {
	return fmt.Sprintf("Package: %s\nPin: release n=%s-backports\nPin-Priority: 500\n", strings.Join(anyArch(pkgs), " "), codename)
}

// anyArch names each package once, as name:any, which apt's pins match
// on every architecture.
func anyArch(pkgs []string) []string {
	var names []string
	for _, p := range pkgs {
		n, _, _ := strings.Cut(p, ":")
		if n += ":any"; !contains(names, n) {
			names = append(names, n)
		}
	}
	return names
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

// ComponentsSource adds, for each Debian entry of the system (stable,
// updates, security, backports), only the components it lacks (5.3). added lists them;
// text is empty when nothing is missing.
func ComponentsSource(srcs []Source, codename string) (text string, added []string) {
	var b strings.Builder
	seen := map[string]bool{}
	for _, s := range srcs {
		var suites []string
		for _, suite := range s.Suites {
			if suite == codename || suite == codename+"-updates" || suite == codename+"-security" || suite == codename+"-backports" {
				suites = append(suites, suite)
			}
		}
		if len(suites) == 0 || len(s.URIs) == 0 {
			continue
		}
		var missing []string
		for _, c := range nonFree {
			if !contains(s.Components, c) {
				missing = append(missing, c)
				if !seen[c] {
					seen[c] = true
					added = append(added, c)
				}
			}
		}
		if len(missing) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Types: deb\nURIs: %s\nSuites: %s\nComponents: %s\n", s.URIs[0], strings.Join(suites, " "), strings.Join(missing, " "))
		if k := signedBy(s, "dovy-components.sources"); k != "" {
			fmt.Fprintf(&b, "Signed-By: %s\n", k)
		}
	}
	return b.String(), added
}

// DebianKeyrings are where debian-archive-keyring puts Debian's keys.
var DebianKeyrings = []string{"/usr/share/keyrings/debian-archive-keyring.gpg", "/usr/share/keyrings/debian-archive-keyring.pgp"}

// signedBy is the key for dovy's copy of the Debian entry s: its own, or,
// when it names none (a fresh install's one-line sources.list), Debian's
// keyring. apt refuses an entry without Signed-By read after one with it
// for the same suite, so Debian's keyring is named only when apt reads s
// before ours, dovy's file.
func signedBy(s Source, ours string) string {
	if s.SignedBy != "" {
		return s.SignedBy
	}
	if s.File != "" && filepath.Base(s.File) != "sources.list" && filepath.Base(s.File) > ours {
		return ""
	}
	for _, k := range DebianKeyrings {
		if _, err := os.Stat(k); err == nil {
			return k
		}
	}
	return ""
}

// ComponentsRecipe is dovy-components; source is empty when nothing is
// missing.
func ComponentsRecipe(source string) *catalog.Recipe {
	r := &catalog.Recipe{
		Name: "components", Kind: Kind, Version: "1", Maintainer: "dovy",
		Description: "Debian contrib, non-free and non-free-firmware, for dovy recipes",
	}
	if source != "" {
		r.Files = []catalog.File{{Path: "/etc/apt/sources.list.d/dovy-components.sources", Content: source}}
	} else {
		r.Description = "Debian components, already configured on this system"
	}
	return r
}

// I386Added records that dovy, not the user, added i386 (3.6, 5.2).
const I386Added = "/var/lib/dovy/i386-added"

// I386Recipe is dovy-i386: its scripts add i386 if missing and remove it
// only if dovy added it and no i386 package is left (5.2, 4.8).
func I386Recipe() *catalog.Recipe {
	return &catalog.Recipe{
		Name: "i386", Kind: Kind, Version: "1", Maintainer: "dovy",
		Description: "i386 architecture, for dovy recipes",
		Scripts: map[string]string{
			"postinst": `#!/bin/sh
set -e
if [ "$1" = configure ] && ! dpkg --print-foreign-architectures | grep -qx i386; then
	dpkg --add-architecture i386
	mkdir -p /var/lib/dovy
	touch ` + I386Added + `
fi
`,
			"postrm": `#!/bin/sh
set -e
if [ "$1" = remove ] && [ -e ` + I386Added + ` ] &&
	! dpkg-query -W -f '${Architecture}\n' | grep -qx i386; then
	dpkg --remove-architecture i386
	rm -f ` + I386Added + `
fi
`,
		},
	}
}

// Content is what a capability package ships, to compare with the installed
// one: files and scripts.
func Content(r *catalog.Recipe) string {
	var b strings.Builder
	for _, f := range r.Files {
		b.WriteString(f.Path + "\n" + f.Content)
	}
	for _, k := range []string{"preinst", "postinst", "prerm", "postrm"} {
		b.WriteString(k + "\n" + r.Scripts[k])
	}
	return b.String()
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Host is a repository's host name, the unit of 5.10.
func Host(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Host
}

// UsedBy returns a source on host that the user configured, if any.
func UsedBy(srcs []Source, host string) *Source {
	for i, s := range srcs {
		for _, u := range s.URIs {
			if Host(u) == host {
				return &srcs[i]
			}
		}
	}
	return nil
}

// RepoSource is the apt source of a vendor repository, with its keyring;
// i386 too when pkgs has :i386 packages.
func RepoSource(q *catalog.Repository, keyring string, pkgs []string) string {
	comps := ""
	if !q.Flat() {
		comps = "Components: " + strings.Join(q.Components, " ") + "\n"
	}
	arch := "amd64"
	for _, p := range pkgs {
		if strings.HasSuffix(p, ":i386") {
			arch = "amd64 i386"
		}
	}
	return fmt.Sprintf("Types: deb\nURIs: %s\nSuites: %s\n%sArchitectures: %s\nSigned-By: %s\n",
		q.URI, q.Suite, comps, arch, keyring)
}

// RepoPin lets the vendor supply only the packages of the recipes using
// the repository (5.10): the host at 1, those packages at 100.
func RepoPin(q *catalog.Repository, pkgs []string) string {
	h := Host(q.URI)
	return fmt.Sprintf("Package: *\nPin: origin %s\nPin-Priority: 1\n\nPackage: %s\nPin: origin %s\nPin-Priority: 100\n",
		h, strings.Join(anyArch(pkgs), " "), h)
}

// Keyring is where a repository package installs its vendor's key.
func Keyring(repo string) string { return "/usr/share/keyrings/dovy-" + repo + ".pgp" }

// RepoRecipe is dovy-repo-<name>: the vendor's key (binary, checked by
// fingerprint), source, pin for pkgs (the packages of the recipes using
// it), and its files for the vendor's scripts (2.4, 2.6, 5.10, ADR 0013).
func RepoRecipe(q *catalog.Repository, key []byte, pkgs []string) *catalog.Recipe {
	files := []catalog.File{
		{Path: Keyring(q.Name), Content: string(key)},
		{Path: "/etc/apt/sources.list.d/dovy-" + q.Name + ".sources", Content: RepoSource(q, Keyring(q.Name), pkgs)},
	}
	if q.Pinned() {
		files = append(files, catalog.File{Path: "/etc/apt/preferences.d/dovy-" + q.Name + ".pref", Content: RepoPin(q, pkgs)})
	}
	return &catalog.Recipe{
		Name: "repo-" + q.Name, Kind: Kind, Version: "1", Maintainer: "dovy",
		Description: Host(q.URI) + " repository, for dovy recipes",
		Files:       append(files, q.Files...),
	}
}
