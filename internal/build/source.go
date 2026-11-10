package build

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/fetch"
	"github.com/getdovy/dovy/internal/sandbox"
	"github.com/getdovy/dovy/internal/unpack"
)

// Sourced reports a recipe built from source in the sandbox (ADR 0006).
func Sourced(r *catalog.Recipe) bool {
	return r.Kind == catalog.UpstreamBuild || r.Kind == catalog.TestingRebuild
}

// LogPath is where a source build's output goes.
func LogPath(cache, name string) string { return filepath.Join(cache, "logs", name+".log") }

// box opens the sandbox, making its base on first use.
func box(cache, name string) (*sandbox.Box, func(), error) {
	if err := sandbox.Check(); err != nil {
		return nil, nil, err
	}
	codename, err := capability.Codename("/etc/os-release")
	if err != nil {
		return nil, nil, err
	}
	lp := LogPath(cache, name)
	if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.Create(lp)
	if err != nil {
		return nil, nil, err
	}
	b := &sandbox.Box{Dir: filepath.Join(cache, "sandbox"), Codename: codename, Log: f}
	if err := b.Base(); err != nil {
		f.Close()
		return nil, nil, err
	}
	return b, func() { f.Close() }, nil
}

// upstream builds an upstream-build recipe: dovy unpacks the sources, the
// recipe's build.sh installs into $DESTDIR, dpkg-shlibdeps gives Depends.
func upstream(r *catalog.Recipe, release int, cache, work string) (string, error) {
	src, stage := filepath.Join(work, "src"), filepath.Join(work, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", err
	}
	for _, s := range r.Sources {
		p, err := fetch.Get(s.URL, s.SHA256, filepath.Join(cache, "sources"))
		if err != nil {
			return "", err
		}
		if err := unpack.TarGz(p, src); err != nil {
			return "", fmt.Errorf("%s: %w", s.URL, err)
		}
	}
	script, err := os.ReadFile(filepath.Join(r.Dir, "build.sh"))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(work, "build.sh"), script, 0o644); err != nil {
		return "", err
	}
	b, done, err := box(cache, r.Name)
	if err != nil {
		return "", err
	}
	defer done()
	var layer string
	if len(r.BuildDepends) > 0 {
		deps := append([]string{}, r.BuildDepends...)
		sort.Strings(deps)
		install := "apt-get update -q\napt-get install -y -q " + strings.Join(deps, " ")
		if layer, err = b.Layer(r.Name, install, work, install); err != nil {
			return "", fmt.Errorf("build dependencies: %w", err)
		}
	}
	if err := b.Run(layer, work, "cd /build/src\nDESTDIR=/build/stage sh -e /build/build.sh\n"+shlibdeps, r.Network != ""); err != nil {
		return "", fmt.Errorf("build.sh: %w", err)
	}
	depends, err := readDepends(filepath.Join(work, "depends"))
	if err != nil {
		return "", err
	}
	return deb.BuildWith(r, deb.Version(r, release), stage, filepath.Join(cache, "debs"), depends)
}

// shlibdeps writes the libraries the staged ELF files need to /build/depends.
const shlibdeps = `cd /build
mkdir -p debian
printf 'Source: dovy\n\nPackage: dovy\nArchitecture: any\n' >debian/control
set --
for f in $(find stage -type f); do readelf -h "$f" >/dev/null 2>&1 && set -- "$@" "-e$f"; done
if [ $# -gt 0 ]; then dpkg-shlibdeps -O --ignore-missing-info -lstage/usr/lib "$@" >depends; else : >depends; fi`

// readDepends reads dpkg-shlibdeps' "shlibs:Depends=..." line.
func readDepends(p string) ([]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "shlibs:Depends="); ok && strings.TrimSpace(v) != "" {
			var d []string
			for _, x := range strings.Split(v, ",") {
				d = append(d, strings.TrimSpace(x))
			}
			return d, nil
		}
	}
	return nil, nil
}

