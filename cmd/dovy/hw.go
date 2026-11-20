package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/fetch"
	"github.com/getdovy/dovy/internal/hardware"
	"github.com/getdovy/dovy/internal/ui"
)

// Set in test builds to fake sysfs and EFI variables: -X main.sysRoot=/fake.
var sysRoot = "/"

// openModule is the package whose planned version picks NVIDIA's table.
const openModule = "nvidia-kernel-open-dkms"

// unavailable runs r's hardware check, or an inherited one, without the network.
func unavailable(r *catalog.Recipe, rs map[string]*catalog.Recipe) string {
	seen := map[string]bool{}
	var walk func(r *catalog.Recipe) string
	walk = func(r *catalog.Recipe) string {
		if r == nil || seen[r.Name] {
			return ""
		}
		seen[r.Name] = true
		if why := machine(r); why != "" {
			return r.Name + ": " + why
		}
		for _, d := range r.Depends {
			if why := walk(rs[d]); why != "" {
				return why
			}
		}
		return ""
	}
	return walk(r)
}

func machine(r *catalog.Recipe) string {
	if r.Kind == catalog.LocalBuild && r.Input == "" { // set by load
		return "no " + strings.Join(r.Local.Match, ", ") + " files in " + r.Local.Path
	}
	if r.Hardware != "nvidia-turing" {
		return ""
	}
	gs, err := hardware.GPUs(sysRoot)
	if err != nil {
		return "cannot read PCI devices: " + err.Error()
	}
	if len(hardware.Of(gs, hardware.NVIDIA)) == 0 {
		return "no NVIDIA GPU"
	}
	return ""
}

// turing checks every NVIDIA GPU against the open-module table at the version
// apt plans to install (ADR 0017).
func (t *txn) turing() (string, error) {
	need := false
	for _, r := range t.build {
		need = need || r.Hardware == "nvidia-turing"
	}
	if !need {
		return "", nil
	}
	v := ""
	for _, x := range t.actions {
		if x.Op == "Inst" && x.Name == openModule {
			v = x.Version
		}
	}
	if v == "" {
		v = t.inst[openModule].Version
	}
	if v == "" {
		return "", fmt.Errorf("the plan has no %s", openModule)
	}
	v = upstream(v)
	b, err := fetch.Bytes(hardware.ReadmeURL(v), 4<<20)
	if err != nil {
		return "", fmt.Errorf("NVIDIA's GPU list: %w", err)
	}
	tb, err := hardware.ParseTable(string(b))
	if err != nil {
		return "", err
	}
	gs, err := hardware.GPUs(sysRoot)
	if err != nil {
		return "", err
	}
	if u := hardware.Uncovered(gs, tb); len(u) > 0 {
		var s []string
		for _, g := range u {
			s = append(s, g.String())
		}
		return "not Turing or newer, so not covered by NVIDIA's open driver " + v + "|" + strings.Join(s, ", "), nil
	}
	return "", nil
}

// dovy runs nothing for Secure Boot; it only explains what recipes need.
func secureBoot(rs []*catalog.Recipe) []ui.Pair {
	var p []ui.Pair
	if !hardware.SecureBoot(sysRoot) {
		return nil
	}
	for _, r := range rs {
		switch r.SecureBoot {
		case "dkms":
			if enrolled("/var/lib/dkms/mok.pub") {
				continue
			}
			p = append(p, ui.Pair{Item: r.Name, Reason: "Secure Boot is on: enroll DKMS's key once, " +
				"sudo mokutil --import /var/lib/dkms/mok.pub, then reboot and confirm it in the blue MOK screen"})
		case "unsigned-kernel":
			p = append(p, ui.Pair{Item: r.Name, Reason: "Secure Boot is on: this kernel is unsigned; turn Secure Boot off in the firmware to boot it"})
		}
	}
	return p
}

// enrolled reports whether a key is already enrolled, when mokutil can say.
func enrolled(key string) bool {
	out, _ := exec.Command("mokutil", "--test-key", key).CombinedOutput()
	return strings.Contains(string(out), "is already enrolled")
}

