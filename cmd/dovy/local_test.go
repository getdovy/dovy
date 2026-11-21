package main

import (
	"strings"
	"testing"

	"github.com/getdovy/dovy/internal/catalog"
)

func TestUnstripped(t *testing.T) {
	list := strings.Join([]string{
		"/home/u/.local/share/fonts/arial.ttf\tArial\taa|ar|en",           // Windows Arial copied by hand
		"/usr/share/fonts/truetype/fonts-windows/arial.ttf\tArial\taa|en", // stripped by fonts-windows
		"/home/u/.local/share/fonts/segoeuisl.ttf\tSegoe UI,Segoe UI Semilight\tar|fa",
		"/home/u/fonts/cambria.ttc\tCambria\tar|en",                               // a collection: not stripped, not reported
		"/usr/share/fonts/truetype/noto/NotoSansArabic.ttf\tNoto Sans Arabic\tar", // not a Microsoft family
		"/home/u/fonts/verdana.ttf\tVerdana\ten",
		"",
	}, "\n")
	if got := strings.Join(unstripped(list), " "); got != "/home/u/.local/share/fonts" {
		t.Errorf("got %q", got)
	}
}

func TestLocalUnavailable(t *testing.T) {
	r := &catalog.Recipe{Name: "fonts-windows", Kind: catalog.LocalBuild, Local: &catalog.Local{Path: "~/Downloads/fonts-ms", Match: []string{"*.ttf", "*.otf"}}}
	if why := machine(r); why != "no *.ttf, *.otf files in ~/Downloads/fonts-ms" {
		t.Errorf("no files: %q", why)
	}
	r.Input = "20261120153000"
	if why := machine(r); why != "" {
		t.Errorf("with files: %q", why)
	}
	if s := source(r); s != "your files: *.ttf, *.otf in ~/Downloads/fonts-ms" {
		t.Errorf("source %q", s)
	}
}

func TestLocalGone(t *testing.T) {
	r := &catalog.Recipe{Name: "fonts-windows", Kind: catalog.LocalBuild, Version: "1", Local: &catalog.Local{Path: "~/Downloads/fonts-ms", Match: []string{"*.ttf"}}}
	// Folder gone: nothing to offer, so an installed 1+... is not "update 1".
	if !gone(r) || offered(r) != "" {
		t.Errorf("folder gone: gone=%v offered=%q", gone(r), offered(r))
	}
	// Folder back, changed since install: its new version is offered.
	r.Input = "20261121090000"
	if gone(r) || offered(r) != "1+20261121090000" {
		t.Errorf("folder back: gone=%v offered=%q", gone(r), offered(r))
	}
	// Other kinds always offer their version.
	if o := offered(&catalog.Recipe{Kind: catalog.Setup, Version: "1"}); o != "1" {
		t.Errorf("setup: %q", o)
	}
}
