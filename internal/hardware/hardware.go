// Package hardware reads the machine for recipes' hardware checks
// (principles 5.6, 6.7, ADR 0017).
package hardware

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GPU is one PCI display device.
type GPU struct {
	Slot                        string // 0000:01:00.0
	Vendor, Device              string // 10de, 1e04
	SubVendor, SubDevice, Class string
}

func (g GPU) String() string { return fmt.Sprintf("%s:%s at %s", g.Vendor, g.Device, g.Slot) }

// NVIDIA is NVIDIA's PCI vendor ID.
const NVIDIA = "10de"

// GPUs lists display devices (PCI class 03xxxx) under root/sys.
func GPUs(root string) ([]GPU, error) {
	dirs, err := filepath.Glob(filepath.Join(root, "sys/bus/pci/devices/*"))
	if err != nil {
		return nil, err
	}
	var gs []GPU
	for _, d := range dirs {
		read := func(f string) string {
			b, _ := os.ReadFile(filepath.Join(d, f))
			return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(string(b))), "0x")
		}
		g := GPU{Slot: filepath.Base(d), Vendor: read("vendor"), Device: read("device"),
			SubVendor: read("subsystem_vendor"), SubDevice: read("subsystem_device"), Class: read("class")}
		if strings.HasPrefix(g.Class, "03") {
			gs = append(gs, g)
		}
	}
	return gs, nil
}

// Of returns the GPUs from vendor.
func Of(gs []GPU, vendor string) []GPU {
	var out []GPU
	for _, g := range gs {
		if g.Vendor == vendor {
			out = append(out, g)
		}
	}
	return out
}

// Table is NVIDIA's open-module table: device → subsystems ("" for any).
type Table map[string][]string

var rowRe = regexp.MustCompile(`^\|[^|]+\|\s*([0-9A-Fa-f]{4})(?:\s+([0-9A-Fa-f]{4})\s+([0-9A-Fa-f]{4}))?\s*\|\s*$`)

// ParseTable reads the GPU table in NVIDIA's open-gpu-kernel-modules
// README: a device ID, or device, subsystem vendor and subsystem IDs.
func ParseTable(readme string) (Table, error) {
	t := Table{}
	for _, l := range strings.Split(readme, "\n") {
		m := rowRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		dev := strings.ToLower(m[1])
		sub := ""
		if m[2] != "" {
			sub = strings.ToLower(m[2] + ":" + m[3])
		}
		t[dev] = append(t[dev], sub)
	}
	if len(t) < 100 {
		return nil, fmt.Errorf("NVIDIA's GPU table: %d entries, expected hundreds", len(t))
	}
	return t, nil
}

// Uncovered returns the NVIDIA GPUs the table does not list.
func Uncovered(gs []GPU, t Table) []GPU {
	var out []GPU
	for _, g := range Of(gs, NVIDIA) {
		ok := false
		for _, sub := range t[g.Device] {
			if sub == "" || sub == g.SubVendor+":"+g.SubDevice {
				ok = true
			}
		}
		if !ok {
			out = append(out, g)
		}
	}
	return out
}

// SecureBoot reports whether Secure Boot is on, from root's EFI variables.
func SecureBoot(root string) bool {
	b, err := os.ReadFile(filepath.Join(root, "sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"))
	return err == nil && len(b) == 5 && b[4] == 1 // 4 attribute bytes, then the value
}

// ReadmeURL is NVIDIA's README at a driver version.
func ReadmeURL(version string) string {
	return "https://raw.githubusercontent.com/NVIDIA/open-gpu-kernel-modules/" + version + "/README.md"
}
