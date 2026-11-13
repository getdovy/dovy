package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getdovy/dovy/internal/ui"
)

func testApp(t *testing.T, inst map[string]string) (*app, *bytes.Buffer) {
	t.Helper()
	var b bytes.Buffer
	u := &ui.UI{Out: &b, Width: 80}
	return &app{
		out: u, errs: u,
		dir:       filepath.Join(t.TempDir(), "catalog"),
		installed: func() (map[string]string, error) { return inst, nil },
	}, &b
}

func TestRun(t *testing.T) {
	cases := []struct {
		args string
		euid int
		code int
		out  string
	}{
		{"", 1000, exitUsage, "usage: dovy <command>"},
		{"--help", 1000, exitOK, "Commands:"},
		{"--version", 1000, exitOK, "dovy 0.0.0-dev"},
		{"frobnicate", 1000, exitUsage, "unknown command frobnicate"},
		{"install", 1000, exitUsage, "needs at least one recipe"},
		{"install steam --yes", 1000, exitUsage, "unknown option --yes"},
		{"install --help", 1000, exitOK, "usage: dovy install <recipe>..."},
		{"update now", 1000, exitUsage, "takes no arguments"},
		{"list --installed --updates", 1000, exitUsage, "one filter at most"},
		{"list --all", 1000, exitUsage, "unknown filter --all"},
		{"search", 1000, exitUsage, "dovy list shows everything"},
		{"search nvidia --installed", 1000, exitUsage, "takes one word"},
		{"search --installed", 1000, exitUsage, "takes no options"},
		{"show", 1000, exitUsage, "needs one recipe"},
		{"show steam --log", 1000, exitUsage, "unknown option --log"},
		{"show steam --recipe --diff", 1000, exitUsage, "one option at most"},
		{"install steam", 0, exitRefused, "dovy runs as root"},
		{"list", 0, exitRefused, "dovy runs as root"},
		{"install steam", 1000, exitFailed, "no catalog yet"},
		{"remove steam", 1000, exitFailed, "no catalog yet"},
		{"check", 1000, exitFailed, "Problem:"},
		{"list", 1000, exitFailed, "no catalog yet"},
	}
	for _, c := range cases {
		a, b := testApp(t, nil)
		code := run(strings.Fields(c.args), a, c.euid)
		if code != c.code {
			t.Errorf("dovy %s: exit %d, want %d", c.args, code, c.code)
		}
		if !strings.Contains(b.String(), c.out) {
			t.Errorf("dovy %s: output lacks %q:\n%s", c.args, c.out, b.String())
		}
	}
}

// fixtureURL is a test catalog bundle: v1, v2, mixed or bad.
func fixtureURL(name string) string {
	p, _ := filepath.Abs("../../internal/catalog/testdata/catalog-" + name + ".bundle")
	return p
}

// withCatalog returns an app whose catalog is the named fixture bundle.
func withCatalog(t *testing.T, name string, inst map[string]string) (*app, *bytes.Buffer) {
	t.Helper()
	bundle := fixtureURL(name)
	signers, err := os.ReadFile("../../internal/catalog/testdata/allowed_signers")
	if _, e := os.Stat(bundle); e != nil || err != nil {
		t.Skip("no fixture: run internal/catalog/testdata/make-fixture.sh")
	}
	a, b := testApp(t, inst)
	a.url, a.signers = bundle, signers
	if code := run([]string{"update"}, a, 1000); code != exitOK {
		t.Fatalf("update: exit %d\n%s", code, b)
	}
	return a, b
}

func TestCommands(t *testing.T) {
	a, b := withCatalog(t, "v1", map[string]string{"steam": "1.0.0.84-1~dovy1", "yad": "14.2-1~dovy1"})
	for _, w := range []string{"Release:     catalog-2026.01.10.1", "Added:       steam 1.0.0.85"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("update output lacks %q:\n%s", w, b)
		}
	}
	cases := []struct {
		args string
		code int
		want []string
	}{
		{"list", exitOK, []string{"Catalog (4)", "firefox", "packages.mozilla.org repository", "update 1.0.0.85", "orphaned"}},
		{"list --updates", exitOK, []string{"Updates (1)", "steam", "Apply: dovy upgrade"}},
		{"list --orphaned", exitOK, []string{"Orphaned (1)", "yad", "dropped from the catalog"}},
		{"list --installed", exitOK, []string{"Installed (2)"}},
		{"search STEAM", exitOK, []string{"1 result", "Valve's Steam client"}},
		{"search swap", exitOK, []string{"1 result", "zram"}},
		{"search nothing-like-this", exitOK, []string{"No recipe matches"}},
		{"show steam", exitOK, []string{"1.0.0.84-1~dovy1 (installed), 1.0.0.85 available", "i386, components"}},
		{"show steam --recipe", exitOK, []string{"steam/recipe.toml", "sha256 = "}},
		{"show zram --diff", exitFailed, []string{"zram is not installed"}},
		{"show vlc", exitFailed, []string{"no recipe vlc", "dovy search vlc"}},
	}
	for _, c := range cases {
		b.Reset()
		if code := run(strings.Fields(c.args), a, 1000); code != c.code {
			t.Errorf("dovy %s: exit %d, want %d\n%s", c.args, code, c.code, b)
		}
		for _, w := range c.want {
			if !strings.Contains(b.String(), w) {
				t.Errorf("dovy %s: lacks %q:\n%s", c.args, w, b)
			}
		}
	}

	// update to v2, then a tampered file is refused.
	b.Reset()
	a.url = fixtureURL("v2")
	run([]string{"update"}, a, 1000)
	for _, w := range []string{"Added:       discord", "Updated:     steam 1.0.0.85 -> 1.0.0.86", "Dropped:     zram"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("update to v2 lacks %q:\n%s", w, b)
		}
	}
	// Newer untrusted releases are skipped with a warning.
	b.Reset()
	a.url = fixtureURL("mixed")
	if code := run([]string{"update"}, a, 1000); code != exitOK {
		t.Errorf("mixed update: exit %d\n%s", code, b)
	}
	for _, w := range []string{"catalog-2026.03.10.2 skipped: not signed by a trusted key", "Catalog verified: catalog-2026.02.10.1. Nothing new."} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("mixed update lacks %q:\n%s", w, b)
		}
	}
	os.WriteFile(filepath.Join(a.dir, "recipes/steam/recipe.toml"), []byte("x"), 0o644)
	b.Reset()
	if code := run([]string{"list"}, a, 1000); code != exitFailed || !strings.Contains(b.String(), "differ from the signed catalog") {
		t.Errorf("tampered catalog: exit %d\n%s", code, b)
	}
}

func TestUpdateUntrusted(t *testing.T) {
	if _, err := os.Stat(fixtureURL("bad")); err != nil {
		t.Skip("no fixture: run internal/catalog/testdata/make-fixture.sh")
	}
	a, b := testApp(t, nil)
	a.url = fixtureURL("bad")
	a.signers, _ = os.ReadFile("../../internal/catalog/testdata/allowed_signers")
	if code := run([]string{"update"}, a, 1000); code != exitFailed {
		t.Errorf("exit %d, want %d", code, exitFailed)
	}
	for _, w := range []string{"unchanged; no catalog yet", "check the maintainer keys in /etc/dovy/keys.d"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("lacks %q:\n%s", w, b)
		}
	}
}
