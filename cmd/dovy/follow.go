package main

import (
	"strings"

	"github.com/getdovy/dovy/internal/apt"
)

// follow keeps packages apt would remove only because they pin the exact version
// of a package moving between stable and backports (firmware-linux-nonfree).
func (t *txn) follow(stubs []string) error {
	if t.verb == "upgrade" { // Debian's dist-upgrade removes as apt does
		return nil
	}
	suite := ""
	for _, r := range t.request {
		if _, s, ok := strings.Cut(r, "/"); ok && strings.HasSuffix(s, "-backports") {
			suite = s
		}
	}
	for round := 0; round < 4; round++ {
		asked := t.asked()
		var toBpo, toStable []string
		for _, x := range t.actions {
			if x.Op != "Remv" && x.Op != "Purg" {
				continue
			}
			if _, ok := t.removing[x.Name]; ok || t.unused[x.Name] || asked[x.Name] {
				continue
			}
			switch {
			case strings.Contains(x.Old, "~bpo") && len(t.down) > 0:
				toStable = append(toStable, x.Name)
			case suite != "" && !strings.Contains(x.Old, "~bpo") && !strings.Contains(x.Old, "~dovy"):
				toBpo = append(toBpo, x.Name)
			}
		}
		n := len(t.request)
		down, removing := copySet(t.down), map[string]string{}
		for k, v := range t.removing {
			removing[k] = v
		}
		bpo := apt.Backports(t.opts, toBpo)
		for _, p := range toBpo {
			if bpo[p] != "" {
				t.request = append(t.request, p+"/"+suite)
			}
		}
		if len(toStable) > 0 {
			t.leaveBackports(toStable, nil, t.inst, nil)
		}
		if len(t.request) == n {
			return nil
		}
		out, err := apt.Simulate(t.opts, t.args(stubs)...)
		if err != nil { // apt cannot move them: the removals stay, and are refused
			t.request, t.down, t.removing = t.request[:n], down, removing
			return nil
		}
		t.actions = apt.Parse(out)
	}
	return nil
}

func copySet(m map[string]bool) map[string]bool {
	c := map[string]bool{}
	for k, v := range m {
		c[k] = v
	}
	return c
}
