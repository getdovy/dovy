package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// The golden files are the agreed mockups (dovy-design/mockups v5, v6)
// printed with NO_COLOR.
func golden(t *testing.T, name string, got string) {
	t.Helper()
	want, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs\n--- got\n%s--- want\n%s", name, got, want)
	}
}

func newUI(w int) (*UI, *bytes.Buffer) {
	var b bytes.Buffer
	return &UI{Out: &b, Width: w}, &b
}

var deps = []Row{
	{"libasound2t64:i386", "1.2.14-1", "Debian", "412 KiB", ""},
	{"libc6:i386", "2.41-12", "Debian", "2.7 MiB", ""},
	{"libdrm2:i386", "2.4.124-2", "Debian", "39 KiB", ""},
	{"libgcc-s1:i386", "14.2.0-19", "Debian", "72 KiB", ""},
	{"libgl1:i386", "1.7.0-1+b2", "Debian", "111 KiB", ""},
	{"libllvm19:i386", "1:19.1.7-3+b1", "Debian", "29.4 MiB", ""},
	{"libstdc++6:i386", "14.2.0-19", "Debian", "732 KiB", ""},
	{"libvulkan1:i386", "1.4.309.0-1", "Debian", "151 KiB", ""},
	{"libx11-6:i386", "2:1.8.12-1", "Debian", "845 KiB", ""},
	{"mesa-vulkan-drivers:i386", "25.0.7-2", "Debian", "11.6 MiB", ""},
	{"nvidia-driver-libs:i386", "590.48.01-1", "NVIDIA", "98.3 MiB", ""},
	{"nvidia-kernel-open-dkms", "590.48.01-1", "NVIDIA", "12.0 MiB", ""},
}

func TestPlanWide(t *testing.T) {
	u, b := newUI(80)
	u.Plan([]Section{
		{Yellow, "Replacing", []Row{{"nvidia-open", "590.48.01-1", "NVIDIA", "1.2 MiB", "replaces nvidia-driver 550.163-1 (Debian)"}}},
		{Green, "Installing", []Row{{"steam", "1.0.0.85-1~dovy1", "dovy", "4.2 MiB", ""}}},
		{Green, "Installing dependencies", deps},
	})
	u.Pairs(Cyan, "System changes", []Pair{
		{"i386 architecture", "steam, nvidia"}, {"contrib, non-free", "steam"}, {"NVIDIA repository", "nvidia"},
	})
	u.Summary(
		[]Count{{"Replace", 1, "package"}, {"Install", 1, "package"}, {"Dependencies", 12, "packages"}, {"System", 3, "changes"}},
		[]Pair{{"Download", "157 MiB"}, {"Disk required", "512 MiB"}},
	)
	golden(t, "plan_wide.golden", b.String())
}

func TestPlanNarrow(t *testing.T) {
	u, b := newUI(60)
	unused := []Row{deps[0], deps[2], deps[4], deps[5], deps[7], deps[8], deps[9]}
	u.Plan([]Section{
		{Red, "Removing", []Row{{"steam", "1.0.0.85-1~dovy1", "dovy", "4.2 MiB", "purged"}}},
		{Red, "Removing unused dependencies", unused},
	})
	u.Pairs(Cyan, "Kept", []Pair{
		{"i386 architecture", "needed by nvidia"}, {"vlc", "installed by user"},
		{"config of 7 packages", "in /etc; apt purge removes it"},
	})
	u.Summary(
		[]Count{{"Remove", 1, "package"}, {"Dependencies", 7, "packages"}},
		[]Pair{{"Disk freed", "42 MiB"}},
	)
	golden(t, "plan_narrow.golden", b.String())
}

func TestList(t *testing.T) {
	u, b := newUI(80)
	u.Grid("Catalog (7 of 48)", [4]string{"Recipe", "Version", "Status", "Note"}, [][4]string{
		{"config-amdgpu", "1.0", "not available", "needs an AMD GPU"},
		{"discord", "0.0.112", "available", ""},
		{"firefox", "145.0", "available", "Mozilla repository"},
		{"mesa", "25.0.7-2", "available", "pair: mesa-backports"},
		{"mesa-backports", "25.2.4-1~bpo13+1", "installed", ""},
		{"steam", "1.0.0.85-1~dovy1", "update 1.0.0.86", ""},
		{"yad", "14.2-1~dovy1", "orphaned", "dropped from the catalog"},
	})
	golden(t, "list.golden", b.String())
}

func TestError(t *testing.T) {
	u, b := newUI(80)
	u.Fail("build of steam (tar exit 2)", "unchanged; nothing was installed",
		"free disk space, then: dovy install steam", "/var/log/dovy/2026-11-05T18:02:11.log")
	golden(t, "error.golden", b.String())
}

func TestColorOff(t *testing.T) {
	u, b := newUI(80)
	u.Say(Green, "Unpacking:", "steam (1.0)")
	if strings.Contains(b.String(), "\033") {
		t.Error("escape codes with color off")
	}
	if b.String() != "Unpacking:  steam (1.0)\n" {
		t.Errorf("got %q", b.String())
	}
}

func TestConfirm(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "yes\n": true, "\n": false, "n\n": false, "": false, "nope\n": false} {
		u, _ := newUI(80)
		if got := u.Confirm(strings.NewReader(in)); got != want {
			t.Errorf("Confirm(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 421888: "412 KiB", 4404019: "4.2 MiB", 3 << 30: "3.0 GiB"} {
		if got := Bytes(n); got != want {
			t.Errorf("%d: got %s, want %s", n, got, want)
		}
	}
}
