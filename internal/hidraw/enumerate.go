// Package hidraw finds Linux hidraw nodes through sysfs, parses their HID report
// descriptors, and performs raw report I/O on them.
package hidraw

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrPermission means the node exists but this user may not open it (missing udev rule).
var ErrPermission = errors.New("no permission to use the keyboard")

// Info describes one hidraw node (read from sysfs, no device I/O).
type Info struct {
	Node       string // /dev/hidrawN (changes on every replug; never store it)
	USBPath    string // sysfs path of the owning USB device: identical for all interfaces of one keyboard
	Name       string
	VID        uint16
	PID        uint16
	Iface      int // USB interface number, -1 if unknown
	Raw        []byte
	Descriptor Descriptor
}

// Enumerate lists hidraw nodes whose vendor id matches vid (0 = all).
func Enumerate(vid uint16) ([]Info, error) { return EnumerateAt("/sys", vid) }

// EnumerateAt is Enumerate with an injectable sysfs root (for tests).
func EnumerateAt(sysRoot string, vid uint16) ([]Info, error) {
	nodes, err := filepath.Glob(filepath.Join(sysRoot, "class/hidraw/hidraw*"))
	if err != nil {
		return nil, err
	}
	sort.Slice(nodes, func(i, j int) bool {
		if len(nodes[i]) != len(nodes[j]) {
			return len(nodes[i]) < len(nodes[j])
		}
		return nodes[i] < nodes[j]
	})
	var out []Info
	for _, n := range nodes {
		dev := filepath.Join(n, "device")
		ue, err := os.ReadFile(filepath.Join(dev, "uevent"))
		if err != nil {
			continue
		}
		inf := Info{Node: "/dev/" + filepath.Base(n), Iface: -1}
		for _, l := range strings.Split(string(ue), "\n") {
			switch {
			case strings.HasPrefix(l, "HID_ID="):
				f := strings.Split(strings.TrimPrefix(l, "HID_ID="), ":")
				if len(f) == 3 {
					v, _ := strconv.ParseUint(f[1], 16, 32)
					p, _ := strconv.ParseUint(f[2], 16, 32)
					inf.VID, inf.PID = uint16(v), uint16(p)
				}
			case strings.HasPrefix(l, "HID_NAME="):
				inf.Name = strings.TrimPrefix(l, "HID_NAME=")
			}
		}
		if vid != 0 && inf.VID != vid {
			continue
		}
		if real, err := filepath.EvalSymlinks(dev); err == nil {
			inf.USBPath = filepath.Dir(filepath.Dir(real)) // hid device -> USB interface -> USB device
			if b, err := os.ReadFile(filepath.Join(real, "..", "bInterfaceNumber")); err == nil {
				if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
					inf.Iface = v
				}
			}
		}
		raw, err := os.ReadFile(filepath.Join(dev, "report_descriptor"))
		if err != nil {
			continue
		}
		inf.Raw, inf.Descriptor = raw, ParseDescriptor(raw)
		out = append(out, inf)
	}
	return out, nil
}

// Groups splits interfaces by physical USB device, ordered by sysfs path so the
// numbering is stable while the keyboards stay plugged in.
func Groups(all []Info) [][]Info {
	idx := map[string]int{}
	var keys []string
	for _, i := range all {
		if _, ok := idx[i.USBPath]; !ok {
			idx[i.USBPath] = -1
			keys = append(keys, i.USBPath)
		}
	}
	sort.Strings(keys)
	for n, k := range keys {
		idx[k] = n
	}
	out := make([][]Info, len(keys))
	for _, i := range all {
		out[idx[i.USBPath]] = append(out[idx[i.USBPath]], i)
	}
	return out
}
