package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// Tests never read the machine's own record of the release it used.
func TestMain(m *testing.M) {
	RecordPath = filepath.Join(os.TempDir(), "dovy-test-no-record")
	os.Exit(m.Run())
}
