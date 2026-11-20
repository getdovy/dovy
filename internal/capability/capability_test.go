package capability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getdovy/dovy/internal/catalog"
)

// debian.sources as Debian 13 installs it.
const deb822 = `Types: deb
# http://snapshot.debian.org/archive/debian/20261104T212339Z
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main contrib non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp

Types: deb
URIs: http://deb.debian.org/debian-security
Suites: trixie-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp
`

func TestBackportsSource(t *testing.T) {
	text, present, err := BackportsSource(ParseDeb822(deb822), "trixie", nil)
	want := `Types: deb
URIs: http://deb.debian.org/debian
Suites: trixie-backports
Components: main contrib non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp
`
	if err != nil || present || text != want {
		t.Errorf("got %v %v\n%s", err, present, text)
	}

	list := "deb [arch=amd64 signed-by=/k.gpg] https://mirror.example/debian trixie main non-free\n" +
		"# deb http://x trixie-backports main\n"
	text, _, _ = BackportsSource(ParseList(list), "trixie", nil)
	if !strings.Contains(text, "URIs: https://mirror.example/debian\n") || !strings.Contains(text, "Components: main non-free\n") || !strings.Contains(text, "Signed-By: /k.gpg\n") {
		t.Errorf("one-line:\n%s", text)
	}

	_, present, _ = BackportsSource(ParseList("deb http://x/debian trixie main\ndeb http://x/debian trixie-backports main\n"), "trixie", nil)
	if !present {
		t.Error("existing backports not seen")
	}
	if _, _, err := BackportsSource(ParseList("deb http://x/ubuntu noble main\n"), "trixie", nil); err == nil {
		t.Error("no Debian source accepted")
	}
	disabled := strings.Replace(deb822, "Types: deb\n# http", "Types: deb\nEnabled: no\n# http", 1)
	if _, _, err := BackportsSource(ParseDeb822(disabled), "trixie", nil); err == nil {
		t.Error("disabled entry used")
	}
}

func TestSystem(t *testing.T) {
	etc := t.TempDir()
	os.MkdirAll(filepath.Join(etc, "sources.list.d"), 0o755)
	os.WriteFile(filepath.Join(etc, "sources.list.d", "debian.sources"), []byte(deb822), 0o644)
	os.WriteFile(filepath.Join(etc, "sources.list.d", "x.list"), []byte("deb http://x stable main\n"), 0o644)
	if got := System(etc); len(got) != 3 {
		t.Errorf("got %d sources", len(got))
	}
}

func TestExpand(t *testing.T) {
	rs := map[string]*catalog.Recipe{
		"mesa":           {Name: "mesa", Kind: catalog.Setup, Packages: []string{"libgbm1"}, Conflicts: []string{"mesa-backports"}},
		"mesa-backports": {Name: "mesa-backports", Kind: catalog.Setup, Packages: []string{"libgbm1", "mesa-libgallium"}, Backports: true, Conflicts: []string{"mesa"}},
	}
	x := Expand(rs["mesa-backports"], rs, "trixie")
	if strings.Join(x.Packages, " ") != "libgbm1 mesa-libgallium dovy-backports" || strings.Join(x.Conflicts, " ") != "dovy-mesa" {
		t.Errorf("got %v %v", x.Packages, x.Conflicts)
	}
	if len(x.Files) != 1 || x.Files[0].Path != "/etc/apt/preferences.d/dovy-mesa-backports.pref" ||
		x.Files[0].Content != "Package: libgbm1:any mesa-libgallium:any\nPin: release n=trixie-backports\nPin-Priority: 500\n" {
		t.Errorf("pin %+v", x.Files)
	}
	if len(rs["mesa-backports"].Packages) != 2 {
		t.Error("recipe changed")
	}
	if x := Expand(rs["mesa"], rs, "trixie"); len(x.Files) != 0 || strings.Join(x.Conflicts, " ") != "dovy-mesa-backports" {
		t.Errorf("stable half %+v", x)
	}
}

