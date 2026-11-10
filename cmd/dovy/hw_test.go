package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getdovy/dovy/internal/catalog"
)

func fakeGPU(t *testing.T, vendor string) {
	t.Helper()
	root := t.TempDir()
	d := filepath.Join(root, "sys/bus/pci/devices/0000:01:00.0")
	os.MkdirAll(d, 0o755)
	for f, v := range map[string]string{"vendor": "0x" + vendor, "device": "0x1e04", "class": "0x030000"} {
		os.WriteFile(filepath.Join(d, f), []byte(v), 0o644)
	}
	old := sysRoot
	sysRoot = root
	t.Cleanup(func() { sysRoot = old })
}

func TestUnavailable(t *testing.T) {
	rs := map[string]*catalog.Recipe{
		"nvidia": {Name: "nvidia", Hardware: "nvidia-turing"},
		"nvflux": {Name: "nvflux", Depends: []string{"nvidia"}},
		"zram":   {Name: "zram"},
	}
	fakeGPU(t, "8086")
	if got := unavailable(rs["nvflux"], rs); got != "nvidia: no NVIDIA GPU" {
		t.Errorf("nvflux: %q", got)
	}
	if got := unavailable(rs["zram"], rs); got != "" {
		t.Errorf("zram: %q", got)
	}
	fakeGPU(t, "10de")
	if got := unavailable(rs["nvflux"], rs); got != "" {
		t.Errorf("with NVIDIA: %q", got)
	}
}

func TestOriginOf(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "developer.download.nvidia.com_compute_cuda_repos_debian13_x86%5f64_InRelease"),
		[]byte("-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\nOrigin: NVIDIA\nLabel: NVIDIA CUDA\n"), 0o644)
	if o := originOf(d, "developer.download.nvidia.com"); o != "NVIDIA CUDA" {
		t.Errorf("got %q", o)
	}
	if o := originOf(d, "example.org"); o != "" {
		t.Errorf("got %q", o)
	}
}

func TestSecureBootNotes(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "sys/firmware/efi/efivars")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"), []byte{6, 0, 0, 0, 1}, 0o644)
	old := sysRoot
	sysRoot = root
	defer func() { sysRoot = old }()
	rs := []*catalog.Recipe{{Name: "nvidia", SecureBoot: "dkms"}, {Name: "zram"}}
	if p := secureBoot(rs); len(p) != 1 || p[0].Item != "nvidia" {
		t.Errorf("got %v", p)
	}
}
