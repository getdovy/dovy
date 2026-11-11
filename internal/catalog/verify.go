package catalog

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// URL is where the catalog comes from. Releases are signed tags (ADR 0005).
// Set at build time for testing: make CATALOG=/path/catalog.bundle.
var URL = "https://github.com/getdovy/dovy-catalog"

// ErrUntrusted: no release is signed by a known key.
var ErrUntrusted = errors.New("catalog is not signed by a trusted key")

// RecordPath holds the newest release a confirmed operation used,
// root-owned; release names only increase, so an older one is a rollback.
var RecordPath = "/var/lib/dovy/catalog-release"

// Recorded reads RecordPath; "" if none.
func Recorded() string {
	b, _ := os.ReadFile(RecordPath)
	return strings.TrimSpace(string(b))
}

// Older reports whether release a is older than b.
func Older(a, b string) bool {
	if a == b || !tagRE.MatchString(a) || !tagRE.MatchString(b) {
		return false
	}
	return newestFirst([]string{a, b})[0] == b
}

func rollback(tag string) error {
	if r := Recorded(); Older(tag, r) {
		return fmt.Errorf("catalog %s is older than %s, already used here: a rollback", tag, r)
	}
	return nil
}

// tagRE matches release tags: catalog-YYYY.MM.DD.N.
var tagRE = regexp.MustCompile(`^catalog-(\d{4})\.(\d{2})\.(\d{2})\.(\d+)$`)

// KeysDir holds keys a machine's owner adds (principles topic 11, rule 2).
const KeysDir = "/etc/dovy/keys.d"

//go:embed keys/allowed_signers
var shipped []byte

// Dir is the local copy: $XDG_DATA_HOME/dovy/catalog.
func Dir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "dovy", "catalog"), nil
}

// Signers returns the shipped keys plus those in keysDir. Files not owned by
// root, or writable by anyone else, are skipped and reported to warn.
func Signers(keysDir string, warn func(string)) []byte {
	out := append([]byte{}, shipped...)
	files, _ := filepath.Glob(filepath.Join(keysDir, "*"))
	sort.Strings(files)
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			warn(f + " ignored: must be owned by root and writable only by root")
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			warn(f + " ignored: " + err.Error())
			continue
		}
		out = append(append(out, '\n'), b...)
	}
	return out
}

// git runs git without the user's or system's config, so nothing there can
// change how signatures are checked.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// verifyTag checks tag is signed by one of signers.
func verifyTag(dir, tag string, signers []byte) error {
	f, err := os.CreateTemp("", "dovy-signers-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(signers); err != nil {
		return err
	}
	f.Close()
	if _, err := git(dir, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+f.Name(), "verify-tag", tag); err != nil {
		return ErrUntrusted
	}
	return nil
}

// Tag is the release tag the local copy is on.
func Tag(dir string) (string, error) {
	out, err := git(dir, "tag", "-l", "catalog-*")
	if err != nil {
		return "", err
	}
	tags := strings.Fields(out)
	if len(tags) != 1 {
		return "", errors.New("catalog is not on one release tag")
	}
	return tags[0], nil
}

// Verify checks the local copy on every run (specs/catalog.md): it sits on
// a release tag signed by a trusted key and the files match it exactly.
func Verify(dir string, signers []byte) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return errors.New("no catalog yet")
	}
	tag, err := Tag(dir)
	if err != nil {
		return err
	}
	if err := verifyTag(dir, tag, signers); err != nil {
		return err
	}
	if err := rollback(tag); err != nil {
		return err
	}
	at, err := git(dir, "rev-parse", tag+"^{commit}", "HEAD")
	if err != nil {
		return err
	}
	if r := strings.Fields(at); len(r) != 2 || r[0] != r[1] {
		return errors.New("catalog is not at its release tag")
	}
	st, err := git(dir, "status", "--porcelain", "--ignored")
	if err != nil {
		return err
	}
	if st != "" {
		return errors.New("catalog files differ from the signed catalog")
	}
	return nil
}

// Change is one recipe version change.
type Change struct{ Name, Old, New string }

// Changes is what update brought.
type Changes struct {
	Tag            string   // release now in use
	Skipped        []string // newer tags not signed by a trusted key
	Added, Dropped []string
	Updated        []Change
}

