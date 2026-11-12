// Package overlap finds files a package would take from another
// (ADR 0016): dpkg refuses them mid-install, so dovy checks first.
package overlap

import (
	"archive/tar"
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Conflict is one file two packages ship.
type Conflict struct {
	Path, Package, Owner string
}

func (c Conflict) String() string {
	return fmt.Sprintf("%s: %s belongs to %s", c.Package, c.Path, c.Owner)
}

// Deb is a package's name, Replaces, and files (not directories).
type Deb struct {
	Package  string
	Replaces []string
	Files    []string
}

// Read lists a .deb's files with its own tar reader over dpkg-deb's
// filesystem archive.
func Read(path string) (*Deb, error) {
	out, err := exec.Command("dpkg-deb", "-f", path, "Package", "Replaces").Output()
	if err != nil {
		return nil, fmt.Errorf("dpkg-deb -f %s: %w", filepath.Base(path), err)
	}
	d := &Deb{}
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "Package:"); ok {
			d.Package = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(l, "Replaces:"); ok {
			d.Replaces = names(v)
		}
	}
	cmd := exec.Command("dpkg-deb", "--fsys-tarfile", path)
	r, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	d.Files, err = list(r)
	if err != nil {
		cmd.Wait()
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return d, cmd.Wait()
}

// list reads the file names of a tar archive and drains r: the writer still
// has padding to send after the end marker, and blocks on a small pipe.
func list(r io.Reader) ([]string, error) {
	defer io.Copy(io.Discard, r)
	var files []string
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeDir {
			files = append(files, "/"+strings.TrimPrefix(filepath.Clean("/"+h.Name), "/"))
		}
	}
}

// names reads package names from a relation field, without versions.
func names(field string) []string {
	var n []string
	for _, alt := range strings.Split(field, ",") {
		for _, p := range strings.Split(alt, "|") {
			if f := strings.Fields(p); len(f) > 0 {
				n = append(n, strings.SplitN(f[0], ":", 2)[0])
			}
		}
	}
	return n
}

// Owners reads dpkg's file lists in infoDir: path → package.
func Owners(infoDir string) (map[string]string, error) {
	lists, err := filepath.Glob(filepath.Join(infoDir, "*.list"))
	if err != nil {
		return nil, err
	}
	owners := map[string]string{}
	for _, l := range lists {
		pkg := strings.SplitN(strings.TrimSuffix(filepath.Base(l), ".list"), ":", 2)[0]
		f, err := os.Open(l)
		if err != nil {
			return nil, err
		}
		s := bufio.NewScanner(f)
		for s.Scan() {
			owners[s.Text()] = pkg
		}
		f.Close()
	}
	return owners, nil
}

// Diverted reads dpkg's diversions: path → the package holding it, "" for
// the administrator's own.
func Diverted() map[string]string {
	out, _ := exec.Command("dpkg-divert", "--list").Output()
	return parseDiversions(string(out))
}

func parseDiversions(out string) map[string]string {
	d := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) == 7 && f[0] == "diversion" && f[5] == "by": // diversion of /a to /b by pkg
			d[f[2]] = f[6]
		case len(f) == 6 && f[0] == "local": // local diversion of /a to /b
			d[f[3]] = ""
		}
	}
	return d
}

// Check finds files the debs take from installed packages that stay, or
// from each other, and files another package diverts (dpkg would rename
// them silently). A package may take its own old files and those of
// packages it Replaces; leaving names packages this transaction removes.
func Check(debs []*Deb, owners map[string]string, leaving map[string]bool, diverted map[string]string) []Conflict {
	var cs []Conflict
	seen := map[string]string{}
	for _, d := range debs {
		for _, p := range d.Files {
			if by, ok := diverted[p]; ok {
				if by != "" && by != d.Package && !leaving[by] && !contains(d.Replaces, by) {
					cs = append(cs, Conflict{p, d.Package, by + " (diverted)"})
				}
				continue
			}
			if o := seen[p]; o != "" && o != d.Package {
				cs = append(cs, Conflict{p, d.Package, o})
				continue
			}
			seen[p] = d.Package
			o, ok := owners[p]
			if !ok || o == d.Package || leaving[o] || contains(d.Replaces, o) || isDir(p) {
				continue
			}
			cs = append(cs, Conflict{p, d.Package, o})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Path < cs[j].Path })
	return cs
}

// isDir: dpkg lists directories too; packages share them.
var isDir = func(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
