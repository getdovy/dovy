package main

import (
	"reflect"
	"sort"
	"testing"

	"github.com/getdovy/dovy/internal/apt"
)

func TestUpgradeRequests(t *testing.T) {
	as := []apt.Action{
		{Op: "Inst", Name: "libc6", Old: "2.41-12", Version: "2.41-12+deb13u1"},
		{Op: "Inst", Name: "linux-image-6.12.99-amd64", Version: "6.12.99-1"},
		{Op: "Remv", Name: "oldlib", Old: "1"},
		{Op: "Inst", Name: "dovy-zram", Old: "1-1~dovy13", Version: "2-1~dovy13"},
	}
	ours := map[string]string{"dovy-zram": "2-1~dovy13"}
	reqs, rm, dovy := upgradeRequests(as, ours, map[string]pkgInfo{})
	sort.Strings(reqs)
	if want := []string{"libc6=2.41-12+deb13u1", "linux-image-6.12.99-amd64=6.12.99-1", "oldlib-"}; !reflect.DeepEqual(reqs, want) || rm["oldlib"] == "" || dovy != nil {
		t.Errorf("got %v %v %v", reqs, rm, dovy)
	}
	_, _, dovy = upgradeRequests([]apt.Action{{Op: "Remv", Name: "dovy-cli-tools"}}, nil, map[string]pkgInfo{"dovy-cli-tools": {Recipe: "cli-tools"}})
	if len(dovy) != 1 {
		t.Errorf("dovy removal not caught: %v", dovy)
	}
}