// tags lists release tags at url, newest first.
func tags(parent, url string) ([]string, error) {
	out, err := git(parent, "ls-remote", "--tags", "--refs", url, "catalog-*")
	if err != nil {
		return nil, err
	}
	var ts []string
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			ts = append(ts, strings.TrimPrefix(f[1], "refs/tags/"))
		}
	}
	return newestFirst(ts), nil
}

// newestFirst keeps release tag names and orders them newest first.
func newestFirst(names []string) []string {
	var ts []string
	for _, n := range names {
		if tagRE.MatchString(n) {
			ts = append(ts, n)
		}
	}
	key := func(t string) [4]int {
		var k [4]int
		for i, v := range tagRE.FindStringSubmatch(t)[1:] {
			k[i], _ = strconv.Atoi(v)
		}
		return k
	}
	sort.Slice(ts, func(i, j int) bool {
		a, b := key(ts[i]), key(ts[j])
		for n := range a {
			if a[n] != b[n] {
				return a[n] > b[n]
			}
		}
		return false
	})
	return ts
}

// Update fetches the newest release tag at url whose signature verifies into
// a new copy, checks its recipes there, and only then swaps it in. A failure
// leaves the old copy as it was.
func Update(dir, url string, signers []byte) (Changes, error) {
	parent := filepath.Dir(dir)
	work, old := dir+".new", dir+".old"
	os.RemoveAll(work)
	os.RemoveAll(old)
	defer os.RemoveAll(work)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Changes{}, err
	}
	ts, err := tags(parent, url)
	if err != nil {
		return Changes{}, err
	}
	if len(ts) == 0 {
		return Changes{}, errors.New("no catalog release found")
	}
	var c Changes
	for _, t := range ts {
		os.RemoveAll(work)
		if _, err := git(parent, "init", "-q", work); err != nil {
			return Changes{}, err
		}
		if _, err := git(work, "fetch", "-q", "--no-tags", "--depth", "1", url, "refs/tags/"+t+":refs/tags/"+t); err != nil {
			return Changes{}, err
		}
		if verifyTag(work, t, signers) == nil {
			c.Tag = t
			break
		}
		c.Skipped = append(c.Skipped, t)
	}
	if c.Tag == "" {
		return Changes{}, ErrUntrusted
	}
	if err := rollback(c.Tag); err != nil {
		return Changes{}, err
	}
	if _, err := git(work, "reset", "-q", "--hard", c.Tag); err != nil {
		return Changes{}, err
	}
	if _, err := Load(work, nil); err != nil {
		return Changes{}, fmt.Errorf("catalog refused: %w", err)
	}
	before := versions(dir)
	if _, err := os.Stat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			return Changes{}, err
		}
		defer os.RemoveAll(old)
	}
	if err := os.Rename(work, dir); err != nil {
		os.Rename(old, dir)
		return Changes{}, err
	}
	d := diff(before, versions(dir))
	c.Added, c.Dropped, c.Updated = d.Added, d.Dropped, d.Updated
	return c, nil
}

// Fetch puts release tag from url into dir, a new copy, after checking
// its signature; for show --diff.
func Fetch(dir, url, tag string, signers []byte) error {
	if _, err := git(filepath.Dir(dir), "init", "-q", dir); err != nil {
		return err
	}
	if _, err := git(dir, "fetch", "-q", "--no-tags", "--depth", "1", url, "refs/tags/"+tag+":refs/tags/"+tag); err != nil {
		return err
	}
	if err := verifyTag(dir, tag, signers); err != nil {
		return err
	}
	_, err := git(dir, "reset", "-q", "--hard", tag)
	return err
}

// versions reads name → version from the recipes on disk, unchecked.
func versions(dir string) map[string]string {
	v := map[string]string{}
	paths, _ := filepath.Glob(filepath.Join(dir, "recipes", "*", "recipe.toml"))
	for _, p := range paths {
		if r, err := parse(p); err == nil {
			v[r.Name] = r.Version
		}
	}
	return v
}

func diff(old, cur map[string]string) Changes {
	var c Changes
	for _, n := range sorted(cur) {
		switch o, ok := old[n]; {
		case !ok:
			c.Added = append(c.Added, n)
		case o != cur[n]:
			c.Updated = append(c.Updated, Change{n, o, cur[n]})
		}
	}
	for _, n := range sorted(old) {
		if _, ok := cur[n]; !ok {
			c.Dropped = append(c.Dropped, n)
		}
	}
	return c
}
