package unpack

import (
	"archive/tar"
	"archive/zip"
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

func TestTarGzGlobalHeader(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	f, _ := os.Create(p)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "abc"}})
	tw.WriteHeader(&tar.Header{Name: "yad-14.2/", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.Close()
	gz.Close()
	f.Close()
	dest := t.TempDir()
	if err := TarGz(p, dest); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "yad-14.2")); err != nil || !fi.IsDir() {
		t.Errorf("got %v %v", fi, err)
	}
}

func TestZip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "fnm"}
	h.SetMode(0o755)
	w, _ := zw.CreateHeader(h)
	w.Write([]byte("bin"))
	zw.Close()
	f.Close()
	dest := t.TempDir()
	if err := Archive(p, "https://x/fnm-linux.zip", dest); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "fnm")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("got %v %v", fi, err)
	}
	p2 := filepath.Join(t.TempDir(), "b.zip")
	f, _ = os.Create(p2)
	zw = zip.NewWriter(f)
	zw.Create("../evil")
	zw.Close()
	f.Close()
	if err := Zip(p2, t.TempDir()); err == nil {
		t.Error("../ accepted")
	}
}

func TestSingleFileAndBz2(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dl")
	os.WriteFile(p, []byte("ELF"), 0o644)
	dest := t.TempDir()
	if err := Archive(p, "https://x/posh-linux-amd64", dest); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "posh-linux-amd64")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("single file: %v %v", fi, err)
	}
	bz := filepath.Join("testdata", "hello.bz2")
	if _, err := os.Stat(bz); err != nil {
		t.Skip("no testdata/hello.bz2")
	}
	if err := Archive(bz, "https://x/restic_0.19.1_linux_amd64.bz2", dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "restic_0.19.1_linux_amd64")); string(b) != "hello\n" {
		t.Errorf("bz2: %q", b)
	}
	if Archived("https://x/posh-linux-amd64") || !Archived("a.tar.xz") || !Archived("a.bz2") {
		t.Error("Archived")
	}
}

func TestTarXz(t *testing.T) {
	xzf := filepath.Join("testdata", "hello.tar.xz")
	if _, err := os.Stat(xzf); err != nil {
		t.Skip("no testdata/hello.tar.xz")
	}
	dest := t.TempDir()
	if err := Archive(xzf, "zig-x86_64-linux-0.17.1.tar.xz", dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "hello", "hello.txt")); string(b) != "hello\n" {
		t.Errorf("tar.xz: %q", b)
	}
}