// rebuild rebuilds Debian testing's source against stable (and backports
// if required), without the network (principles 1.3).
func rebuild(r *catalog.Recipe, release int, cache, work string) (string, error) {
	s := r.Sources[0]
	files, err := sourceFiles(s.URL, s.SHA256, cache, work)
	if err != nil {
		return "", err
	}
	dsc := path.Base(s.URL)
	if r.HasBuild {
		script, err := os.ReadFile(filepath.Join(r.Dir, "build.sh"))
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(work, "build.sh"), script, 0o644); err != nil {
			return "", err
		}
	}
	b, done, err := box(cache, r.Name)
	if err != nil {
		return "", err
	}
	defer done()
	target := ""
	for _, n := range r.Needs() {
		if n == "backports" {
			target = " -t " + b.Codename + "-backports"
		}
	}
	install := "apt-get update -q\napt-get build-dep -y -q" + target + " /build/" + dsc
	layer, err := b.Layer(r.Name, install+"\n"+strings.Join(files, "\n"), work, install)
	if err != nil {
		return "", fmt.Errorf("build dependencies (Debian stable): %w", err)
	}
	// One command per line: sh -e ignores failures inside && lists.
	script := "cd /build\ndpkg-source -x " + dsc + " src\ncd src\n"
	if r.HasBuild {
		script += "sh -e /build/build.sh\n"
	}
	script += "dpkg-buildpackage -b -uc -us"
	if err := b.Run(layer, work, script, r.Network != ""); err != nil {
		return "", fmt.Errorf("dpkg-buildpackage: %w", err)
	}
	m, _ := filepath.Glob(filepath.Join(work, r.Package()+"_*.deb"))
	if len(m) != 1 {
		return "", fmt.Errorf("%s: the source built no package %s", r.SourcePackage, r.Package())
	}
	return repack(r, m[0], deb.Version(r, release), cache, filepath.Join(work, "repack"))
}

// sourceFiles fetches a .dsc and the files it lists into work, each checked
// against the .dsc's sha256 (2.4); it returns their sha256 lines.
func sourceFiles(url, sum, cache, work string) ([]string, error) {
	sources := filepath.Join(cache, "sources")
	p, err := fetch.Get(url, sum, sources)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	files, err := ParseDsc(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path.Base(url), err)
	}
	if err := link(p, filepath.Join(work, path.Base(url))); err != nil {
		return nil, err
	}
	var ids []string
	for _, name := range sortedKeys(files) {
		got, err := fetch.Get(url[:strings.LastIndex(url, "/")+1]+name, files[name], sources)
		if err != nil {
			return nil, err
		}
		if err := link(got, filepath.Join(work, name)); err != nil {
			return nil, err
		}
		ids = append(ids, files[name]+" "+name)
	}
	return ids, nil
}

// ParseDsc reads a .dsc's Checksums-Sha256: file name → sha256. A signed
// .dsc is read through its signature armour.
func ParseDsc(dsc string) (map[string]string, error) {
	files := map[string]string{}
	in := false
	s := bufio.NewScanner(strings.NewReader(dsc))
	for s.Scan() {
		l := s.Text()
		switch {
		case l == "Checksums-Sha256:":
			in = true
		case in && strings.HasPrefix(l, " "):
			f := strings.Fields(l)
			if len(f) != 3 || len(f[0]) != 64 || strings.ContainsAny(f[2], "/\\") || strings.HasPrefix(f[2], ".") {
				return nil, fmt.Errorf("bad Checksums-Sha256 line %q", l)
			}
			if _, err := hex.DecodeString(f[0]); err != nil {
				return nil, fmt.Errorf("bad sha256 %q", f[0])
			}
			files[f[2]] = f[0]
		default:
			in = false
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Checksums-Sha256")
	}
	return files, nil
}

func link(from, to string) error {
	os.Remove(to)
	if err := os.Link(from, to); err == nil {
		return nil
	}
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o644)
}

func sortedKeys(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
