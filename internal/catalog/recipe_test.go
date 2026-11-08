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
		"script name":      {map[string]string{"steam": steam + "\n[scripts]\nfoo = \"\"\n"}, `unknown script "foo"`},
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
	recipe := func(name, repo string) string {
		return "name = \"" + name + "\"\ndescription = \"d\"\nkind = \"vendor-repository\"\nversion = \"1\"\nmaintainer = \"m\"\ndebian = [13]\nrepository = \"" + repo + "\"\npackages = [\"" + name + "\"]\n"
	}
	repo := func(name, uri, extra string) string {
		return "name = \"" + name + "\"\nuri = \"" + uri + "\"\nsuite = \"stable\"\ncomponents = [\"main\"]\nkey-url = \"https://x/k\"\nmaintainer = \"m\"\n" +
			"fingerprints = [\"DBF1A116C220B8C7164F98230686B78420038257\", \"47D32A74E9A9E013A4B4926C68D513D36A73CD96\"]\n" + extra
	}
	load := func(recipes, repos map[string]string) (map[string]*Recipe, error) {
		dir := catalogDir(t, recipes)
		os.MkdirAll(filepath.Join(dir, "repositories"), 0o755)
		for n, body := range repos {
			os.WriteFile(filepath.Join(dir, "repositories", n+".toml"), []byte(body), 0o644)
		}
		return Load(dir, noDebian)
	}
	brave := repo("brave", "https://brave-browser-apt-release.s3.brave.com/", "[[file]]\npath = \"/etc/default/brave-browser\"\ncontent = \"x\"\n")
	rs, err := load(map[string]string{"brave-browser": recipe("brave-browser", "brave"), "brave-origin": recipe("brave-origin", "brave")}, map[string]string{"brave": brave})
	if err != nil || rs["brave-browser"].Repo != rs["brave-origin"].Repo || rs["brave-origin"].Repo.Name != "brave" {
		t.Fatalf("shared repository: %v", err)
	}
	op, err := load(map[string]string{"opera-stable": recipe("opera-stable", "opera") + "[scripts]\npostinst = \"x\"\nconfig = \"\"\n"},
		map[string]string{"opera": repo("opera", "https://deb.opera.com/opera-stable/", "repack = true\n")})
	if err != nil || op["opera-stable"].Package() != "opera-stable" {
		t.Errorf("repacked: %v", err)
	}
	flat := strings.Replace(strings.Replace(repo("floorp", "https://ppa.floorp.app/amd64", ""), `suite = "stable"`, `suite = "./"`, 1), `components = ["main"]`+"\n", "", 1)
	if fl, err := load(nil, map[string]string{"floorp": flat}); err != nil {
		t.Errorf("flat: %v %v", fl, err)
	}
	cases := map[string]struct {
		recipes, repos map[string]string
		want           string
	}{
		"missing":   {map[string]string{"a": recipe("a", "nope")}, nil, `no repository "nope"`},
		"same host": {nil, map[string]string{"a": repo("a", "https://dl.google.com/a", ""), "b": repo("b", "https://dl.google.com/b", "")}, "dl.google.com already has a repository"},
		"file":      {nil, map[string]string{"a": repo("a", "https://a.example/", "[[file]]\npath = \"/etc/apt/x.list\"\ncontent = \"\"\n")}, "only /etc/default/<name>"},
		"name":      {nil, map[string]string{"a": repo("b", "https://a.example/", "")}, "does not match the file"},
		"fpr":       {nil, map[string]string{"a": strings.Replace(repo("a", "https://a.example/", ""), "DBF1A116", "dbf1a116", 1)}, "uppercase hex"},
		"http":      {nil, map[string]string{"a": repo("a", "http://a.example/", "")}, "https"},
		"flat":      {nil, map[string]string{"a": strings.Replace(repo("a", "https://a.example/", ""), `suite = "stable"`, `suite = "./"`, 1)}, "none for a flat suite"},
		"scripts":   {map[string]string{"a": recipe("a", "brave") + "[scripts]\npostinst = \"\"\n"}, map[string]string{"brave": brave}, "only for a repacked repository"},
		"repack":    {map[string]string{"a": strings.Replace(recipe("a", "op"), `packages = ["a"]`, `packages = ["b"]`, 1)}, map[string]string{"op": repo("op", "https://deb.opera.com/x/", "repack = true\n")}, "must be the recipe's name"},
		"config":    {map[string]string{"a": recipe("a", "op") + "[scripts]\nconfig = \"x\"\n"}, map[string]string{"op": repo("op", "https://deb.opera.com/x/", "repack = true\n")}, "config can only be dropped"},
	}
	for name, c := range cases {
		if _, err := load(c.recipes, c.repos); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
