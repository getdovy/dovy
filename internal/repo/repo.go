// Package repo keeps dovy's local apt repository: a flat repository,
// writable only by root (principles 7.3, 8.1).
package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dir is the repository: deb [trusted=yes] file:/var/lib/dovy/repo ./
const Dir = "/var/lib/dovy/repo"

// Root runs a command as root.
type Root func(args ...string) error

// Sudo runs args with sudo (principles 2.9).
func Sudo(args ...string) error {
	cmd := exec.Command("sudo", append([]string{"--"}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// Direct runs args as the current user; for tests.
func Direct(args ...string) error {
	if b, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", strings.Join(args, " "), strings.TrimSpace(string(b)))
	}
	return nil
}

// Publish adds debs to the repository in dir, drops the versions they
// supersede, and rewrites the indexes.
func Publish(dir string, debs []string, root Root) error { return Update(dir, debs, nil, root) }

// Update adds debs, removes the named packages, and rewrites the indexes.
func Update(dir string, debs, remove []string, root Root) error {
	tmp, err := os.MkdirTemp("", "dovy-repo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	final := map[string]string{} // package → path
	var old []string
	present, _ := filepath.Glob(filepath.Join(dir, "*.deb"))
	for _, p := range present {
		name, err := field(p, "Package")
		if err != nil {
			return err
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
		s, err := stanza(final[name])
		if err != nil {
			return err
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

// stanza is the deb's Packages entry.
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
	b, err := os.ReadFile(p)
	if err != nil {
		return "", 0, err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), int64(len(b)), nil
}

func sortedKeys(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}
