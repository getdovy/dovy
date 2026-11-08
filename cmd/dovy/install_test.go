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

func TestSurpriseAndSame(t *testing.T) {
	if s := surprise(simulated(t, "sim-install.txt")); len(s) != 0 {
		t.Errorf("install: %v", s)
	}
	s := surprise(simulated(t, "sim-surprise.txt"))
	if len(s) != 3 || s[0] != "exim4-daemon-light 4.98.2-1+deb13u5" {
		t.Errorf("surprise: %v", s)
	}
	a := simulated(t, "sim-install.txt")
	b := append([]apt.Action{a[len(a)-1]}, a[:len(a)-1]...) // order differs
	if !same(a, b) {
		t.Error("reordered plan differs")
	}
	b[0].Version = "1-2~dovy13"
	if same(a, b) {
		t.Error("changed version not caught")
	}
}

func TestSections(t *testing.T) {
	p := &plan{versions: map[string]string{"dovy-cli-tools": "1-1~dovy13"}, actions: simulated(t, "sim-install.txt")}
	secs, counts, totals := p.sections(map[string]apt.Size{"ripgrep": {Download: 1433568, Installed: 5278 * 1024}}, nil)
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