func TestComponents(t *testing.T) {
	text, added := ComponentsSource(ParseDeb822(deb822), "trixie")
	want := `Types: deb
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: non-free
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp

Types: deb
URIs: http://deb.debian.org/debian-security
Suites: trixie-security
Components: contrib non-free non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp
`
	if text != want || strings.Join(added, " ") != "non-free contrib non-free-firmware" {
		t.Errorf("got %v\n%s", added, text)
	}
	full := "deb http://x/debian trixie main contrib non-free non-free-firmware\n"
	if text, added := ComponentsSource(ParseList(full), "trixie"); text != "" || len(added) != 0 {
		t.Errorf("complete: %v %q", added, text)
	}
	bp, _, _ := BackportsSource(ParseDeb822(deb822), "trixie", []string{"non-free", "contrib"})
	if !strings.Contains(bp, "Components: main contrib non-free-firmware non-free\n") {
		t.Errorf("backports with added components:\n%s", bp)
	}
}

func TestSystemSkipsDovy(t *testing.T) {
	etc := t.TempDir()
	os.MkdirAll(filepath.Join(etc, "sources.list.d"), 0o755)
	os.WriteFile(filepath.Join(etc, "sources.list.d", "dovy-backports.sources"), []byte(deb822), 0o644)
	if got := System(etc); len(got) != 0 {
		t.Errorf("read dovy's own source: %d", len(got))
	}
}

func TestI386(t *testing.T) {
	r := I386Recipe()
	if r.Package() != I386 || !strings.Contains(r.Scripts["postinst"], "dpkg --add-architecture i386") ||
		!strings.Contains(r.Scripts["postrm"], "grep -qx i386") {
		t.Errorf("%+v", r)
	}
	if Content(r) == Content(ComponentsRecipe("")) {
		t.Error("content not distinct")
	}
}

func TestPinI386(t *testing.T) {
	if got := Pin([]string{"libgl1-mesa-dri", "libgl1-mesa-dri:i386", "libgbm1"}, "trixie"); got != "Package: libgl1-mesa-dri:any libgbm1:any\nPin: release n=trixie-backports\nPin-Priority: 500\n" {
		t.Errorf("got %q", got)
	}
	r := &catalog.Recipe{Name: "steam", Kind: catalog.Setup, Requires: []string{"components"}, Packages: []string{"steam-installer", "libgl1-mesa-dri:i386"}}
	if x := Expand(r, nil, "trixie"); strings.Join(x.Packages, " ") != "steam-installer libgl1-mesa-dri:i386 dovy-components dovy-i386" {
		t.Errorf("depends %v", x.Packages)
	}
}

func TestRepo(t *testing.T) {
	q := &catalog.Repository{Name: "brave", URI: "https://brave-browser-apt-release.s3.brave.com/", Suite: "stable", Components: []string{"main"},
		Files: []catalog.File{{Path: "/etc/default/brave-browser", Content: "x"}}}
	x := RepoRecipe(q, []byte{0x99, 1, 2}, []string{"brave-browser", "brave-origin"})
	if x.Package() != "dovy-repo-brave" || len(x.Files) != 4 || x.Files[0].Path != "/usr/share/keyrings/dovy-brave.pgp" || x.Files[3].Path != "/etc/default/brave-browser" {
		t.Fatalf("%+v", x)
	}
	if !strings.Contains(x.Files[1].Content, "Signed-By: /usr/share/keyrings/dovy-brave.pgp\n") {
		t.Errorf("source:\n%s", x.Files[1].Content)
	}
	want := "Package: *\nPin: origin brave-browser-apt-release.s3.brave.com\nPin-Priority: 1\n\nPackage: brave-browser:any brave-origin:any\nPin: origin brave-browser-apt-release.s3.brave.com\nPin-Priority: 100\n"
	if x.Files[2].Content != want {
		t.Errorf("pin:\n%s", x.Files[2].Content)
	}
	r := &catalog.Recipe{Name: "brave-origin", Kind: catalog.VendorRepository, Repository: "brave", Repo: q, Packages: []string{"brave-origin"}}
	if e := Expand(r, nil, "trixie"); strings.Join(e.Packages, " ") != "brave-origin dovy-repo-brave" {
		t.Errorf("recipe package %+v", e.Packages)
	}
	srcs := ParseList("deb [signed-by=/k] https://dl.google.com/linux/chrome/deb/ stable main\n")
	if UsedBy(srcs, "dl.google.com") == nil || UsedBy(srcs, "packages.mozilla.org") != nil {
		t.Error("host use")
	}
}

