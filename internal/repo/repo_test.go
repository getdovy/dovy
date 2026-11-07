package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func build(t *testing.T, name, version string) string {
	t.Helper()
	stage := t.TempDir()
	os.MkdirAll(filepath.Join(stage, "DEBIAN"), 0o755)
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: amd64\nMaintainer: test\nX-Dovy-Recipe: %s\nDescription: test\n", name, version, name)
	os.WriteFile(filepath.Join(stage, "DEBIAN", "control"), []byte(control), 0o644)
	deb := filepath.Join(t.TempDir(), fmt.Sprintf("%s_%s_amd64.deb", name, version))
	if b, err := exec.Command("dpkg-deb", "--root-owner-group", "--build", stage, deb).CombinedOutput(); err != nil {
		t.Fatalf("%s", b)
	}
	return deb
}

func TestPublish(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("no dpkg-deb")
	}
	dir := filepath.Join(t.TempDir(), "repo")
	if err := Publish(dir, []string{build(t, "pfetch", "3.1.1-1~dovy13"), build(t, "fzf", "0.75.0-1~dovy13")}, Direct); err != nil {
		t.Fatal(err)
	}
	if err := Publish(dir, []string{build(t, "pfetch", "3.1.1-2~dovy13")}, Direct); err != nil {
		t.Fatal(err)
	}
	debs, _ := filepath.Glob(filepath.Join(dir, "*.deb"))
	if len(debs) != 2 || !strings.HasSuffix(debs[1], "pfetch_3.1.1-2~dovy13_amd64.deb") {
		t.Errorf("debs: %v", debs)
	}
	pkgs, _ := os.ReadFile(filepath.Join(dir, "Packages"))
	for _, w := range []string{"Package: fzf\n", "Version: 3.1.1-2~dovy13\n", "X-Dovy-Recipe: pfetch\n", "Filename: ./pfetch_3.1.1-2~dovy13_amd64.deb\n", "SHA256: "} {
		if !strings.Contains(string(pkgs), w) {
			t.Errorf("Packages lacks %q:\n%s", w, pkgs)
		}
	}
	if strings.Contains(string(pkgs), "3.1.1-1~dovy13") {
		t.Error("superseded version listed")
	}
	h := sha256.Sum256(pkgs)
	rel, _ := os.ReadFile(filepath.Join(dir, "Release"))
	want := fmt.Sprintf(" %s %d Packages\n", hex.EncodeToString(h[:]), len(pkgs))
	if !strings.Contains(string(rel), want) || !strings.Contains(string(rel), "Label: dovy\n") {
		t.Errorf("Release:\n%s", rel)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) != 0 {
		t.Errorf("leftovers: %v", left)
	}
}
