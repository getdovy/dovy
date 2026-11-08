package apt

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// Fixtures are real apt-get output from Debian 13.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParse(t *testing.T) {
	as := Parse(fixture(t, "sim-install.txt"))
	if len(as) != 17 {
		t.Fatalf("got %d actions", len(as))
	}
	want := Action{Op: "Inst", Name: "dovy-cli-tools", Version: "1-1~dovy13", Origin: "dovy"}
	if as[16] != want {
		t.Errorf("got %+v", as[16])
	}
	if as[0].Origin != "Debian" || as[0].Version != "1.21.3-5+deb13u1" {
		t.Errorf("got %+v", as[0])
	}

	var remv []string
	for _, a := range Parse(fixture(t, "sim-surprise.txt")) {
		if a.Op == "Remv" {
			remv = append(remv, a.Name+" "+a.Old)
		}
	}
	if !reflect.DeepEqual(remv, []string{"exim4-daemon-light 4.98.2-1+deb13u5", "exim4-config 4.98.2-1+deb13u5", "exim4-base 4.98.2-1+deb13u5"}) {
		t.Errorf("got %v", remv)
	}

	if a := Parse("Inst libc6 [2.41-11] (2.41-12 Debian:13.7/stable [amd64])"); a[0].Old != "2.41-11" || a[0].Version != "2.41-12" {
		t.Errorf("upgrade: %+v", a)
	}
	if a := Parse(fixture(t, "sim-purge.txt")); len(a) != 1 || a[0].Op != "Purg" || a[0].Old != "1-1~dovy13" {
		t.Errorf("purge: %+v", a)
	}
}

func TestNoLongerRequired(t *testing.T) {
	one := "The following package was automatically installed and is no longer required:\n  dovy-backports\nUse 'apt autoremove' to remove it.\n"
	if got := NoLongerRequired(one); len(got) != 1 || got[0] != "dovy-backports" {
		t.Errorf("one package: %v", got)
	}
	got := NoLongerRequired(fixture(t, "sim-purge.txt"))
	if len(got) != 9 || got[0] != "bat" || got[8] != "ripgrep" {
		t.Errorf("got %v", got)
	}
}

func TestStatus(t *testing.T) {
	var tidy []string
	for _, l := range strings.Split(fixture(t, "install.txt"), "\n") {
		s, ok := ParseStatus(l)
		if ok && s.Verb != "" {
			tidy = append(tidy, s.Verb+" "+s.Package)
		}
		if !ok && !Chatter(l) && !strings.HasPrefix(l, "debconf: ") {
			t.Errorf("not chatter: %q", l)
		}
	}
	if len(tidy) != 20 || tidy[0] != "Unpacking: libhttp-parser2.9" || tidy[19] != "Setting up: dovy-cli-tools" {
		t.Errorf("got %d: %v", len(tidy), tidy)
	}
	cases := map[string]Status{
		"pmstatus:man-db:90.0:Running post-installation trigger man-db": {"Processing:", "man-db"},
		"pmstatus:bat:10.0:Removing bat (amd64)":                        {"Removing:", "bat"},
		"pmerror:bat:10.0:subprocess failed":                            {"Error:", "bat: subprocess failed"},
	}
	for l, want := range cases {
		if s, _ := ParseStatus(l); s != want {
			t.Errorf("%s: got %+v", l, s)
		}
	}
	if _, ok := ParseStatus("Setting up bat (0.25.0-2+b2) ..."); ok {
		t.Error("dpkg text read as status")
	}
	if Chatter("Created symlink /etc/systemd/system/x.service.") {
		t.Error("maintainer script output hidden")
	}
}

func TestParseFetch(t *testing.T) {
	cases := map[string]Fetch{
		"Get:1 http://deb.debian.org/debian trixie/main amd64 ripgrep amd64 14.1.1-1+b4 [1434 kB]": {"Get", "ripgrep 14.1.1-1+b4", "1434 kB", true},
		"Hit:1 http://deb.debian.org/debian trixie InRelease":                                      {"Hit", "http://deb.debian.org/debian trixie InRelease", "", false},
		"Get:2 file:/var/lib/dovy/repo ./ Release [236 B]":                                         {"Get", "file:/var/lib/dovy/repo ./ Release", "236 B", false},
		"Get:4 http://deb.debian.org/debian trixie/main amd64 Packages [9670 kB]":                  {"Get", "http://deb.debian.org/debian trixie/main amd64 Packages", "9670 kB", false},
		"Get:1 /tmp/dovy-cli-tools_1-1~dovy13_all.deb dovy-cli-tools all 1-1~dovy13 [684 B]":       {"Get", "dovy-cli-tools 1-1~dovy13", "684 B", true},
		"Get:9 file:/var/lib/dovy/repo ./ pfetch 3.1.1-1~dovy13 [1212 kB]":                         {"Get", "pfetch 3.1.1-1~dovy13", "1212 kB", true},
	}
	for l, want := range cases {
		if f, ok := ParseFetch(l); !ok || f != want {
			t.Errorf("%s: got %+v", l, f)
		}
	}
}

func TestBytes(t *testing.T) {
	for in, want := range map[string]int64{"1434 kB": 1434000, "236 B": 236, "9.6 MB": 9600000, "1,234 kB": 1234000, "x": -1} {
		if got := Bytes(in); got != want {
			t.Errorf("%s: got %d", in, got)
		}
	}
}
