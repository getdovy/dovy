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
	text, present, err := BackportsSource(ParseDeb822(deb822), "trixie")
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
	text, _, _ = BackportsSource(ParseList(list), "trixie")
	if !strings.Contains(text, "URIs: https://mirror.example/debian\n") || !strings.Contains(text, "Components: main non-free\n") || !strings.Contains(text, "Signed-By: /k.gpg\n") {
		t.Errorf("one-line:\n%s", text)
	}

	_, present, _ = BackportsSource(ParseList("deb http://x/debian trixie main\ndeb http://x/debian trixie-backports main\n"), "trixie")
	if !present {
		t.Error("existing backports not seen")
	}
	if _, _, err := BackportsSource(ParseList("deb http://x/ubuntu noble main\n"), "trixie"); err == nil {
		t.Error("no Debian source accepted")
	}
	disabled := strings.Replace(deb822, "Types: deb\n# http", "Types: deb\nEnabled: no\n# http", 1)
	if _, _, err := BackportsSource(ParseDeb822(disabled), "trixie"); err == nil {
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
