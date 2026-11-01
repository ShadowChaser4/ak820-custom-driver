package hidraw

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The four interface descriptors of a real AK820 Pro (0c45:800a), as captured
// from /sys/class/hidraw/hidraw5-8/device/report_descriptor.
var realUnit = []struct {
	iface int
	desc  []byte
}{
	{0, h("0501 0906 a101 0507 19e0 29e7 1500 2501 7501 9508 8102 9501 7508 8101 9505 7501 0508 1901 2905 9102 9501 7503 9101 9506 7508 1500 26ff 0005 0719 002a 6500 8100 c0")},
	{1, h("050c 0901 a101 8503 1900 2a3c03 1500 263c03 9501 7510 8100 c0")},
	{2, h("0668ff0961a1010962150026ff00954075088102 096315 0026ff00 960010 7508 9102 c0")},
	{3, h("0613ff0901a1011500 26ff00 7508 9540 0902 8102 0903 9102 0a00ff b102 c0")},
}

// addUnit builds one keyboard in a fake sysfs: usb device `usb`, hidraw numbers
// starting at first. It mirrors the real layout:
//
//	devices/.../<usb>/<usb>:1.<if>/<hid id>/{uevent,report_descriptor}
//	devices/.../<usb>/<usb>:1.<if>/bInterfaceNumber
//	class/hidraw/hidrawN/device -> the hid dir
func addUnit(t *testing.T, root, usb string, vid, pid uint16, first int) {
	t.Helper()
	for k, u := range realUnit {
		ifDir := filepath.Join(root, "devices/pci0000:00/usb1", usb, fmt.Sprintf("%s:1.%d", usb, u.iface))
		hid := filepath.Join(ifDir, fmt.Sprintf("0003:%04X:%04X.%04d", vid, pid, first+k))
		for _, d := range []string{hid, filepath.Join(root, "class/hidraw", fmt.Sprintf("hidraw%d", first+k))} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		write := func(p string, b []byte) {
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write(filepath.Join(ifDir, "bInterfaceNumber"), []byte(fmt.Sprintf("%02d\n", u.iface)))
		write(filepath.Join(hid, "uevent"), []byte(fmt.Sprintf("HID_ID=0003:%08X:%08X\nHID_NAME=SONiX AK820\n", vid, pid)))
		write(filepath.Join(hid, "report_descriptor"), u.desc)
		if err := os.Symlink(hid, filepath.Join(root, "class/hidraw", fmt.Sprintf("hidraw%d", first+k), "device")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnumerateRealUnit(t *testing.T) {
	root := t.TempDir()
	addUnit(t, root, "1-1", 0x0C45, 0x800A, 5)
	got, err := EnumerateAt(root, 0x0C45)
	if err != nil || len(got) != 4 {
		t.Fatalf("%v, %d interfaces", err, len(got))
	}
	for k, want := range []struct {
		node         string
		page         uint32
		out, feature int
	}{{"/dev/hidraw5", 0x01, 1, 0}, {"/dev/hidraw6", 0x0C, 0, 0}, {"/dev/hidraw7", 0xFF68, 4096, 0}, {"/dev/hidraw8", 0xFF13, 64, 64}} {
		g := got[k]
		if g.Node != want.node || g.Descriptor.Page != want.page || g.Descriptor.OutBytes() != want.out || g.Descriptor.FeatBytes() != want.feature {
			t.Errorf("#%d: %s page %#x out %d feat %d", k, g.Node, g.Descriptor.Page, g.Descriptor.OutBytes(), g.Descriptor.FeatBytes())
		}
		if g.Iface != k || g.VID != 0x0C45 || g.PID != 0x800A || g.Name != "SONiX AK820" {
			t.Errorf("#%d identity: iface %d %04x:%04x %q", k, g.Iface, g.VID, g.PID, g.Name)
		}
		if g.USBPath == "" || g.USBPath != got[0].USBPath {
			t.Errorf("#%d: all interfaces of one keyboard must share a Device, got %q vs %q", k, g.USBPath, got[0].USBPath)
		}
	}
}

// The same keyboard after a replug: different hidraw numbers, identical content.
func TestEnumerateAfterRenumberingFindsTheSameInterfaces(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	addUnit(t, a, "1-1", 0x0C45, 0x800A, 5)
	addUnit(t, b, "1-1", 0x0C45, 0x800A, 3)
	ga, _ := EnumerateAt(a, 0x0C45)
	gb, _ := EnumerateAt(b, 0x0C45)
	if len(ga) != 4 || len(gb) != 4 {
		t.Fatal("expected 4 interfaces each")
	}
	if gb[0].Node != "/dev/hidraw3" || gb[3].Node != "/dev/hidraw6" {
		t.Errorf("nodes %s .. %s", gb[0].Node, gb[3].Node)
	}
	for k := range ga {
		if ga[k].Descriptor != gb[k].Descriptor || ga[k].Iface != gb[k].Iface {
			t.Errorf("#%d differs after renumbering", k)
		}
	}
}

func TestEnumerateTwoKeyboardsAreGroupedSeparately(t *testing.T) {
	root := t.TempDir()
	addUnit(t, root, "1-1", 0x0C45, 0x800A, 3)
	addUnit(t, root, "1-2", 0x0C45, 0x800A, 7)
	all, _ := EnumerateAt(root, 0x0C45)
	groups := Groups(all)
	if len(all) != 8 || len(groups) != 2 || len(groups[0]) != 4 || len(groups[1]) != 4 {
		t.Fatalf("%d interfaces, %d groups", len(all), len(groups))
	}
	if groups[0][0].USBPath == groups[1][0].USBPath {
		t.Error("two physical keyboards must have different Device keys")
	}
	for _, g := range groups {
		for _, i := range g {
			if i.USBPath != g[0].USBPath {
				t.Error("a group mixes interfaces of different keyboards")
			}
		}
	}
}

func TestEnumerateFiltersVendorAndSkipsBrokenNodes(t *testing.T) {
	root := t.TempDir()
	addUnit(t, root, "1-1", 0x0C45, 0x800A, 3)
	addUnit(t, root, "1-3", 0x046D, 0xC31C, 20) // someone else's keyboard
	// break one node of ours: no report_descriptor
	broken, _ := filepath.Glob(filepath.Join(root, "devices/pci0000:00/usb1/1-1/1-1:1.1/*/report_descriptor"))
	if len(broken) != 1 {
		t.Fatal("fixture")
	}
	os.Remove(broken[0])
	got, err := EnumerateAt(root, 0x0C45)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 usable interfaces (one broken, other vendor excluded), got %d", len(got))
	}
	all, _ := EnumerateAt(root, 0)
	if len(all) != 7 {
		t.Errorf("vid 0 means every vendor: got %d", len(all))
	}
}

func TestEnumerateWithNoSysfsReturnsNothingNotAnError(t *testing.T) {
	got, err := EnumerateAt(t.TempDir(), 0x0C45)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %d", err, len(got))
	}
}

func TestEnumerateOrdersHidrawNumbersNumerically(t *testing.T) {
	root := t.TempDir()
	addUnit(t, root, "1-1", 0x0C45, 0x800A, 8) // hidraw8, 9, 10, 11
	got, _ := EnumerateAt(root, 0x0C45)
	want := []string{"/dev/hidraw8", "/dev/hidraw9", "/dev/hidraw10", "/dev/hidraw11"}
	for k, w := range want {
		if got[k].Node != w {
			t.Errorf("#%d = %s, want %s", k, got[k].Node, w)
		}
	}
}