func TestFlatSource(t *testing.T) {
	q := &catalog.Repository{URI: "https://ppa.floorp.app/amd64", Suite: "./"}
	if got := RepoSource(q, "/k", nil); got != "Types: deb\nURIs: https://ppa.floorp.app/amd64\nSuites: ./\nArchitectures: amd64\nSigned-By: /k\n" {
		t.Errorf("got %q", got)
	}
}

func TestSignedByFallback(t *testing.T) {
	key := filepath.Join(t.TempDir(), "debian-archive-keyring.gpg")
	os.WriteFile(key, nil, 0o644)
	old := DebianKeyrings
	DebianKeyrings = []string{"/nonexistent.gpg", key}
	defer func() { DebianKeyrings = old }()

	// A fresh install's one-line sources.list names no key.
	srcs := ParseList("deb http://deb.debian.org/debian/ trixie main non-free-firmware\n")
	for i := range srcs {
		srcs[i].File = "/etc/apt/sources.list"
	}
	if text, _ := ComponentsSource(srcs, "trixie"); !strings.Contains(text, "Signed-By: "+key+"\n") {
		t.Errorf("components without Debian's keyring:\n%s", text)
	}
	if text, _, _ := BackportsSource(srcs, "trixie", nil); !strings.Contains(text, "Signed-By: "+key+"\n") {
		t.Errorf("backports without Debian's keyring:\n%s", text)
	}
	// Read after dovy's file: apt would refuse the pair, so no key is named.
	srcs[0].File = "/etc/apt/sources.list.d/zz-mirror.sources"
	if text, _ := ComponentsSource(srcs, "trixie"); strings.Contains(text, "Signed-By:") {
		t.Errorf("key named for an entry apt reads later:\n%s", text)
	}
	srcs[0].SignedBy = "/k.gpg"
	if text, _ := ComponentsSource(srcs, "trixie"); !strings.Contains(text, "Signed-By: /k.gpg\n") {
		t.Errorf("own key not kept:\n%s", text)
	}
}

func TestDeb822SourceOnly(t *testing.T) {
	src := "Types: deb-src\nURIs: http://deb.debian.org/debian\nSuites: trixie-backports\nComponents: main\n"
	if got := ParseDeb822(src); len(got) != 0 {
		t.Errorf("deb-src only read as %d package source(s)", len(got))
	}
	if got := ParseDeb822(strings.Replace(src, "deb-src", "deb deb-src", 1)); len(got) != 1 {
		t.Errorf("deb deb-src read as %d sources", len(got))
	}
	// Backports as source packages only: dovy still ships the binary source.
	text, present, err := BackportsSource(ParseDeb822(deb822+"\n"+src), "trixie", nil)
	if err != nil || present || !strings.Contains(text, "Suites: trixie-backports\n") {
		t.Errorf("got %v %v\n%s", err, present, text)
	}
}

