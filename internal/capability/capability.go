// Package capability builds the system packages recipes depend on.
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

const (
	Backports  = "dovy-backports"
	Components = "dovy-components"
	I386       = "dovy-i386"
)

func Package(name string) string { return "dovy-" + name }

var nonFree = []string{"contrib", "non-free", "non-free-firmware"}

type Source struct {
	URIs, Suites, Components []string
	SignedBy                 string
	File                     string
	Codenames                map[string]string // suite -> release codename, when they differ
	Foreign                  bool              // apt's lists name a non-Debian origin
}

// release maps a role suite ("stable") to its codename as apt last fetched it; "" for a non-Debian source.
func (s Source) release(suite string) string {
	if s.Foreign {
		return ""
	}
	if c := s.Codenames[suite]; c != "" {
		return c
	}
	return suite
}

var debianOrigins = map[string]bool{"Debian": true, "Debian Backports": true}

// Resolve maps role suites (stable, oldstable...) to codenames and marks vendor sources
// Foreign, from apt's Release files. Suites apt has not fetched stay unresolved.
func Resolve(srcs []Source, lists string) {
	for i := range srcs {
		for _, suite := range srcs[i].Suites {
			for _, u := range srcs[i].URIs {
				origin, c, ok := releaseFields(lists, u, suite)
				if !ok {
					continue
				}
				if !debianOrigins[origin] {
					srcs[i].Foreign = true
				}
				if c != "" && c != suite {
					if srcs[i].Codenames == nil {
						srcs[i].Codenames = map[string]string{}
					}
					srcs[i].Codenames[suite] = c
				}
				break
			}
		}
	}
}

func releaseFields(lists, uri, suite string) (origin, codename string, ok bool) {
	base := filepath.Join(lists, listName(uri, suite))
	for _, f := range []string{base + "InRelease", base + "Release"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if v, found := strings.CutPrefix(l, "Origin:"); found && origin == "" {
				origin = strings.TrimSpace(v)
			}
			if v, found := strings.CutPrefix(l, "Codename:"); found && codename == "" {
				codename = strings.TrimSpace(v)
			}
		}
		return origin, codename, true
	}
	return "", "", false
}

// listName mirrors apt's URItoFileName: no scheme or credentials, unsafe bytes %-quoted, "/" as "_".
func listName(uri, suite string) string {
	u := uri
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if at := strings.LastIndex(strings.SplitN(u, "/", 2)[0], "@"); at >= 0 {
		u = u[at+1:]
	}
	u = strings.TrimSuffix(u, "/") + "/dists/" + suite + "/"
	var b strings.Builder
	for _, c := range []byte(u) {
		if c <= 0x20 || c >= 0x7f || strings.IndexByte(`\|{}[]<>"^~_=!@#$%*`, c) >= 0 {
			fmt.Fprintf(&b, "%%%02x", c)
		} else {
			b.WriteByte(c)
		}
	}
	return strings.ReplaceAll(b.String(), "/", "_")
}

func ParseDeb822(text string) []Source {
	var out []Source
	cur := map[string]string{}
	flush := func() {
		// "Types: deb-src" alone carries no binary packages.
		if contains(strings.Fields(cur["types"]), "deb") && !strings.EqualFold(cur["enabled"], "no") {
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
			continue // dovy's own files are computed, not read
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

// BackportsSource always writes the codename suite, which the pins name.
func BackportsSource(srcs []Source, codename string, extra []string) (text string, present bool, err error) {
	var main *Source
	for i, s := range srcs {
		for _, suite := range s.Suites {
			switch s.release(suite) {
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
	if k := signedBy(*main, []string{codename + "-backports"}, srcs, codename, "dovy-backports.sources"); k != "" {
		fmt.Fprintf(&b, "Signed-By: %s\n", k)
	}
	return b.String(), false, nil
}

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
	if r.Repo != nil && !r.Repo.Repack { // the repository package ships source, key and pin
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

func Pin(pkgs []string, codename string) string {
	return fmt.Sprintf("Package: %s\nPin: release n=%s-backports\nPin-Priority: 500\n", strings.Join(anyArch(pkgs), " "), codename)
}

// anyArch uses name:any: apt pins match a bare name on the native architecture only,
// leaving i386 copies on stable, out of step with amd64.
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

func ComponentsSource(srcs []Source, codename string) (text string, added []string) {
	var b strings.Builder
	seen := map[string]bool{}
	for _, s := range srcs {
		var suites []string
		for _, suite := range s.Suites {
			if debianSuite(s.release(suite), codename) {
				suites = append(suites, suite) // the user's suite name, so both entries move together
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
		if k := signedBy(s, suites, srcs, codename, "dovy-components.sources"); k != "" {
			fmt.Fprintf(&b, "Signed-By: %s\n", k)
		}
	}
	return b.String(), added
}

var DebianKeyrings = []string{"/usr/share/keyrings/debian-archive-keyring.gpg", "/usr/share/keyrings/debian-archive-keyring.pgp"}

func debianSuite(suite, codename string) bool {
	return suite == codename || suite == codename+"-updates" || suite == codename+"-security" || suite == codename+"-backports"
}

// signedBy reuses the user's key path: apt rejects two different Signed-By values for one
// URI and suite, even two paths to the same file (.gpg and .pgp). Inline keys are not copied.
func signedBy(s Source, suites []string, srcs []Source, codename, ours string) string {
	if s.SignedBy != "" {
		return s.SignedBy
	}
	path := func(o Source) bool { return o.SignedBy != "" && !strings.Contains(o.SignedBy, "\n") }
	for _, o := range srcs {
		if path(o) && sameURI(o, s) && overlap(o.Suites, suites) {
			return o.SignedBy
		}
	}
	for _, o := range srcs {
		// Only Debian's keyring, so a vendor key is never reused for a Debian entry.
		if !path(o) || !strings.HasPrefix(filepath.Base(o.SignedBy), "debian-archive-keyring.") {
			continue
		}
		for _, suite := range o.Suites {
			if debianSuite(o.release(suite), codename) {
				return o.SignedBy
			}
		}
	}
	// Not named for an entry apt reads after ours; apt 3.0 accepts either order, only two different keys conflict.
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

// I386Added marks that dovy, not the user, added i386; only then does postrm remove it.
const I386Added = "/var/lib/dovy/i386-added"

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

// Content is compared with the installed capability package to detect changes.
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

// sameURI ignores a trailing slash, as apt does.
func sameURI(a, b Source) bool {
	for _, u := range a.URIs {
		for _, v := range b.URIs {
			if strings.TrimSuffix(u, "/") == strings.TrimSuffix(v, "/") {
				return true
			}
		}
	}
	return false
}

func overlap(a, b []string) bool {
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func Host(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Host
}

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

// RepoPin lets the vendor supply only the recipes' packages: host at 1, those packages at 100.
func RepoPin(q *catalog.Repository, pkgs []string) string {
	h := Host(q.URI)
	return fmt.Sprintf("Package: *\nPin: origin %s\nPin-Priority: 1\n\nPackage: %s\nPin: origin %s\nPin-Priority: 100\n",
		h, strings.Join(anyArch(pkgs), " "), h)
}

func Keyring(repo string) string { return "/usr/share/keyrings/dovy-" + repo + ".pgp" }

// RepoRecipe ships the vendor key as binary, checked by fingerprint (ADR 0013).
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
