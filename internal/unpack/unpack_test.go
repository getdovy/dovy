package unpack

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type entry struct {
	name, link string
	typ        byte
	mode       int64
}

func archive(t *testing.T, es ...entry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: e.mode}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.name))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			tw.Write([]byte(e.name))
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
	return p
}

func TestTarGz(t *testing.T) {
	dest := t.TempDir()
	p := archive(t,
		entry{"./", "", tar.TypeDir, 0o755},
		entry{"./pfetch", "", tar.TypeReg, 0o755},
		entry{"target/man/eza.1", "", tar.TypeReg, 0o644},
		entry{"target/link", "man/eza.1", tar.TypeSymlink, 0o777},
	)
	if err := TarGz(p, dest); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{"pfetch": 0o755, "target/man/eza.1": 0o644} {
		fi, err := os.Stat(filepath.Join(dest, name))
		if err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: %v %v", name, fi, err)
		}
	}
}

func TestTarGzRefuses(t *testing.T) {
	cases := map[string]entry{
		"absolute":      {"/etc/passwd", "", tar.TypeReg, 0o644},
		"dotdot":        {"a/../../x", "", tar.TypeReg, 0o644},
		"link outside":  {"a/l", "../../etc", tar.TypeSymlink, 0o777},
		"absolute link": {"l", "/etc/passwd", tar.TypeSymlink, 0o777},
		"hard link":     {"h", "x", tar.TypeLink, 0o644},
		"device":        {"d", "", tar.TypeChar, 0o644},
	}
	for name, e := range cases {
		if err := TarGz(archive(t, e), t.TempDir()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A file may not be written through an existing link.
	dest := t.TempDir()
	err := TarGz(archive(t, entry{"l", "x", tar.TypeSymlink, 0o777}, entry{"l", "", tar.TypeReg, 0o644}), dest)
	if err == nil {
		t.Error("write through link accepted")
	}
	if err := TarGz(archive(t), dest); err != nil {
		t.Errorf("empty: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "x.tar.gz")
	os.WriteFile(bad, []byte("plain"), 0o644)
	if err := TarGz(bad, dest); err == nil || !strings.Contains(err.Error(), "not a .tar.gz") {
		t.Errorf("not gzip: %v", err)
	}
}