func TestSignedByUser(t *testing.T) {
	key := filepath.Join(t.TempDir(), "debian-archive-keyring.gpg")
	os.WriteFile(key, nil, 0o644)
	old := DebianKeyrings
	DebianKeyrings = []string{key}
	defer func() { DebianKeyrings = old }()
	at := func(srcs []Source, file string) []Source {
		for i := range srcs {
			srcs[i].File = file
		}
		return srcs
	}
	fresh := at(ParseList("deb http://deb.debian.org/debian/ trixie main non-free-firmware\n"), "/etc/apt/sources.list")

	// Another Debian entry names .pgp: apt would refuse .gpg as a different key.
	pgp := at(ParseDeb822("Types: deb\nURIs: http://security.debian.org/debian-security\nSuites: trixie-security\n"+
		"Components: main contrib non-free non-free-firmware\nSigned-By: /usr/share/keyrings/debian-archive-keyring.pgp\n"),
		"/etc/apt/sources.list.d/debian.sources")
	srcs := append(append([]Source{}, fresh...), pgp...)
	if text, _ := ComponentsSource(srcs, "trixie"); !strings.Contains(text, "Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp\n") || strings.Contains(text, key) {
		t.Errorf("components without the user's key:\n%s", text)
	}
	if text, _, _ := BackportsSource(srcs, "trixie", nil); !strings.Contains(text, "Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp\n") {
		t.Errorf("backports without the user's key:\n%s", text)
	}

	// Same URI and suite wins over other Debian entries: apt merges the two.
	same := at(ParseList("deb [signed-by=/k.gpg] http://deb.debian.org/debian trixie contrib\n"), "/etc/apt/sources.list.d/extra.list")
	srcs = append(append(append([]Source{}, pgp...), fresh...), same...)
	text, _ := ComponentsSource(srcs, "trixie")
	if !strings.Contains(text, "Suites: trixie\nComponents: contrib non-free\nSigned-By: /k.gpg\n") {
		t.Errorf("same suite's key not used:\n%s", text)
	}

	// A key written inline is not copied: Debian's keyring, as before.
	inline := at(ParseDeb822("Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie-updates\nComponents: main\n"+
		"Signed-By:\n -----BEGIN PGP PUBLIC KEY BLOCK-----\n .\n -----END PGP PUBLIC KEY BLOCK-----\n"),
		"/etc/apt/sources.list.d/debian.sources")
	srcs = append(append([]Source{}, fresh...), inline...)
	if text, _ := ComponentsSource(srcs, "trixie"); !strings.Contains(text, "Suites: trixie\nComponents: contrib non-free\nSigned-By: "+key+"\n") {
		t.Errorf("inline key copied:\n%s", text)
	}
}

func TestListName(t *testing.T) {
	for _, c := range [][3]string{
		{"http://deb.debian.org/debian", "trixie", "deb.debian.org_debian_dists_trixie_"},
		{"http://security.debian.org/debian-security/", "trixie-security", "security.debian.org_debian-security_dists_trixie-security_"},
		{"https://user:pw@mirror.example:8080/deb_ian", "stable", "mirror.example:8080_deb%5fian_dists_stable_"},
	} {
		if got := listName(c[0], c[1]); got != c[2] {
			t.Errorf("%s %s: got %s", c[0], c[1], got)
		}
	}
}

func release(t *testing.T, lists, uri, suite, codename string) {
	t.Helper()
	releaseFrom(t, lists, uri, suite, "Debian", codename)
}

