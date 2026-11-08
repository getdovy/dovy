package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sha = "1111111111111111111111111111111111111111111111111111111111111111"

const steam = `name = "steam"
description = "Valve's Steam client"
kind = "upstream-binary"
version = "1.0.0.85"
maintainer = "test"
debian = [13]
requires = ["i386", "components"]

[[source]]
url = "https://example.org/steam.tar.gz"
sha256 = "` + sha + `"
`

// catalogDir writes recipes (name → recipe.toml) into a temporary catalog.
func catalogDir(t *testing.T, recipes map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range recipes {
		d := filepath.Join(dir, "recipes", name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "recipe.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// top adds top-level fields to steam, before its [[source]] table.
func top(extra string) string { return strings.Replace(steam, "\n[[source]]", extra+"\n[[source]]", 1) }

func noDebian(names []string) (map[string]bool, error) { return map[string]bool{}, nil }

func TestLoadValid(t *testing.T) {
	rs, err := Load(catalogDir(t, map[string]string{"steam": steam}), noDebian)
	if err != nil {
		t.Fatal(err)
	}
	if r := rs["steam"]; r == nil || r.Version != "1.0.0.85" || r.Package() != "steam" {
		t.Fatalf("got %+v", r)
	}
}

func TestLoadRefuses(t *testing.T) {
	setup := func(extra string) string {
		return "name = \"x\"\ndescription = \"X\"\nkind = \"setup\"\nversion = \"1\"\nmaintainer = \"t\"\ndebian = [13]\n" + extra
	}
	cases := map[string]struct {
		recipes map[string]string
		want    string
	}{
		"unknown field":    {map[string]string{"steam": top("colour = \"red\"\n")}, "unknown fields: colour"},
		"folder mismatch":  {map[string]string{"valve": steam}, "does not match the folder"},
		"bad sha256":       {map[string]string{"steam": strings.Replace(steam, sha, "abc", 1)}, "sha256 must be 64"},
		"http source":      {map[string]string{"steam": strings.Replace(steam, "https://", "http://", 1)}, "https only"},
		"latest":           {map[string]string{"steam": strings.Replace(steam, `"1.0.0.85"`, `"latest"`, 1)}, "exact version"},
		"debian 12":        {map[string]string{"steam": strings.Replace(steam, "[13]", "[12]", 1)}, "12 not supported"},
		"capability":       {map[string]string{"steam": strings.Replace(steam, `"components"`, `"multilib"`, 1)}, "unknown capability"},
		"period":           {map[string]string{"steam": strings.Replace(steam, `client"`, `client."`, 1)}, "no final period"},
		"wildcard":         {map[string]string{"x": setup(`packages = ["mesa-*"]`)}, "exact package name"},
		"home file":        {map[string]string{"x": setup("[[file]]\npath = \"/home/u/.bashrc\"\ncontent = \"\"\n")}, "only drop-ins"},
		"etc not drop-in":  {map[string]string{"x": setup("[[file]]\npath = \"/etc/bash.bashrc\"\ncontent = \"\"\n")}, "only drop-ins"},
		"dotdot":           {map[string]string{"x": setup("[[file]]\npath = \"/etc/sysctl.d/../passwd\"\ncontent = \"\"\n")}, "only drop-ins"},
		"revision":         {map[string]string{"steam": top("revision = -1\n")}, "revision: 1 or more"},
		"install outside":  {map[string]string{"steam": steam + "\n[install]\n\"/etc/x\" = \"x\"\n"}, "under /usr/"},
		"install dotdot":   {map[string]string{"steam": steam + "\n[install]\n\"/usr/bin/x\" = \"../x\"\n"}, "without .."},
		"arch suffix":      {map[string]string{"x": setup(`packages = ["libgl1:arm64"]`)}, "exact package name"},
		"script name":      {map[string]string{"steam": steam + "\n[scripts]\nconfig = \"\"\n"}, `unknown script "config"`},
		"script tarball":   {map[string]string{"steam": steam + "\n[scripts]\npostinst = \"\"\n"}, "only for one vendor .deb"},
		"wrong kind field": {map[string]string{"steam": top("members = [\"steam\"]\n")}, "members: not used by kind"},
		"empty setup":      {map[string]string{"x": setup("")}, "setup needs packages"},
		"missing depend":   {map[string]string{"steam": strings.Replace(steam, "requires", "depends = [\"nvidia\"]\nrequires", 1)}, `no recipe "nvidia"`},
		"one-way conflict": {map[string]string{
			"a": strings.Replace(setup(`packages = ["mesa"]`+"\nconflicts = [\"b\"]"), `"x"`, `"a"`, 1),
			"b": strings.Replace(setup(`packages = ["mesa"]`), `"x"`, `"b"`, 1),
		}, "b does not list a"},
		"cycle": {map[string]string{
			"a": strings.Replace(setup(`packages = ["p"]`+"\ndepends = [\"b\"]"), `"x"`, `"a"`, 1),
			"b": strings.Replace(setup(`packages = ["p"]`+"\ndepends = [\"a\"]"), `"x"`, `"b"`, 1),
		}, "dependency cycle: a → b → a"},
	}
	for name, c := range cases {
		_, err := Load(catalogDir(t, c.recipes), noDebian)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestReplaces(t *testing.T) {
	yadr := strings.NewReplacer(`"steam"`, `"yad"`, "Valve's Steam client", "Yet another dialog")
	yad := yadr.Replace(steam)
	debian := func(names []string) (map[string]bool, error) { return map[string]bool{"yad": true}, nil }
	dir := catalogDir(t, map[string]string{"yad": yad})
	if _, err := Load(dir, debian); err == nil || !strings.Contains(err.Error(), "list it in replaces") {
		t.Fatalf("got %v", err)
	}
	dir = catalogDir(t, map[string]string{"yad": yadr.Replace(top("replaces = [\"yad\"]\n"))})
	if _, err := Load(dir, debian); err != nil {
		t.Fatal(err)
	}
}

func TestPackageNames(t *testing.T) {
	for kind, want := range map[string]string{UpstreamBuild: "x", Setup: "dovy-x", VendorRepository: "dovy-x", Bundle: "dovy-x"} {
		if got := (&Recipe{Name: "x", Kind: kind}).Package(); got != want {
			t.Errorf("%s: got %s, want %s", kind, got, want)
		}
	}
}

func TestNeeds(t *testing.T) {
	r := &Recipe{Requires: []string{"components"}, Backports: true, Packages: []string{"steam-installer", "libgl1-mesa-dri:i386"}}
	if got := strings.Join(r.Needs(), " "); got != "backports components i386" {
		t.Errorf("got %s", got)
	}
	x := "name = \"x\"\ndescription = \"d\"\nkind = \"setup\"\nversion = \"1\"\nmaintainer = \"m\"\ndebian = [13]\npackages = [\"libgl1-mesa-dri:i386\"]\n"
	rs, err := Load(catalogDir(t, map[string]string{"x": x}), noDebian)
	if err != nil || rs["x"].Needs()[0] != "i386" {
		t.Errorf("i386 package: %v", err)
	}
}

func TestVendorRepository(t *testing.T) {
	repo := func(name, uri, extra string) string {
		return "name = \"" + name + "\"\ndescription = \"d\"\nkind = \"vendor-repository\"\nversion = \"1\"\nmaintainer = \"m\"\ndebian = [13]\n" + extra +
			"\n[repository]\nuri = \"" + uri + "\"\nsuite = \"stable\"\ncomponents = [\"main\"]\nkey-url = \"https://x/k\"\nfingerprint = \"EB4C1BFD4F042F6DDDCCEC917721F63BD38B4796\"\npackages = [\"p\"]\n"
	}
	file := func(path string) string { return "[[file]]\npath = \"" + path + "\"\ncontent = \"x\"\n" }
	ok := map[string]string{"google-chrome": repo("google-chrome", "https://dl.google.com/linux/chrome/deb/", file("/etc/default/google-chrome"))}
	if _, err := Load(catalogDir(t, ok), noDebian); err != nil {
		t.Errorf("valid: %v", err)
	}
	bad := map[string]map[string]string{
		"/etc/default/google-chrome": {"x": repo("x", "https://a.example/", file("/etc/apt/sources.list.d/x.list"))},
		"already has a repository":   {"a": repo("a", "https://dl.google.com/a", ""), "b": repo("b", "https://dl.google.com/b", "")},
	}
	want := map[string]string{"/etc/default/google-chrome": "writes only /etc/default/<name>", "already has a repository": "dl.google.com already has a repository recipe"}
	for k, rs := range bad {
		if _, err := Load(catalogDir(t, rs), noDebian); err == nil || !strings.Contains(err.Error(), want[k]) {
			t.Errorf("%s: %v", k, err)
		}
	}
}
