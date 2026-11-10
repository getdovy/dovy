package deb

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
)

func pfetch() *catalog.Recipe {
	return &catalog.Recipe{
		Name: "pfetch", Kind: catalog.UpstreamBinary, Version: "3.1.1",
		Maintainer: "hmwassim", Description: "Pretty system information tool",
		Install: map[string]string{"/usr/bin/pfetch": "pfetch", "/usr/share/doc/pfetch/README": "README"},
	}
}

func TestRelease(t *testing.T) {
	p := filepath.Join(t.TempDir(), "os-release")
	os.WriteFile(p, []byte("PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nVERSION_ID=\"13\"\n"), 0o644)
	if n, err := Release(p); n != 13 || err != nil {
		t.Errorf("got %d %v", n, err)
	}
	os.WriteFile(p, []byte("ID=debian\n"), 0o644)
	if _, err := Release(p); err == nil {
		t.Error("no VERSION_ID accepted")
	}
}

func TestVersion(t *testing.T) {
	r := pfetch()
	if v := Version(r, 13); v != "3.1.1-1~dovy13" {
		t.Errorf("got %s", v)
	}
	r.Revision = 2
	if v := Version(r, 14); v != "3.1.1-2~dovy14" {
		t.Errorf("got %s", v)
	}
}

func TestControl(t *testing.T) {
	want := `Package: pfetch
Version: 3.1.1-1~dovy13
Architecture: amd64
Maintainer: hmwassim
Installed-Size: 7
Section: misc
Priority: optional
X-Dovy-Recipe: pfetch
Description: Pretty system information tool
`
	if got := Control(pfetch(), "3.1.1-1~dovy13", 7); got != want {
		t.Errorf("got:\n%s", got)
	}
}

func TestStageAndBuild(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("no dpkg-deb")
	}
	src, stage, out := t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(src, "pfetch"), []byte("#!/bin/sh\n"), 0o700)
	os.WriteFile(filepath.Join(src, "README"), []byte("doc\n"), 0o600)
	r := pfetch()
	if err := Stage(r, src, stage); err != nil {
		t.Fatal(err)
	}
	deb, err := Build(r, "3.1.1-1~dovy13", stage, out)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(deb) != "pfetch_3.1.1-1~dovy13_amd64.deb" {
		t.Errorf("name %s", deb)
	}
	b, _ := exec.Command("dpkg-deb", "-f", deb, "X-Dovy-Recipe", "Version").Output()
	if !strings.Contains(string(b), "X-Dovy-Recipe: pfetch") || !strings.Contains(string(b), "Version: 3.1.1-1~dovy13") {
		t.Errorf("fields:\n%s", b)
	}
	b, _ = exec.Command("dpkg-deb", "-c", deb).Output()
	for _, w := range []string{"-rwxr-xr-x root/root", "./usr/bin/pfetch", "-rw-r--r-- root/root", "./usr/share/doc/pfetch/README"} {
		if !strings.Contains(string(b), w) {
			t.Errorf("contents lack %q:\n%s", w, b)
		}
	}
}

func TestStageRefuses(t *testing.T) {
	src := t.TempDir()
	os.Symlink("/etc/passwd", filepath.Join(src, "pfetch"))
	r := pfetch()
	r.Install = map[string]string{"/usr/bin/pfetch": "pfetch"}
	if err := Stage(r, src, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("link: %v", err)
	}
	r.Install = map[string]string{"/usr/bin/x": "missing"}
	if err := Stage(r, src, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not in the sources") {
		t.Errorf("missing: %v", err)
	}
}

func TestSetup(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("no dpkg-deb")
	}
	r := &catalog.Recipe{
		Name: "zram", Kind: catalog.Setup, Version: "1", Maintainer: "test",
		Description: "Compressed swap in RAM", Packages: []string{"systemd-zram-generator"},
		Files: []catalog.File{{Path: "/etc/systemd/zram-generator.conf.d/00-dovy.conf", Content: "\n[zram0]\n"}},
	}
	stage := t.TempDir()
	if err := Stage(r, "", stage); err != nil {
		t.Fatal(err)
	}
	deb, err := Build(r, Version(r, 13), stage, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(deb) != "dovy-zram_1-1~dovy13_all.deb" {
		t.Errorf("name %s", deb)
	}
	b, _ := exec.Command("dpkg-deb", "-f", deb, "Depends", "Architecture").Output()
	if !strings.Contains(string(b), "Depends: systemd-zram-generator") || !strings.Contains(string(b), "Architecture: all") {
		t.Errorf("fields:\n%s", b)
	}
	b, _ = exec.Command("dpkg-deb", "--ctrl-tarfile", deb).Output()
	if !strings.Contains(string(b), "/etc/systemd/zram-generator.conf.d/00-dovy.conf") {
		t.Error("conffiles missing")
	}
	b, _ = exec.Command("dpkg-deb", "--fsys-tarfile", deb).Output()
	if !strings.Contains(string(b), "[zram0]\n") || strings.Contains(string(b), "\n\n[zram0]") {
		t.Error("drop-in content")
	}
}

// A plan's stub holds only the control file.
func TestStub(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("no dpkg-deb")
	}
	r := &catalog.Recipe{Name: "zram", Kind: catalog.Setup, Version: "1", Maintainer: "test", Description: "d",
		Files: []catalog.File{{Path: "/etc/x.d/a.conf", Content: "a"}}}
	if _, err := Build(r, "1-1~dovy13", t.TempDir(), t.TempDir()); err != nil {
		t.Error(err)
	}
}

func TestControlCapability(t *testing.T) {
	r := capability.BackportsRecipe("x")
	c := Control(r, "1-1~dovy13", 1)
	for _, w := range []string{"Package: dovy-backports\n", "Architecture: all\n", "X-Dovy-Capability: backports\n"} {
		if !strings.Contains(c, w) {
			t.Errorf("lacks %q:\n%s", w, c)
		}
	}
	m := &catalog.Recipe{Name: "mesa", Kind: catalog.Setup, Packages: []string{"libgbm1"}, Conflicts: []string{"dovy-mesa-backports"}}
	if c := Control(m, "1-1~dovy13", 1); !strings.Contains(c, "Depends: libgbm1\nConflicts: dovy-mesa-backports\n") {
		t.Errorf("relations:\n%s", c)
	}
}

func TestControlReplaces(t *testing.T) {
	r := &catalog.Recipe{Name: "firefox", Kind: catalog.VendorRepository, Maintainer: "t", Description: "Firefox",
		Packages: []string{"firefox"}, Replaces: []string{"firefox-esr"}, Replaced: []string{"firefox-esr manual"}}
	got := control(r, "1-1~dovy13", 1, []string{"libc6"})
	for _, want := range []string{"Depends: firefox, libc6\n", "Conflicts: firefox-esr\n", "X-Dovy-Replaced: firefox-esr manual\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	y := &catalog.Recipe{Name: "yad", Kind: catalog.UpstreamBuild, Replaces: []string{"yad"}}
	if c := Conflicts(y); len(c) != 0 {
		t.Errorf("same name conflicts: %v", c)
	}
}