func releaseFrom(t *testing.T, lists, uri, suite, origin, codename string) {
	t.Helper()
	text := "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\nOrigin: " + origin + "\nSuite: " + suite + "\nCodename: " + codename + "\n"
	if err := os.WriteFile(filepath.Join(lists, listName(uri, suite)+"InRelease"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStableSuite(t *testing.T) {
	const deb, sec = "http://deb.debian.org/debian", "http://security.debian.org/debian-security"
	list := "deb " + deb + " stable main non-free-firmware\ndeb " + deb + " stable-updates main non-free-firmware\n" +
		"deb " + sec + " stable-security main non-free-firmware\n"

	// Never fetched: stable is not known to be trixie, as before.
	if _, _, err := BackportsSource(ParseList(list), "trixie", nil); err == nil {
		t.Error("unresolved stable taken as trixie")
	}

	lists := t.TempDir()
	release(t, lists, deb, "stable", "trixie")
	release(t, lists, deb, "stable-updates", "trixie-updates")
	release(t, lists, sec, "stable-security", "trixie-security")
	srcs := ParseList(list)
	Resolve(srcs, lists)
	text, added := ComponentsSource(srcs, "trixie")
	if !strings.Contains(text, "Suites: stable\nComponents: contrib non-free\n") ||
		!strings.Contains(text, "Suites: stable-updates\nComponents: contrib non-free\n") ||
		!strings.Contains(text, "Suites: stable-security\nComponents: contrib non-free\n") || strings.Join(added, " ") != "contrib non-free" {
		t.Errorf("components for stable: %v\n%s", added, text)
	}
	bp, present, err := BackportsSource(srcs, "trixie", nil)
	if err != nil || present || !strings.Contains(bp, "URIs: "+deb+"\nSuites: trixie-backports\n") {
		t.Errorf("backports for stable: %v %v\n%s", err, present, bp)
	}

	// stable-backports is the release's backports.
	withBp := list + "deb " + deb + " stable-backports main\n"
	releaseFrom(t, lists, deb, "stable-backports", "Debian Backports", "trixie-backports")
	srcs = ParseList(withBp)
	Resolve(srcs, lists)
	if _, present, _ := BackportsSource(srcs, "trixie", nil); !present {
		t.Error("stable-backports not seen as trixie-backports")
	}

	// Once forky is stable, a trixie system's stable entries are not trixie's.
	forky := t.TempDir()
	release(t, forky, deb, "stable", "forky")
	release(t, forky, sec, "stable-security", "forky-security")
	srcs = ParseList(list)
	Resolve(srcs, forky)
	if text, _ := ComponentsSource(srcs, "trixie"); strings.Contains(text, "Suites: stable") {
		t.Errorf("forky's stable completed for trixie:\n%s", text)
	}
}

func TestForeignTrixie(t *testing.T) {
	key := filepath.Join(t.TempDir(), "debian-archive-keyring.gpg")
	os.WriteFile(key, nil, 0o644)
	old := DebianKeyrings
	DebianKeyrings = []string{key}
	defer func() { DebianKeyrings = old }()
	const docker, deb = "https://download.docker.com/linux/debian", "http://deb.debian.org/debian"
	read := func() []Source {
		v := ParseList("deb [signed-by=/usr/share/keyrings/docker.gpg] " + docker + " trixie stable\n")
		v[0].File = "/etc/apt/sources.list.d/aa-docker.list"
		d := ParseList("deb " + deb + " trixie main non-free-firmware\n")
		d[0].File = "/etc/apt/sources.list.d/zz-debian.list"
		return append(v, d...) // the vendor's entry first
	}

	// A vendor's "trixie" suite is not Debian's once apt's lists say so.
	lists := t.TempDir()
	releaseFrom(t, lists, docker, "trixie", "Docker", "trixie")
	release(t, lists, deb, "trixie", "trixie")
	srcs := read()
	Resolve(srcs, lists)
	if !srcs[0].Foreign || srcs[1].Foreign {
		t.Fatalf("foreign: %v %v", srcs[0].Foreign, srcs[1].Foreign)
	}
	if text, _ := ComponentsSource(srcs, "trixie"); strings.Contains(text, docker) || !strings.Contains(text, "URIs: "+deb+"\n") {
		t.Errorf("components for the vendor's entry:\n%s", text)
	}
	if bp, _, err := BackportsSource(srcs, "trixie", nil); err != nil || !strings.Contains(bp, "URIs: "+deb+"\n") {
		t.Errorf("backports from the vendor's host: %v\n%s", err, bp)
	}

	// Never fetched: the vendor's key is still not given to Debian's entries.
	if text, _ := ComponentsSource(read(), "trixie"); strings.Contains(text, "URIs: "+deb+"\nSuites: trixie\nComponents: contrib non-free\nSigned-By: /usr/share/keyrings/docker.gpg") {
		t.Errorf("vendor key copied to Debian's entry:\n%s", text)
	}
}

func TestComponentsBackports(t *testing.T) {
	// The user's own backports line, main only: firmware-backports needs non-free-firmware there.
	list := "deb http://deb.debian.org/debian trixie main contrib non-free non-free-firmware\n" +
		"deb http://deb.debian.org/debian trixie-backports main\n"
	text, added := ComponentsSource(ParseList(list), "trixie")
	if !strings.Contains(text, "Suites: trixie-backports\nComponents: contrib non-free non-free-firmware\n") || strings.Contains(text, "Suites: trixie\n") ||
		strings.Join(added, " ") != "contrib non-free non-free-firmware" {
		t.Errorf("got %v\n%s", added, text)
	}
}
