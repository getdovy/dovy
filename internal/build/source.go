package build

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/deb"
	"github.com/getdovy/dovy/internal/fetch"
	"github.com/getdovy/dovy/internal/sandbox"
	"github.com/getdovy/dovy/internal/unpack"
)

func Sourced(r *catalog.Recipe) bool {
	return r.Kind == catalog.UpstreamBuild || r.Kind == catalog.TestingRebuild || r.Kind == catalog.LocalBuild
}

func LogPath(cache, name string) string { return filepath.Join(cache, "logs", name+".log") }

// Refresh remakes the sandbox base before the next source build; set by upgrade (ADR 0015).
var Refresh bool

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
	if Refresh {
		Refresh = false
		if err := b.Reset(); err != nil {
			f.Close()
			return nil, nil, err
		}
	}
	if err := b.Base(); err != nil {
		f.Close()
		return nil, nil, err
	}
	return b, func() { f.Close() }, nil
}

// upstream: the recipe's build.sh installs into $DESTDIR; dpkg-shlibdeps gives Depends.
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
		if err := unpack.Archive(p, s.URL, src); err != nil {
			return "", fmt.Errorf("%s: %w", s.URL, err)
		}
	}
	return sandboxed(r, release, cache, work)
}

// local copies the matching files as the user and builds offline; the folder is only read.
func local(r *catalog.Recipe, release int, cache, work string) (string, error) {
	root, files, _, err := LocalFiles(r)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no %s files in %s", strings.Join(r.Local.Match, ", "), r.Local.Path)
	}
	src := filepath.Join(work, "src")
	for _, f := range files {
		if err := copyFile(filepath.Join(root, f), filepath.Join(src, f)); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Join(work, "stage"), 0o755); err != nil {
		return "", err
	}
	return sandboxed(r, release, cache, work)
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// LocalFiles does not follow links. stamp is the newest mtime (UTC) of the folder,
// its subfolders and matching files, so it only increases; "" when nothing matches.
func LocalFiles(r *catalog.Recipe) (root string, files []string, stamp string, err error) {
	if r.Local == nil {
		return "", nil, "", fmt.Errorf("%s: not a local-build", r.Name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, "", err
	}
	root = filepath.Join(home, strings.TrimPrefix(r.Local.Path, "~/"))
	var newest time.Time
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		matched := false
		for _, m := range r.Local.Match {
			if ok, _ := filepath.Match(strings.ToLower(m), strings.ToLower(d.Name())); ok {
				matched = true
			}
		}
		if !d.IsDir() && !(d.Type().IsRegular() && matched) {
			return nil // links and other files are not taken, nor counted
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return "", nil, "", err
	}
	sort.Strings(files)
	if len(files) > 0 {
		stamp = newest.UTC().Format("20060102150405")
	}
	return root, files, stamp, nil
}

func sandboxed(r *catalog.Recipe, release int, cache, work string) (string, error) {
	stage := filepath.Join(work, "stage")
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
	if err := writePin(r, stage); err != nil {
		return "", err
	}
	return deb.BuildWith(r, deb.Version(r, release), stage, filepath.Join(cache, "debs"), depends)
}

const shlibdeps = `cd /build
mkdir -p debian
printf 'Source: dovy\n\nPackage: dovy\nArchitecture: any\n' >debian/control
set --
for f in $(find stage -type f); do readelf -h "$f" >/dev/null 2>&1 && set -- "$@" "-e$f"; done
if [ $# -gt 0 ]; then dpkg-shlibdeps -O --ignore-missing-info -lstage/usr/lib "$@" >depends; else : >depends; fi`

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

// rebuild builds Debian testing's source against stable (and backports if needed), offline.
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
	return repack(r, m[0], r.Package(), deb.Version(r, release), cache, filepath.Join(work, "repack"))
}

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

// ParseDsc also reads a signed .dsc, through its armour.
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
