package hardware

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pci(t *testing.T, root, slot, vendor, device, sub, class string) {
	t.Helper()
	d := filepath.Join(root, "sys/bus/pci/devices", slot)
	os.MkdirAll(d, 0o755)
	sv, sd, _ := strings.Cut(sub, ":")
	for f, v := range map[string]string{"vendor": "0x" + vendor, "device": "0x" + device,
		"subsystem_vendor": "0x" + sv, "subsystem_device": "0x" + sd, "class": "0x" + class} {
		os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644)
	}
}

func table() string {
	var b strings.Builder
	b.WriteString("| Product | PCI ID |\n|---|---|\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "| Some GPU | %04X |\n", 0x2000+i)
	}
	b.WriteString("| NVIDIA GeForce RTX 2080 Ti                              | 1E04           |\n")
	b.WriteString("| Quadro RTX 8000                                         | 1E30 1028 129E |\n")
	return b.String()
}

func TestTuring(t *testing.T) {
	tb, err := ParseTable(table())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pci(t, root, "0000:00:02.0", "8086", "a780", "8086:0000", "030000") // Intel iGPU
	pci(t, root, "0000:01:00.0", "10de", "1e04", "10de:12fa", "030000") // RTX 2080 Ti
	pci(t, root, "0000:01:00.1", "10de", "10f7", "10de:12fa", "040300") // its audio
	pci(t, root, "0000:02:00.0", "10de", "1e30", "103c:129e", "030200") // Quadro, other subsystem
	gs, err := GPUs(root)
	if err != nil || len(gs) != 3 || len(Of(gs, NVIDIA)) != 2 {
		t.Fatalf("got %v, %v", gs, err)
	}
	u := Uncovered(gs, tb)
	if len(u) != 1 || u[0].Device != "1e30" {
		t.Errorf("uncovered: %v", u)
	}
	pci(t, root, "0000:02:00.0", "10de", "1e30", "1028:129e", "030200")
	gs, _ = GPUs(root)
	if u := Uncovered(gs, tb); len(u) != 0 {
		t.Errorf("subsystem match: %v", u)
	}
	if _, err := ParseTable("| x | 1E04 |\n"); err == nil {
		t.Error("short table accepted")
	}
}

func TestSecureBoot(t *testing.T) {
	root := t.TempDir()
	if SecureBoot(root) {
		t.Error("on without the variable")
	}
	d := filepath.Join(root, "sys/firmware/efi/efivars")
	os.MkdirAll(d, 0o755)
	p := filepath.Join(d, "SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c")
	os.WriteFile(p, []byte{6, 0, 0, 0, 1}, 0o644)
	if !SecureBoot(root) {
		t.Error("off with value 1")
	}
	os.WriteFile(p, []byte{6, 0, 0, 0, 0}, 0o644)
	if SecureBoot(root) {
		t.Error("on with value 0")
	}
}
