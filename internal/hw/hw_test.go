//go:build hardware

package hw

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/gallery"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/drivers"
)

// Run:  go test -tags hardware -v ./internal/hw
// Read-only by default. Writes need AK820_HW_WRITE=1 (clock) and AK820_HW_UPLOAD=1 (picture).

func find(t *testing.T) (*keyboard.Finder, []hidraw.Info) {
	t.Helper()
	f := drivers.System()
	if v := os.Getenv("AK820_DEVICE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatal(err)
		}
		f = f.WithDevice(n)
	}
	ifaces, err := f.Interfaces()
	if err != nil {
		t.Skipf("no keyboard available: %v", err)
	}
	return f, ifaces
}

func TestReadOnlyInterfacesAreRecognised(t *testing.T) {
	f, ifaces := find(t)
	t.Logf("layout: %s", f.Layout(ifaces))
	for _, i := range ifaces {
		t.Logf("%s if%d page %#04x in %d out %d feat %d rw=%v", i.Node, i.Iface, i.Descriptor.Page, i.Descriptor.InBytes(), i.Descriptor.OutBytes(), i.Descriptor.FeatBytes(), i.Accessible())
		if !i.Accessible() {
			t.Errorf("%s: no read/write permission (run `ak820 setup`)", i.Node)
		}
	}
	if f.Layout(ifaces) == keyboard.LayoutUnknown {
		t.Fatalf("layout not recognised; run `ak820 report --issue` and share the output")
	}
}

func TestReadOnlyGetFeature(t *testing.T) {
	_, ifaces := find(t)
	for _, i := range ifaces {
		if i.Descriptor.FeatBytes() == 0 {
			continue
		}
		d, err := hidraw.Open(i)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		r, err := d.GetFeature(0, i.Descriptor.FeatBytes()) // a read; nothing is sent
		if err != nil {
			t.Fatalf("GET_FEATURE: %v", err)
		}
		t.Logf("GET_FEATURE %s: % x", i.Node, r[:min(16, len(r))])
	}
}

func TestWriteClockSync(t *testing.T) {
	if os.Getenv("AK820_HW_WRITE") != "1" {
		t.Skip("set AK820_HW_WRITE=1 to run (writes the keyboard clock)")
	}
	f, ifaces := find(t)
	drv, err := f.Driver(ifaces, keyboard.Override{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := drv.PlanClock(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Run(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	t.Log("clock command accepted. LOOK AT THE KEYBOARD SCREEN and record whether the time changed.")
}

func TestWriteUploadPicture(t *testing.T) {
	if os.Getenv("AK820_HW_UPLOAD") != "1" {
		t.Skip("set AK820_HW_UPLOAD=1 to run (overwrites the picture stored in the keyboard)")
	}
	f, ifaces := find(t)
	drv, err := f.Driver(ifaces, keyboard.Override{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := gallery.Data("bongo_cat")
	if err != nil {
		t.Fatal(err)
	}
	frames, err := animation.Decode(data, animation.FitCover, animation.ParseColor("black"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := drv.PlanUpload(frames)
	if err != nil {
		t.Fatal(err)
	}
	last := 0
	if err := plan.Run(t.Context(), func(d, n int) { last = d * 100 / n }); err != nil {
		t.Fatalf("upload failed at %d%%: %v", last, err)
	}
	t.Log("upload finished. LOOK AT THE KEYBOARD SCREEN, then unplug and replug and look again.")
}
