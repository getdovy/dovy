// Package unpack extracts archives with dovy's own code.
package unpack

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

// TarGz refuses absolute paths, "..", and links pointing outside dest.
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
	return untar(tar.NewReader(gz), dest)
}

func TarXz(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	xr, err := xz.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: not a .tar.xz: %w", filepath.Base(path), err)
	}
	return untar(tar.NewReader(xr), dest)
}

func untar(tr *tar.Reader, dest string) error {
	links := map[string]bool{} // links made so far, by cleaned name
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
		// Checked by name only so far: an entry under an earlier link would
		// be written wherever the links lead.
		for d := filepath.Dir(name); d != "."; d = filepath.Dir(d) {
			if links[d] {
				return fmt.Errorf("%s: path goes through a link", h.Name)
			}
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
			links[name] = true
		default:
			return fmt.Errorf("%s: unsupported entry type", h.Name)
		}
		if err != nil {
			return err
		}
	}
}

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

// Zip refuses links: zip releases carry plain files.
func Zip(path, dest string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("%s: not a .zip: %w", filepath.Base(path), err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		name, err := inside(f.Name)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}
		target := filepath.Join(dest, name)
		m := f.Mode()
		switch {
		case m.IsDir():
			err = os.MkdirAll(target, 0o755)
		case m.IsRegular():
			var r io.ReadCloser
			if r, err = f.Open(); err == nil {
				err = file(target, r, m.Perm()&0o111 != 0)
				r.Close()
			}
		default:
			return fmt.Errorf("%s: unsupported entry type", f.Name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Archive copies a non-archive as an executable under its own name.
func Archive(path, name, dest string) error {
	base := filepath.Base(name)
	switch {
	case strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz"):
		return TarGz(path, dest)
	case strings.HasSuffix(name, ".tar.xz") || strings.HasSuffix(name, ".txz"):
		return TarXz(path, dest)
	case strings.HasSuffix(name, ".zip"):
		return Zip(path, dest)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if b, ok := strings.CutSuffix(base, ".bz2"); ok {
		r, base = bzip2.NewReader(f), b
	}
	if _, err := inside(base); err != nil || base == "." || base == "" {
		return fmt.Errorf("%s: bad file name", name)
	}
	return file(filepath.Join(dest, base), r, true)
}

func Archived(name string) bool {
	for _, s := range []string{".tar.gz", ".tgz", ".tar.xz", ".txz", ".zip", ".bz2"} {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}
