package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// escape is the classic way out of a chroot for root: chroot deeper while
// the working directory stays outside, then walk up.
const escape = `perl -e 'mkdir "/tmp/x"; chroot("/tmp/x") or die; chdir("..") for 1..64; chroot(".") or die;
print((-e $ARGV[0]) ? "visible\n" : "hidden\n")' "$SECRET" >/build/result`

// Needs the network, mmdebstrap and uidmap.
func TestHostHidden(t *testing.T) {
	if os.Getenv("DOVY_TEST_SANDBOX") == "" {
		t.Skip("set DOVY_TEST_SANDBOX=1")
	}
	if err := Check(); err != nil {
		t.Skip(err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	b := &Box{Dir: filepath.Join(cache, "dovy-test", "sandbox"), Codename: "trixie", Log: os.Stderr}
	if err := b.Base(); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "host-file")
	os.WriteFile(secret, nil, 0o644)
	work := filepath.Join(cache, "dovy-test", "work")
	os.MkdirAll(work, 0o755)
	defer Remove(work)
	if err := b.Run("", work, "SECRET="+secret+"\n"+escape, false); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(work, "result"))
	if strings.TrimSpace(string(got)) != "hidden" {
		t.Errorf("host file from inside the sandbox: %q", got)
	}
}
