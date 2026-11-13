package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getdovy/dovy/internal/catalog"
)

// Tests never read the machine's own record of the release it used.
func TestMain(m *testing.M) {
	catalog.RecordPath = filepath.Join(os.TempDir(), "dovy-test-no-record")
	os.Exit(m.Run())
}
