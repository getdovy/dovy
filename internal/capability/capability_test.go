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
		x.Files[0].Content != "Package: libgbm1 mesa-libgallium\nPin: release n=trixie-backports\nPin-Priority: 500\n" {
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
	if got := Pin([]string{"libgl1-mesa-dri", "libgl1-mesa-dri:i386", "libgbm1"}, "trixie"); got != "Package: libgl1-mesa-dri libgbm1\nPin: release n=trixie-backports\nPin-Priority: 500\n" {
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
	want := "Package: *\nPin: origin brave-browser-apt-release.s3.brave.com\nPin-Priority: 1\n\nPackage: brave-browser brave-origin\nPin: origin brave-browser-apt-release.s3.brave.com\nPin-Priority: 100\n"
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