// Removals that an unpinned repository's package conflicts with, breaks or
// replaces are expected (ADR 0017).
func (t *txn) platform() {
	if len(t.unpinned) == 0 {
		return
	}
	origins := map[string]bool{}
	for _, h := range t.unpinned {
		if o := originOf(t.listsDir(), h); o != "" {
			origins[o] = true
		}
	}
	claimed := map[string]bool{}
	var upgraded []string
	for _, x := range t.actions {
		if x.Op != "Inst" || !origins[x.Origin] {
			continue
		}
		if x.Old != "" {
			upgraded = append(upgraded, x.Name)
		}
		out, _ := exec.Command("apt-cache", append(append([]string{}, t.opts...), "show", x.Name+"="+x.Version)...).Output()
		for _, l := range strings.Split(string(out), "\n") {
			for _, f := range []string{"Conflicts:", "Breaks:", "Replaces:"} {
				if v, ok := strings.CutPrefix(l, f); ok {
					for _, n := range relNames(v) {
						claimed[n] = true
					}
				}
			}
		}
	}
	manual := manualPkgs()
	// What depends on a package NVIDIA replaces or upgrades goes with it
	// (Debian's exact-version driver libraries), through chains.
	for _, n := range upgraded {
		claimed[strings.SplitN(n, ":", 2)[0]] = true
	}
	for changed := true; changed; {
		changed = false
		for _, x := range t.actions {
			n := strings.SplitN(x.Name, ":", 2)[0]
			if (x.Op != "Remv" && x.Op != "Purg") || claimed[n] {
				continue
			}
			out, _ := exec.Command("dpkg-query", "-W", "-f", "${Depends}, ${Pre-Depends}", x.Name).Output()
			for _, d := range relNames(string(out)) {
				if claimed[d] {
					claimed[n], changed = true, true
				}
			}
		}
	}
	for _, n := range upgraded {
		delete(claimed, strings.SplitN(n, ":", 2)[0]) // upgraded, not removed
	}
	var record []string
	for _, x := range t.actions {
		if (x.Op == "Remv" || x.Op == "Purg") && claimed[strings.SplitN(x.Name, ":", 2)[0]] {
			if _, ok := t.removing[x.Name]; !ok {
				t.removing[x.Name] = "replaced by NVIDIA"
			}
			record = append(record, x.Name+" "+mark(manual[x.Name]))
		}
	}
	for _, n := range upgraded { // shared names: removal takes them (ADR 0017)
		record = append(record, n+" "+mark(manual[n]))
		if manual[n] {
			if t.restored == nil {
				t.restored = map[string]string{}
			}
			t.restored[n] = "auto"
		}
	}
	for _, r := range t.build {
		if r.Repo != nil && !r.Repo.Pinned() {
			r.Replaced = record
		}
	}
}

func mark(manual bool) string {
	if manual {
		return "manual"
	}
	return "auto"
}

func (t *txn) listsDir() string {
	for _, o := range t.opts {
		if v, ok := strings.CutPrefix(o, "Dir::State::Lists="); ok {
			return v
		}
	}
	return "/var/lib/apt/lists"
}

// apt's simulation names a source by its Release Label, or Origin without one.
func originOf(lists, host string) string {
	m, _ := filepath.Glob(filepath.Join(lists, host+"_*Release"))
	for _, p := range m {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		origin := ""
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "Label:"); ok {
				return strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(l, "Origin:"); ok {
				origin = strings.TrimSpace(v)
			}
		}
		if origin != "" {
			return origin
		}
	}
	return ""
}

func relNames(v string) []string {
	var n []string
	for _, rel := range strings.Split(v, ",") {
		for _, alt := range strings.Split(rel, "|") {
			if f := strings.Fields(alt); len(f) > 0 {
				n = append(n, strings.SplitN(f[0], ":", 2)[0])
			}
		}
	}
	return n
}
