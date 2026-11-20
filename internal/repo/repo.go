// Package repo keeps dovy's local flat apt repository, writable only by root.
package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dir is the repository: deb [trusted=yes] file:/var/lib/dovy/repo ./
const Dir = "/var/lib/dovy/repo"

type Root func(args ...string) error

func Sudo(args ...string) error {
	cmd := exec.Command("sudo", append([]string{"--"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// Direct is for tests.
func Direct(args ...string) error {
	if b, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", strings.Join(args, " "), strings.TrimSpace(string(b)))
	}
	return nil
}

func Publish(dir string, debs []string, root Root) error { return Update(dir, debs, nil, root) }

func Update(dir string, debs, remove []string, root Root) error {
	tmp, err := os.MkdirTemp("", "dovy-repo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	final := map[string]string{} // package -> path
	var old []string
	known := indexed(dir) // path -> stanza, for files unchanged since
	present, _ := filepath.Glob(filepath.Join(dir, "*.deb"))
	for _, p := range present {
		name := stanzaField(known[p], "Package")
		if name == "" {
			if name, err = field(p, "Package"); err != nil {
				return err
			}
		}
		final[name] = p
	}
	for _, name := range remove {
		if p, ok := final[name]; ok {
			old = append(old, p)
			delete(final, name)
		}
	}
	for _, p := range debs {
		name, err := field(p, "Package")
		if err != nil {
			return err
		}
		if prev, ok := final[name]; ok && filepath.Base(prev) != filepath.Base(p) {
			old = append(old, prev)
		}
		final[name] = p
	}

	var packages strings.Builder
	for _, name := range sortedKeys(final) {
		s := known[final[name]]
		if s == "" {
			if s, err = stanza(final[name]); err != nil {
				return err
			}
		}
		packages.WriteString(s + "\n")
	}
	pkgs := filepath.Join(tmp, "Packages")
	if err := os.WriteFile(pkgs, []byte(packages.String()), 0o644); err != nil {
		return err
	}
	rel := filepath.Join(tmp, "Release")
	if err := os.WriteFile(rel, []byte(release(packages.String(), time.Now())), 0o644); err != nil {
		return err
	}

	if err := root("install", "-d", "-m", "755", dir); err != nil {
		return err
	}
	for _, p := range debs {
		if filepath.Dir(p) == filepath.Clean(dir) { // already here: a reinstall's copy
			continue
		}
		if err := root("install", "-m", "644", p, filepath.Join(dir, filepath.Base(p))); err != nil {
			return err
		}
	}
	for _, p := range []string{pkgs, rel} {
		next := filepath.Join(dir, "."+filepath.Base(p)+".new")
		if err := root("install", "-m", "644", p, next); err != nil {
			return err
		}
		if err := root("mv", "-f", next, filepath.Join(dir, filepath.Base(p))); err != nil {
			return err
		}
	}
	for _, p := range old {
		if err := root("rm", "-f", p); err != nil {
			return err
		}
	}
	return nil
}

func stanza(deb string) (string, error) {
	b, err := exec.Command("dpkg-deb", "-f", deb).Output()
	if err != nil {
		return "", fmt.Errorf("dpkg-deb -f %s: %w", deb, err)
	}
	sum, size, err := hashFile(deb)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%sFilename: ./%s\nSize: %d\nSHA256: %s\n", b, filepath.Base(deb), size, sum), nil
}

func release(packages string, now time.Time) string {
	h := sha256.Sum256([]byte(packages))
	return fmt.Sprintf(`Origin: dovy
Label: dovy
Suite: dovy
Codename: dovy
Date: %s
Architectures: amd64 all
Description: packages built by dovy
SHA256:
 %s %d Packages
`, now.UTC().Format(time.RFC1123), hex.EncodeToString(h[:]), len(packages))
}

func field(deb, name string) (string, error) {
	b, err := exec.Command("dpkg-deb", "-f", deb, name).Output()
	if err != nil {
		return "", fmt.Errorf("dpkg-deb -f %s: %w", deb, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// Find returns the .deb only when its size and SHA256 match the index, so a reinstall
// can skip the build.
func Find(dir, pkg, version string) (string, bool) {
	for p, st := range indexed(dir) {
		if stanzaField(st, "Package") != pkg || stanzaField(st, "Version") != version {
			continue
		}
		sum, _, err := hashFile(p)
		return p, err == nil && sum == stanzaField(st, "SHA256")
	}
	return "", false
}

// indexed keeps the stanza of each .deb whose size is unchanged, so it is not hashed
// again; only root writes dir.
func indexed(dir string) map[string]string {
	b, err := os.ReadFile(filepath.Join(dir, "Packages"))
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, st := range strings.Split(string(b), "\n\n") {
		st = strings.TrimSpace(st)
		name := strings.TrimPrefix(stanzaField(st, "Filename"), "./")
		if name == "" || name != filepath.Base(name) {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fmt.Sprint(fi.Size()) == stanzaField(st, "Size") {
			m[p] = st + "\n"
		}
	}
	return m
}

func stanzaField(st, name string) string {
	for _, l := range strings.Split(st, "\n") {
		if v, ok := strings.CutPrefix(l, name+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func sortedKeys(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}
