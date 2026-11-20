package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/getdovy/dovy/internal/build"
	"github.com/getdovy/dovy/internal/catalog"
	"github.com/getdovy/dovy/internal/log"
	"github.com/getdovy/dovy/internal/sandbox"
	"github.com/getdovy/dovy/internal/state"
	"github.com/getdovy/dovy/internal/ui"
)

// The cache holds only what can be fetched or built again; what is installed
// is in the local repository.

// With no paths, dropBuilt deletes every .deb this plan made. A failed run
// keeps them: its error message points there.
func (t *txn) dropBuilt(paths ...string) {
	cache, err := build.CacheDir()
	if err != nil {
		return
	}
	if len(paths) == 0 {
		for _, d := range t.prebuilt {
			paths = append(paths, d)
		}
		for _, d := range t.parts {
			paths = append(paths, d)
		}
	}
	dir := filepath.Join(cache, "debs")
	for _, p := range paths {
		if filepath.Dir(p) == dir { // never the repository's copy a reinstall used
			os.Remove(p)
		}
	}
}

// pruneCache runs only after a successful run: the local repository then holds
// the repacked vendor packages.
func (a *app) pruneCache() {
	cache, err := build.CacheDir()
	if err != nil {
		return
	}
	rs, err := catalog.Load(a.dir, nil)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, r := range rs {
		for _, s := range r.Sources {
			keep[s.SHA256] = true
		}
	}
	dir := filepath.Join(cache, "sources")
	es, _ := os.ReadDir(dir)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	for _, n := range stale(names, keep) {
		os.Remove(filepath.Join(dir, n))
	}
	os.RemoveAll(filepath.Join(cache, "vendor"))
}

// Only sha256-named downloads are stale; anything else, such as .part-*, stays.
func stale(names []string, keep map[string]bool) []string {
	var out []string
	for _, n := range names {
		if isSHA256(n) && !keep[n] {
			out = append(out, n)
		}
	}
	return out
}

func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

func (a *app) pruneLogs() {
	es, err := os.ReadDir(log.Dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	old := log.Old(names, log.Keep)
	if len(old) == 0 {
		return
	}
	args := []string{"rm", "-f", "--"}
	for _, n := range old {
		args = append(args, filepath.Join(log.Dir, n))
	}
	a.root(args...)
}

// The local repository, which apt installs from, and the logs are not touched.
func (a *app) clean() int {
	cache, err := build.CacheDir()
	if err != nil {
		a.errs.Fail(err.Error(), "unchanged", "set HOME or XDG_CACHE_HOME", "")
		return exitFailed
	}
	if _, err := os.Lstat(cache); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(a.out.Out, "Nothing to clean: "+cache+" does not exist.")
		return exitOK
	}
	// Not while another dovy builds and installs from it.
	if f, err := state.Lock(state.Dir); err == nil {
		defer f.Close()
	} else if errors.Is(err, state.ErrBusy) {
		a.errs.Fail("another dovy is running", "unchanged", "wait for it to finish, then: dovy clean", "")
		return exitFailed
	}
	size := treeSize(cache)
	if err := sandbox.Remove(cache); err != nil { // the sandbox's files belong to subuids
		a.errs.Fail(err.Error(), "partly cleaned", "dovy clean", "")
		return exitFailed
	}
	a.out.Say(ui.Green, "Removed:", fmt.Sprintf("%s (%s)", cache, ui.Bytes(size)))
	fmt.Fprintln(a.out.Out, "Downloads and builds come back when a recipe needs them; the sandbox is made again by the next source build.")
	return exitOK
}

func treeSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
