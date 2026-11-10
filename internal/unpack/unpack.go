// Package unpack extracts tarballs with dovy's own code (principles 2.10).
package unpack

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// TarGz extracts a .tar.gz into dest. Absolute paths, "..", and links
// pointing outside dest are refused.
func TarGz(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: not a .tar.gz: %w", filepath.Base(path), err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader { // git archive's commit id
			continue
		}
		name, err := inside(h.Name)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}
		target := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			err = file(target, tr, h.FileInfo().Mode().Perm()&0o111 != 0)
		case tar.TypeSymlink:
			link := h.Linkname
			if !filepath.IsAbs(link) {
				link = filepath.Join(filepath.Dir(name), link)
			}
			if _, e := inside(link); e != nil || filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("%s: link points outside", h.Name)
			}
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				err = os.Symlink(h.Linkname, target)
			}
		default:
			return fmt.Errorf("%s: unsupported entry type", h.Name)
		}
		if err != nil {
			return err
		}
	}
}

// inside cleans an archive path and refuses anything outside the root.
func inside(p string) (string, error) {
	c := filepath.Clean(p)
	if filepath.IsAbs(p) || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%s: path outside the archive", p)
	}
	return c, nil
}

func file(path string, r io.Reader, exec bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
