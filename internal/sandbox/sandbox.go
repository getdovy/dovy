// Package sandbox runs source builds in a minimal Debian stable, unprivileged
// (principles 2.5, ADR 0006, ADR 0015).
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Box is the sandbox's cache: base, layers, apt downloads.
type Box struct {
	Dir      string // cache/sandbox
	Codename string // Debian release, e.g. trixie
	Log      io.Writer
}

// userns is the namespace every step runs in: the user is root, the
// subuid range maps the rest, so dpkg works inside.
var userns = []string{"unshare", "--map-auto", "--map-root-user", "--mount", "--pid", "--fork", "--kill-child"}

// Check reports why source builds cannot run here, or nil.
func Check() error {
	for _, t := range []struct{ cmd, pkg string }{{"mmdebstrap", "mmdebstrap"}, {"newuidmap", "uidmap"}} {
		if _, err := exec.LookPath(t.cmd); err != nil {
			return fmt.Errorf("source builds need %s: sudo apt install %s", t.cmd, t.pkg)
		}
	}
	out, err := exec.Command(userns[0], append(userns[1:], "true")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("source builds need user namespaces and a subuid range (/etc/subuid): %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *Box) base() string     { return filepath.Join(b.Dir, "base") }
func (b *Box) archives() string { return filepath.Join(b.Dir, "archives") }

// baseID changes whenever the base is made again.
func (b *Box) baseID() string {
	id, _ := os.ReadFile(filepath.Join(b.Dir, "base.id"))
	return string(id)
}

// Base makes the base once: mmdebstrap writes a tarball, unpacked inside
// the namespace so file owners match every later step.
func (b *Box) Base() error {
	if b.baseID() != "" {
		return nil
	}
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return err
	}
	if err := Remove(b.base()); err != nil {
		return err
	}
	tar := filepath.Join(b.Dir, "base.tar")
	defer os.Remove(tar)
	mirror := "https://deb.debian.org/debian"
	c := b.Codename
	args := []string{"--mode=unshare", "--variant=buildd", "--format=tar", "--components=main",
		"--include=ca-certificates",
		`--aptopt=APT::Install-Recommends "false"`, `--aptopt=Acquire::Languages "none"`,
		c, tar,
		"deb " + mirror + " " + c + " main",
		"deb " + mirror + " " + c + "-updates main",
		"deb " + mirror + "-security " + c + "-security main",
		"deb " + mirror + " " + c + "-backports main",
	}
	if err := b.cmd("mmdebstrap", args...); err != nil {
		return fmt.Errorf("mmdebstrap: %w", err)
	}
	if err := os.MkdirAll(b.base(), 0o755); err != nil {
		return err
	}
	if err := b.cmd(userns[0], append(userns[1:], "tar", "-xpf", tar, "-C", b.base(), "--exclude=./dev/*")...); err != nil {
		return fmt.Errorf("unpacking the base: %w", err)
	}
	return os.WriteFile(filepath.Join(b.Dir, "base.id"), []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o644)
}

// Layer makes name's layer, with the network, by running script in it;
// it is kept while key and the base are unchanged.
func (b *Box) Layer(name, key, work, script string) (string, error) {
	dir := filepath.Join(b.Dir, "layers", name)
	h := sha256.Sum256([]byte(b.baseID() + "\n" + key))
	id := hex.EncodeToString(h[:])
	if got, _ := os.ReadFile(filepath.Join(dir, "key")); string(got) == id {
		return dir, nil
	}
	if err := Remove(dir); err != nil {
		return "", err
	}
	if err := b.run(b.base(), dir, work, script, true); err != nil {
		Remove(dir)
		return "", err
	}
	return dir, os.WriteFile(filepath.Join(dir, "key"), []byte(id), 0o644)
}

// Run runs script on a throwaway overlay over layer, with work at /build;
// the network is off unless network is true.
func (b *Box) Run(layer, work, script string, network bool) error {
	lower := b.base()
	if layer != "" {
		lower = filepath.Join(layer, "up") + ":" + lower
	}
	tmp, err := os.MkdirTemp(b.Dir, "run-")
	if err != nil {
		return err
	}
	defer Remove(tmp)
	return b.run(lower, tmp, work, script, network)
}

// run mounts an overlay of lower with upper dir/up, then runs script as
// root in a chroot with work at /build. The host's files are not visible.
func (b *Box) run(lower, dir, work, script string, network bool) error {
	for _, d := range []string{filepath.Join(dir, "up"), filepath.Join(dir, "wk"), filepath.Join(dir, "root"), b.archives()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	args := append([]string{}, userns[1:]...)
	if !network {
		args = append(args, "--net")
	}
	args = append(args, "sh", "-c", setup, "sh", lower, dir, work, b.archives(), script)
	err := b.cmd(userns[0], args...)
	if err != nil && strings.Contains(err.Error(), "not supported as upperdir") {
		return fmt.Errorf("source builds need the cache on a filesystem with overlay support (ext4, btrfs, xfs): %s", b.Dir)
	}
	return err
}

// setup builds the chroot: overlay, /proc, a minimal /dev, /tmp, apt's
// download cache and /build; then runs the script with a clean environment.
const setup = `set -e
lower=$1 dir=$2 work=$3 archives=$4 script=$5
root=$dir/root
mount -t overlay overlay -o "lowerdir=$lower,upperdir=$dir/up,workdir=$dir/wk,userxattr" "$root"
mount -t proc proc "$root/proc"
mount -t tmpfs -o mode=755 tmpfs "$root/dev"
for d in null zero full random urandom tty; do
	touch "$root/dev/$d"
	mount --bind "/dev/$d" "$root/dev/$d"
done
ln -s /proc/self/fd "$root/dev/fd"
mkdir "$root/dev/pts" "$root/dev/shm"
mount -t tmpfs tmpfs "$root/dev/shm"
mount -t tmpfs tmpfs "$root/tmp"
mount --bind "$archives" "$root/var/cache/apt/archives"
mkdir -p "$root/build"
mount --bind "$work" "$root/build"
cp -L /etc/resolv.conf "$root/etc/resolv.conf" 2>/dev/null || true
exec chroot "$root" /usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/build/home LANG=C.UTF-8 \
	DEBIAN_FRONTEND=noninteractive sh -ec "mkdir -p /build/home; $script"
`

func (b *Box) cmd(name string, args ...string) error {
	c := exec.Command(name, args...)
	var tail tailBuffer
	w := io.Writer(&tail)
	if b.Log != nil {
		w = io.MultiWriter(b.Log, &tail)
	}
	c.Stdout, c.Stderr = w, w
	if err := c.Run(); err != nil {
		if s := tail.String(); s != "" {
			return errors.New(s)
		}
		return err
	}
	return nil
}

// Remove deletes path, whose files may be owned by subuids, inside the
// namespace.
func Remove(path string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	out, err := exec.Command(userns[0], append(userns[1:], "rm", "-rf", path)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("removing %s: %s", path, strings.TrimSpace(string(out)))
	}
	return nil
}

// tailBuffer keeps the last lines of output, for errors.
type tailBuffer struct{ lines []string }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.lines = append(t.lines, strings.Split(strings.TrimRight(string(p), "\n"), "\n")...)
	if n := len(t.lines); n > 8 {
		t.lines = t.lines[n-8:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(strings.Join(t.lines, "\n")) }
