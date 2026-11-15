package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadable(t *testing.T) {
	defer func(v string) { Running = v }(Running)
	for _, c := range []struct{ running, file, want string }{
		{"1.0.0", `dovy = "1.0.0"`, ""},
		{"1.0.1", `dovy = "1.0.0"`, ""},
		{"1.0.0", `dovy = "1.0.1"`, "needs dovy 1.0.1 (this is 1.0.0)"},
		{"1.10.0", `dovy = "1.9.0"`, ""},
		{"0.0.0-dev", `dovy = "9.9.9"`, ""}, // development build
		{"test-904ec40c0271", `dovy = "9.9.9"`, ""},
		{"1.0.0", "dovy = \"1.0.0\"\nlater = \"a field from a newer dovy\"", ""},
		{"1.0.0", `dovy = "1.0"`, `catalog.toml: dovy must be "X.Y.Z", not "1.0"`},
		{"1.0.0", ``, `catalog.toml: dovy must be "X.Y.Z", not ""`},
	} {
		Running = c.running
		if got := readable([]byte(c.file)); got != c.want {
			t.Errorf("dovy %s, %q: got %q, want %q", c.running, c.file, got, c.want)
		}
	}
}

func TestUpdateSkipsTooNew(t *testing.T) {
	bundle, signers := fixture(t, "minimum")
	defer func(v string) { Running = v }(Running)

	// 1.0.0 reads only the oldest release; each newer one says why not.
	Running = "1.0.0"
	dir := filepath.Join(t.TempDir(), "catalog")
	c, err := Update(dir, bundle, signers)
	if err != nil {
		t.Fatal(err)
	}
	want := []Skip{{"catalog-2026.05.12.1", "no catalog.toml"}, {"catalog-2026.05.11.1", "needs dovy 9.9.9 (this is 1.0.0)"}}
	if c.Tag != "catalog-2026.05.10.1" || !reflect.DeepEqual(c.Skipped, want) {
		t.Errorf("got tag %s, skipped %v; want catalog-2026.05.10.1, %v", c.Tag, c.Skipped, want)
	}
	if err := Verify(dir, signers); err != nil {
		t.Fatal(err)
	}

	// A newer dovy reads the newer release; an older one then refuses it.
	Running = "9.9.9"
	if c, err = Update(dir, bundle, signers); err != nil || c.Tag != "catalog-2026.05.11.1" {
		t.Fatalf("9.9.9: tag %s, %v", c.Tag, err)
	}
	Running = "1.0.0"
	err = Verify(dir, signers)
	var tooNew TooNew
	if !errors.As(err, &tooNew) || !strings.Contains(err.Error(), "needs dovy 9.9.9") {
		t.Errorf("downgraded dovy: got %v", err)
	}
}

func TestUpdateAllTooNew(t *testing.T) {
	bundle, signers := fixture(t, "minimum")
	defer func(v string) { Running = v }(Running)
	Running = "0.9.0"
	_, err := Update(filepath.Join(t.TempDir(), "catalog"), bundle, signers)
	if err == nil || !strings.Contains(err.Error(), "no usable release; catalog-2026.05.12.1: no catalog.toml") {
		t.Errorf("got %v", err)
	}
}

func TestVerifyNoMinimum(t *testing.T) {
	bundle, signers := fixture(t, "minimum")
	defer func(v string) { Running = v }(Running)
	Running = "1.0.0"
	dir := filepath.Join(t.TempDir(), "catalog")
	if _, err := Update(dir, bundle, signers); err != nil {
		t.Fatal(err)
	}
	if err := checkMinimum(t.TempDir(), "catalog-x"); err == nil || !strings.Contains(err.Error(), "has no catalog.toml") {
		t.Errorf("missing file: got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, MinimumFile)); err != nil {
		t.Errorf("checked-out release lacks %s: %v", MinimumFile, err)
	}
}
