// Package deb turns a recipe into a .deb (ADR 0010).
package deb

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/getdovy/dovy/internal/capability"
	"github.com/getdovy/dovy/internal/catalog"
)

// Arch is the only architecture supported for now.
const Arch = "amd64"

// ArchOf: setup packages hold no binaries, so they are arch all.
func ArchOf(r *catalog.Recipe) string {
	if r.Kind == catalog.Setup || r.Kind == catalog.VendorRepository || r.Kind == catalog.Bundle || r.Kind == capability.Kind {
		return "all"
	}
	return Arch
}

func Release(osRelease string) (int, error) {
	f, err := os.Open(osRelease)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if v, ok := strings.CutPrefix(s.Text(), "VERSION_ID="); ok {
			return strconv.Atoi(strings.Trim(v, `"`))
		}
	}
	return 0, fmt.Errorf("%s: no VERSION_ID", osRelease)
}

// Version is <upstream>-<revision>~dovy<debian> (ADR 0010).
func Version(r *catalog.Recipe, release int) string {
	return fmt.Sprintf("%s-%d~dovy%d", r.Upstream(), r.Rev(), release)
}

func Stage(r *catalog.Recipe, src, stage string) error {
	for _, f := range r.Files {
		p := filepath.Join(stage, f.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(strings.TrimPrefix(f.Content, "\n")), 0o644); err != nil {
			return err
		}
	}
	for dst, from := range r.Install {
		p := filepath.Join(src, from)
		fi, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("install %s: %s not in the sources", dst, from)
		}
		if fi.IsDir() { // a whole folder (runtime files, bundled apps)
			if err := copyTree(p, filepath.Join(stage, dst)); err != nil {
				return fmt.Errorf("install %s: %w", dst, err)
			}
			continue
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("install %s: %s is not a regular file", dst, from)
		}
		mode := os.FileMode(0o644)
		if fi.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		if err := copyFile(p, filepath.Join(stage, dst), mode); err != nil {
			return err
		}
	}
	for l, t := range r.Links {
		p := filepath.Join(stage, l)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(t, p); err != nil {
			return fmt.Errorf("link %s: %w", l, err)
		}
	}
	return nil
}

func Build(r *catalog.Recipe, version, stage, out string) (string, error) {
	return BuildWith(r, version, stage, out, nil)
}

func BuildWith(r *catalog.Recipe, version, stage, out string, depends []string) (string, error) {
	size, err := installedSize(stage)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(stage, "DEBIAN"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, "DEBIAN", "control"), []byte(control(r, version, size, depends)), 0o644); err != nil {
		return "", err
	}
	for name, body := range r.Scripts {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(stage, "DEBIAN", name), []byte(body), 0o755); err != nil {
			return "", err
		}
	}
	if c := conffiles(r, stage); c != "" {
		if err := os.WriteFile(filepath.Join(stage, "DEBIAN", "conffiles"), []byte(c), 0o644); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	deb := filepath.Join(out, fmt.Sprintf("%s_%s_%s.deb", r.Package(), version, ArchOf(r)))
	cmd := exec.Command("dpkg-deb", "--root-owner-group", "--build", stage, deb)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("dpkg-deb: %s", strings.TrimSpace(string(b)))
	}
	return deb, nil
}

// Control: size is in KiB.
func Control(r *catalog.Recipe, version string, size int64) string {
	return control(r, version, size, nil)
}

// Conflicts are versioned so they match the real package, not one providing its name
// (tuned-ppd provides power-profiles-daemon) (ADR 0016).
func Conflicts(r *catalog.Recipe) []string {
	c := append([]string{}, r.Conflicts...)
	for _, p := range r.Replaces {
		if p != r.Package() {
			c = append(c, p+" (>= 0)")
		}
	}
	return c
}

func ReplacedField(r *catalog.Recipe) string {
	if len(r.Replaced) == 0 {
		return ""
	}
	return "X-Dovy-Replaced: " + strings.Join(r.Replaced, ", ") + "\n"
}

func control(r *catalog.Recipe, version string, size int64, depends []string) string {
	var rel string
	// Versioned so only the real package satisfies them, not a provider.
	var d []string
	for _, p := range r.Packages {
		d = append(d, p+" (>= 0)")
	}
	if d = append(d, depends...); len(d) > 0 {
		rel += "Depends: " + strings.Join(d, ", ") + "\n"
	}
	if c := Conflicts(r); len(c) > 0 {
		rel += "Conflicts: " + strings.Join(c, ", ") + "\n"
	}
	if len(r.Recommends) > 0 {
		rel += "Recommends: " + strings.Join(r.Recommends, ", ") + "\n"
	}
	rel += ReplacedField(r)
	if r.Release != "" {
		rel += "X-Dovy-Release: " + r.Release + "\n"
	}
	field := "X-Dovy-Recipe"
	if r.Kind == capability.Kind {
		rel += "X-Dovy-Content: " + ContentHash(r) + "\n"
		field = "X-Dovy-Capability"
	}
	return fmt.Sprintf(`Package: %s
Version: %s
Architecture: %s
Maintainer: %s
Installed-Size: %d
%sSection: misc
Priority: optional
%s: %s
Description: %s
`, r.Package(), version, ArchOf(r), r.Maintainer, size, rel, field, r.Name, r.Description)
}

// conffiles are the files dpkg keeps when the user edits them.
func conffiles(r *catalog.Recipe, stage string) string {
	var b strings.Builder
	var paths []string
	for _, f := range r.Files {
		paths = append(paths, f.Path)
	}
	for dst := range r.Install {
		paths = append(paths, dst)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if _, err := os.Stat(filepath.Join(stage, p)); err == nil && strings.HasPrefix(p, "/etc/") {
			b.WriteString(p + "\n")
		}
	}
	return b.String()
}

// installedSize counts in KiB, as dpkg does.
func installedSize(dir string) (int64, error) {
	var n int64
	err := filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += (fi.Size() + 1023) / 1024
		}
		return err
	})
	return n, err
}

// copyTree: links must stay inside the folder.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		switch {
		case fi.IsDir():
			return os.MkdirAll(out, 0o755)
		case fi.Mode()&os.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if c := filepath.Clean(filepath.Join(filepath.Dir(rel), t)); filepath.IsAbs(t) || c == ".." || strings.HasPrefix(c, "../") {
				return fmt.Errorf("%s: link points outside", rel)
			}
			return os.Symlink(t, out)
		case fi.Mode().IsRegular():
			mode := os.FileMode(0o644)
			if fi.Mode().Perm()&0o111 != 0 {
				mode = 0o755
			}
			return copyFile(p, out, mode)
		}
		return fmt.Errorf("%s: unsupported file type", rel)
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(dst, mode) // umask may have narrowed it
	}
	return err
}

// ContentHash lets dovy rebuild a capability package only when its content changes.
func ContentHash(r *catalog.Recipe) string {
	h := sha256.Sum256([]byte(capability.Content(r)))
	return hex.EncodeToString(h[:6])
}
