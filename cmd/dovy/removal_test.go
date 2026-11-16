package main

import (
	"reflect"
	"testing"
)

func TestInstalledAs(t *testing.T) {
	inst := map[string]pkgInfo{"mesa-va-drivers:amd64": {Version: "26.1.6-1~bpo13+1"}, "firmware-misc-nonfree": {Version: "20260916-1~bpo13+1"}, "libgl1:i386": {Version: "1"}}
	for _, c := range []struct {
		p  string
		ok bool
	}{{"mesa-va-drivers", true}, {"mesa-va-drivers:amd64", true}, {"firmware-misc-nonfree", true}, {"libgl1", false}, {"libgl1:i386", true}, {"absent", false}} {
		if _, ok := installedAs(inst, c.p); ok != c.ok {
			t.Errorf("installedAs(%s) = %v, want %v", c.p, ok, c.ok)
		}
	}
}

func TestParseConffiles(t *testing.T) {
	in := " /etc/systemd/zram-generator.conf 5d41402abc4b2a76b9719d911017c592\n /etc/old.conf 098f6bcd4621d373cade4e832627b4f6 obsolete\n garbage\n"
	want := []conffile{{"/etc/systemd/zram-generator.conf", "5d41402abc4b2a76b9719d911017c592"}, {"/etc/old.conf", "098f6bcd4621d373cade4e832627b4f6"}}
	if got := parseConffiles(in); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
