package apt

import (
	"os/exec"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0~rc1", "1.0", -1},
		{"25.0.7-2+deb13u1", "26.1.6-1~bpo13+1", -1},
		{"1-1~dovy13", "1-1", -1},
		{"1:1.0", "2.0", 1},
		{"1.0a", "1.0", 1},
		{"1.0-1", "1.0", 1},
		{"6.12.107-1", "7.2.12-1~bpo13+1", -1},
		{"2.4.124-2", "2.4.124-10", -1},
		{"1.0+b1", "1.0", 1},
	}
	for _, c := range cases {
		got := Compare(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("%s vs %s: got %d", c.a, c.b, got)
		}
	}
	// Against dpkg itself, where present.
	if _, err := exec.LookPath("dpkg"); err == nil {
		for _, c := range cases {
			lt := exec.Command("dpkg", "--compare-versions", c.a, "lt", c.b).Run() == nil
			if lt != (Compare(c.a, c.b) < 0) {
				t.Errorf("%s vs %s: dpkg disagrees", c.a, c.b)
			}
		}
	}
}

func TestParseMadison(t *testing.T) {
	out := ` mesa-libgallium | 26.1.6-1~bpo13+1 | http://deb.debian.org/debian trixie-backports/main amd64 Packages
 mesa-libgallium | 25.0.7-2+deb13u1 | http://deb.debian.org/debian trixie/main amd64 Packages
 mesa-libgallium | 25.0.7-2 | http://deb.debian.org/debian trixie/main amd64 Packages
   linux-image-amd64 | 6.12.107-1 | http://deb.debian.org/debian-security trixie-security/main amd64 Packages
       yad | 14.2-1~dovy13 | file:/var/lib/dovy/repo ./ Packages
       yad | 0.40.0-1.1 | http://deb.debian.org/debian trixie/main amd64 Packages
`
	got := parseMadison(out)
	if got["mesa-libgallium"] != "25.0.7-2+deb13u1" || got["linux-image-amd64"] != "6.12.107-1" || got["yad"] != "0.40.0-1.1" {
		t.Errorf("got %v", got)
	}
}
