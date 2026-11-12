package overlap

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	isDir = func(p string) bool { return p == "/usr/bin" }
	owners := map[string]string{
		"/usr/bin/firefox": "firefox-esr", "/usr/bin/yad": "yad", "/usr/bin": "coreutils",
		"/usr/bin/old": "gone", "/usr/bin/repl": "oldpkg", "/usr/bin/div": "other",
	}
	debs := []*Deb{
		{Package: "firefox", Files: []string{"/usr/bin/firefox", "/usr/bin", "/usr/lib/firefox/x"}},
		{Package: "yad", Files: []string{"/usr/bin/yad"}},
		{Package: "new", Replaces: []string{"oldpkg"}, Files: []string{"/usr/bin/old", "/usr/bin/repl", "/usr/bin/div", "/usr/lib/firefox/x"}},
	}
	got := Check(debs, owners, map[string]bool{"gone": true}, map[string]string{"/usr/bin/div": ""})
	var s []string
	for _, c := range got {
		s = append(s, c.String())
	}
	want := "firefox: /usr/bin/firefox belongs to firefox-esr|new: /usr/lib/firefox/x belongs to firefox"
	if strings.Join(s, "|") != want {
		t.Errorf("got %q", strings.Join(s, "|"))
	}
	if c := Check(debs[:1], owners, map[string]bool{"firefox-esr": true}, nil); len(c) != 0 {
		t.Errorf("leaving owner: %v", c)
	}
}

func TestOwnersAndNames(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "libc6:amd64.list"), []byte("/.\n/usr/lib/libc.so.6\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "yad.list"), []byte("/usr/bin/yad\n"), 0o644)
	o, err := Owners(dir)
	if err != nil || o["/usr/lib/libc.so.6"] != "libc6" || o["/usr/bin/yad"] != "yad" {
		t.Errorf("got %v, %v", o, err)
	}
	if n := names("a (<< 2), b:any | c"); strings.Join(n, " ") != "a b c" {
		t.Errorf("got %v", n)
	}
}

func TestDiversions(t *testing.T) {
	d := parseDiversions("diversion of /usr/bin/firefox to /usr/bin/firefox.real by firefox-esr\nlocal diversion of /etc/x to /etc/x.orig\n")
	if d["/usr/bin/firefox"] != "firefox-esr" || d["/etc/x"] != "" || len(d) != 2 {
		t.Fatalf("got %v", d)
	}
	ff := []*Deb{{Package: "firefox", Files: []string{"/usr/bin/firefox"}}}
	if c := Check(ff, nil, nil, d); len(c) != 1 || c[0].Owner != "firefox-esr (diverted)" {
		t.Errorf("got %v", c)
	}
	if c := Check(ff, nil, map[string]bool{"firefox-esr": true}, d); len(c) != 0 {
		t.Errorf("leaving: %v", c)
	}
}

func TestListDrains(t *testing.T) {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	w.WriteHeader(&tar.Header{Name: "./usr/bin/x", Mode: 0o755, Typeflag: tar.TypeReg})
	w.WriteHeader(&tar.Header{Name: "./usr/", Typeflag: tar.TypeDir})
	w.Close()
	b.Write(make([]byte, 64<<10)) // padding after the end marker
	files, err := list(&b)
	if err != nil || len(files) != 1 || files[0] != "/usr/bin/x" {
		t.Fatalf("got %v, %v", files, err)
	}
	if b.Len() != 0 {
		t.Errorf("%d bytes left unread: the writer would block", b.Len())
	}
}
