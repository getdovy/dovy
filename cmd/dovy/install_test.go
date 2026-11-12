package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/getdovy/dovy/internal/apt"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/ui"
)

func TestResolve(t *testing.T) {
	rs := map[string]*catalog.Recipe{
		"nvflux": {Name: "nvflux", Depends: []string{"nvidia"}},
		"nvidia": {Name: "nvidia"},
		"zram":   {Name: "zram"},
	}
	order, missing := resolve(rs, []string{"nvflux", "zram", "nvidia"})
	var names []string
	for _, r := range order {
		names = append(names, r.Name)
	}
	if missing != "" || strings.Join(names, " ") != "nvidia nvflux zram" {
		t.Errorf("got %v %q", names, missing)
	}
	if _, missing := resolve(rs, []string{"vlc"}); missing != "vlc" {
		t.Errorf("missing %q", missing)
	}
}

func simulated(t *testing.T, name string) []apt.Action {
	t.Helper()
	b, err := os.ReadFile("../../internal/apt/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return apt.Parse(string(b))
}

func TestCheck(t *testing.T) {
	tx := &txn{actions: simulated(t, "sim-install.txt"), removing: map[string]string{}, down: map[string]bool{}}
	if s, k := tx.check(); len(s) != 0 || k != "" {
		t.Errorf("install: %v %q", s, k)
	}
	tx.actions = simulated(t, "sim-surprise.txt")
	if s, _ := tx.check(); len(s) != 3 || s[0] != "removes exim4-daemon-light 4.98.2-1+deb13u5" {
		t.Errorf("surprise: %v", s)
	}
	// A pair partner's removal is planned, not a surprise.
	tx.removing = map[string]string{"exim4-daemon-light": "", "exim4-config": "", "exim4-base": ""}
	if s, _ := tx.check(); len(s) != 0 {
		t.Errorf("planned removal: %v", s)
	}
	tx = &txn{actions: simulated(t, "sim-downgrade.txt"), removing: map[string]string{}, down: map[string]bool{}}
	if s, _ := tx.check(); len(s) != 7 {
		t.Errorf("unplanned downgrades: %v", s)
	}
	for _, p := range []string{"libgl1-mesa-dri", "libegl-mesa0", "libglx-mesa0", "libgbm1", "mesa-libgallium", "mesa-va-drivers", "mesa-vulkan-drivers"} {
		tx.down[p] = true
	}
	if s, _ := tx.check(); len(s) != 0 {
		t.Errorf("planned downgrades: %v", s)
	}
}

func TestRunningKernel(t *testing.T) {
	kernelRelease = "7.2.12+bpo-amd64"
	defer func() { kernelRelease = "" }()
	tx := &txn{removing: map[string]string{"dovy-kernel-backports": "purged"}, unused: map[string]bool{"linux-image-7.2.12+bpo-amd64": true},
		actions: apt.Parse("Remv dovy-kernel-backports [1-1~dovy13]\nRemv linux-image-7.2.12+bpo-amd64 [7.2.12-1~bpo13+1]\n")}
	if _, k := tx.check(); k != "linux-image-7.2.12+bpo-amd64" {
		t.Errorf("running kernel removal not caught: %q", k)
	}
	kernelRelease = "6.12.107+deb13-amd64"
	if _, k := tx.check(); k != "" {
		t.Errorf("other kernel refused: %q", k)
	}
}

func TestSame(t *testing.T) {
	a := simulated(t, "sim-install.txt")
	b := append([]apt.Action{a[len(a)-1]}, a[:len(a)-1]...) // order differs
	if !same(a, b, nil) {
		t.Error("reordered plan differs")
	}
	b[0].Version = "1-2~dovy13"
	if same(a, b, nil) {
		t.Error("changed version not caught")
	}
	if !same(a, b, map[string]bool{"dovy-cli-tools": true}) {
		t.Error("skipped package compared")
	}
}

func TestSections(t *testing.T) {
	tx := &txn{versions: map[string]string{"dovy-cli-tools": "1-1~dovy13"}, actions: simulated(t, "sim-install.txt")}
	secs, counts, totals := tx.sections(map[string]apt.Size{"ripgrep": {Download: 1433568, Installed: 5278 * 1024}}, nil)
	if len(secs) != 2 || secs[0].Title != "Installing" || len(secs[0].Rows) != 1 || len(secs[1].Rows) != 16 {
		t.Fatalf("got %+v", secs)
	}
	if r := secs[0].Rows[0]; r.Source != "dovy" || r.Size != "—" {
		t.Errorf("dovy row %+v", r)
	}
	if counts[1] != (ui.Count{Verb: "Dependencies", N: 16, Unit: "packages"}) || totals[0].Reason != "1.4 MiB" {
		t.Errorf("summary %+v %+v", counts, totals)
	}
	var b bytes.Buffer
	u := &ui.UI{Out: &b, Width: 80}
	u.Plan(secs)
	if !strings.Contains(b.String(), "  dovy-cli-tools     1-1~dovy13") || !strings.Contains(b.String(), "ripgrep") {
		t.Errorf("plan:\n%s", b.String())
	}

	tx = &txn{versions: map[string]string{}, actions: simulated(t, "sim-downgrade.txt"), down: map[string]bool{},
		inst: map[string]pkgInfo{"libgbm1": {Version: "26.1.6-1~bpo13+1", Size: 50 << 10}}}
	secs, _, totals = tx.sections(map[string]apt.Size{"libgbm1": {Download: 45 << 10, Installed: 44 << 10}}, nil)
	if totals[1] != (ui.Pair{Item: "Disk freed", Reason: "6 KiB"}) {
		t.Errorf("downgrade disk %+v", totals)
	}
	if len(secs) != 2 || secs[0].Title != "Installing dependencies" || secs[1].Title != "Downgrading" || len(secs[1].Rows) != 7 ||
		secs[1].Rows[0].Note != "from 26.1.6-1~bpo13+1" {
		t.Errorf("downgrades %+v", secs)
	}
}

func TestCancel(t *testing.T) {
	var b bytes.Buffer
	u := &ui.UI{Out: &b, Width: 80}
	if u.Confirm(strings.NewReader("\n")) || !u.Confirm(strings.NewReader("y\n")) {
		t.Error("Enter must mean no, y yes")
	}
}

func TestUnused(t *testing.T) {
	got := unused([]string{"old-lib"}, []string{"bat", "old-lib", "ripgrep", "dovy-cli-tools"}, map[string]bool{"dovy-cli-tools": true})
	if strings.Join(got, " ") != "bat ripgrep" {
		t.Errorf("got %v", got)
	}
}

// A recipe's own packages are only expected to go; apt is still asked.
func TestAsked(t *testing.T) {
	tx := &txn{removing: map[string]string{"dovy-archivers": "purged", "7zip": "with archivers"},
		request: []string{"dovy-archivers-", "linux-base=4.12.1"}}
	got := unused(nil, []string{"7zip", "dovy-archivers", "linux-base", "zstd"}, tx.asked())
	if strings.Join(got, " ") != "7zip zstd" {
		t.Errorf("got %v", got)
	}
}

func TestPkgName(t *testing.T) {
	for in, want := range map[string]string{"mesa-libgallium/trixie-backports": "mesa-libgallium", "linux-base=4.12.1": "linux-base",
		"libgl1-mesa-dri:i386-": "libgl1-mesa-dri:i386", "dovy-mesa-": "dovy-mesa"} {
		if got := pkgName(in); got != want {
			t.Errorf("%s: got %s", in, got)
		}
	}
}
