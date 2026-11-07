package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/repo"
)

// pfetch matches dovy-catalog's recipes/pfetch/recipe.toml.
const pfetch = `name = "pfetch"
description = "Pretty system information tool, Rust rewrite of pfetch"
kind = "upstream-binary"
version = "3.1.1"
maintainer = "hmwassim"
debian = [13]

[[source]]
url = "https://github.com/Gobidev/pfetch-rs/releases/download/v3.1.1/pfetch-linux-musl-x86_64.tar.gz"
sha256 = "1816aa30f763856403b1a4d91f73ffd79e96d3a0a5cef6f305f025253f6a3a6c"

[install]
"/usr/bin/pfetch" = "pfetch"
`

func TestRefusesOtherKinds(t *testing.T) {
	r := &catalog.Recipe{Name: "zram", Kind: catalog.Setup}
	if _, err := Recipe(r, 13, t.TempDir()); err == nil || !strings.Contains(err.Error(), "not built yet") {
		t.Errorf("got %v", err)
	}
}

// TestPfetch is milestone 2's proof. It downloads, uses sudo and writes
// /var/lib/dovy/repo: run only in a disposable Debian 13 system.
func TestPfetch(t *testing.T) {
	if os.Getenv("DOVY_INTEGRATION") != "1" {
		t.Skip("set DOVY_INTEGRATION=1 in a disposable Debian 13 system")
	}
	r := &catalog.Recipe{}
	if _, err := toml.Decode(pfetch, r); err != nil {
		t.Fatal(err)
	}
	release, err := deb.Release("/etc/os-release")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Recipe(r, release, cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(repo.Dir, []string{p}, repo.Sudo); err != nil {
		t.Fatal(err)
	}

	run := func(name string, args ...string) string {
		b, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, b)
		}
		t.Logf("$ %s %s\n%s", name, strings.Join(args, " "), b)
		return string(b)
	}
	want := func(out string, ws ...string) {
		t.Helper()
		for _, w := range ws {
			if !strings.Contains(out, w) {
				t.Errorf("lacks %q", w)
			}
		}
	}
	published := filepath.Join(repo.Dir, filepath.Base(p))
	want(run("dpkg-deb", "-f", published), "Package: pfetch", "Version: 3.1.1-1~dovy13", "X-Dovy-Recipe: pfetch")
	want(run("dpkg-deb", "-c", published), "-rwxr-xr-x root/root", "./usr/bin/pfetch")
	want(run("stat", "-c", "%U %a %n", repo.Dir, published, repo.Dir+"/Packages", repo.Dir+"/Release"),
		"root 755 "+repo.Dir+"\n", "root 644 "+published, "root 644 "+repo.Dir+"/Packages", "root 644 "+repo.Dir+"/Release")

	// apt, as the user, with a private configuration.
	apt := t.TempDir()
	list := filepath.Join(apt, "dovy.list")
	os.WriteFile(list, []byte("deb [trusted=yes] file:"+repo.Dir+" ./\n"), 0o644)
	os.MkdirAll(filepath.Join(apt, "lists", "partial"), 0o755)
	os.MkdirAll(filepath.Join(apt, "archives", "partial"), 0o755)
	opts := []string{
		"-o", "Dir::Etc::SourceList=" + list, "-o", "Dir::Etc::SourceParts=/nonexistent",
		"-o", "Dir::State::Lists=" + filepath.Join(apt, "lists"), "-o", "Dir::Cache=" + apt,
		"-o", "Dir::Cache::Archives=" + filepath.Join(apt, "archives"),
		"-o", "Debug::NoLocking=1",
	}
	run("apt-get", append(opts, "update")...)
	want(run("apt-cache", append(opts, "show", "pfetch")...), "Version: 3.1.1-1~dovy13", "X-Dovy-Recipe: pfetch")
	want(run("apt-cache", append(opts, "policy", "pfetch")...), "Candidate: 3.1.1-1~dovy13")
	want(run("apt-cache", append(opts, "policy")...), "o=dovy,a=dovy,n=dovy,l=dovy")
}
