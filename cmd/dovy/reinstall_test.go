package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/repo"
)

// controlDeb builds a .deb with only a control file.
func controlDeb(t *testing.T, name, version, recipe string) string {
	t.Helper()
	stage := t.TempDir()
	os.MkdirAll(filepath.Join(stage, "DEBIAN"), 0o755)
	control := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: amd64\nMaintainer: test\nX-Dovy-Recipe: %s\nDescription: test\n", name, version, recipe)
	os.WriteFile(filepath.Join(stage, "DEBIAN", "control"), []byte(control), 0o644)
	deb := filepath.Join(t.TempDir(), fmt.Sprintf("%s_%s_amd64.deb", name, version))
	if b, err := exec.Command("dpkg-deb", "--root-owner-group", "--build", stage, deb).CombinedOutput(); err != nil {
		t.Fatalf("%s", b)
	}
	return deb
}

func TestAgain(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("no dpkg-deb")
	}
	const v = "7.2.12-1~dovy13"
	dir := filepath.Join(t.TempDir(), "repo")
	old := localRepo
	localRepo = dir
	defer func() { localRepo = old }()
	if err := repo.Publish(dir, []string{controlDeb(t, "kernel-dovy", v, "kernel-dovy"), controlDeb(t, "linux-image-7.2.12-dovy", v, "kernel-dovy")}, repo.Direct); err != nil {
		t.Fatal(err)
	}
	r := &catalog.Recipe{Name: "kernel-dovy", Kind: catalog.UpstreamBinary}
	inst := map[string]pkgInfo{
		"kernel-dovy":             {Version: v, Recipe: "kernel-dovy"},
		"linux-image-7.2.12-dovy": {Version: v, Recipe: "kernel-dovy"},
		"zram":                    {Version: "1-1~dovy13", Recipe: "zram"},
	}

	// Every copy intact: reinstalled from the repository, parts named.
	tx := &txn{versions: map[string]string{}}
	tx.again(r, v, inst)
	if !tx.reinstall["kernel-dovy"] || !tx.reinstall["linux-image-7.2.12-dovy"] || tx.reinstall["zram"] || !tx.fromRepo["kernel-dovy"] {
		t.Fatalf("reinstall %v, from repo %v", tx.reinstall, tx.fromRepo)
	}
	if tx.prebuilt["kernel-dovy"] != filepath.Join(dir, "kernel-dovy_"+v+"_amd64.deb") ||
		tx.parts["linux-image-7.2.12-dovy"] != filepath.Join(dir, "linux-image-7.2.12-dovy_"+v+"_amd64.deb") || tx.versions["linux-image-7.2.12-dovy"] != v {
		t.Errorf("copies: %v %v %v", tx.prebuilt, tx.parts, tx.versions)
	}
	if strings.Join(tx.flags(), " ") != "--allow-downgrades --reinstall" {
		t.Errorf("flags %v", tx.flags())
	}

	// One copy missing: built again, as on a first install.
	os.Remove(filepath.Join(dir, "linux-image-7.2.12-dovy_"+v+"_amd64.deb"))
	tx = &txn{versions: map[string]string{}}
	tx.again(r, v, inst)
	if !tx.reinstall["linux-image-7.2.12-dovy"] || tx.fromRepo["kernel-dovy"] || tx.prebuilt["kernel-dovy"] != "" || len(tx.parts) != 0 {
		t.Errorf("partial copies used: %v %v %v", tx.fromRepo, tx.prebuilt, tx.parts)
	}
	if strings.Join((&txn{}).flags(), " ") != "--allow-downgrades" {
		t.Error("--reinstall without a reinstall")
	}
}

func TestReinstallPlan(t *testing.T) {
	sim := "Inst kernel-dovy [7.2.12-1~dovy13] (7.2.12-1~dovy13 local-deb [amd64])\nConf kernel-dovy (7.2.12-1~dovy13 local-deb [amd64])\n"
	tx := &txn{actions: apt.Parse(sim), removing: map[string]string{}, down: map[string]bool{},
		versions: map[string]string{"kernel-dovy": "7.2.12-1~dovy13"}, reinstall: map[string]bool{"kernel-dovy": true},
		inst: map[string]pkgInfo{"kernel-dovy": {Version: "7.2.12-1~dovy13", Recipe: "kernel-dovy"}}}
	if s, k := tx.check(); len(s) != 0 || k != "" {
		t.Errorf("surprises in a reinstall: %v %q", s, k)
	}
	secs, _, _ := tx.sections(nil, nil)
	if len(secs) != 1 || len(secs[0].Rows) != 1 || secs[0].Rows[0].Note != "reinstall" {
		t.Errorf("plan: %+v", secs)
	}
}

func TestRetry(t *testing.T) {
	tx := &txn{verb: "install", options: []string{"--reinstall", "--restore-config"}, names: []string{"zram", "fzf"}}
	if got := tx.retry(); got != "dovy install --reinstall --restore-config zram fzf" {
		t.Errorf("got %q", got)
	}
}

func TestStale(t *testing.T) {
	keep := strings.Repeat("a", 64)
	old := strings.Repeat("b", 64)
	got := stale([]string{keep, old, ".part-123", strings.Repeat("B", 64), "notes"}, map[string]bool{keep: true})
	if strings.Join(got, " ") != old {
		t.Errorf("got %v", got)
	}
}

func TestDropBuilt(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	debs := filepath.Join(cache, "dovy", "debs")
	os.MkdirAll(debs, 0o755)
	built, part := filepath.Join(debs, "a.deb"), filepath.Join(debs, "b.deb")
	kept := filepath.Join(t.TempDir(), "repo-copy.deb") // the repository's, used by a reinstall
	for _, p := range []string{built, part, kept} {
		os.WriteFile(p, nil, 0o644)
	}
	tx := &txn{prebuilt: map[string]string{"a": built, "c": kept}, parts: map[string]string{"b": part}}
	tx.dropBuilt() // a plan not carried out: everything it made
	for _, p := range []string{built, part} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s kept", p)
		}
	}
	if _, err := os.Stat(kept); err != nil {
		t.Error("deleted a file outside the cache")
	}
	os.WriteFile(built, nil, 0o644)
	os.WriteFile(part, nil, 0o644)
	(&txn{}).dropBuilt(built) // a run that succeeded: what it published
	if _, err := os.Stat(built); err == nil {
		t.Error("published build kept")
	}
	if _, err := os.Stat(part); err != nil {
		t.Error("deleted a build not given")
	}
}
