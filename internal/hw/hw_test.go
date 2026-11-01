//go:build hardware

package hw

import (
	"os"
	"testing"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/core"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/gallery"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/img"
)

// Run:  go test -tags hardware -v ./internal/hw
// Read-only by default. Writes need AK820_HW_WRITE=1 (clock) and AK820_HW_UPLOAD=1 (picture).

func keyboard(t *testing.T) (*core.Core, []hidraw.Info) {
	t.Helper()
	c := core.New()
	if v := os.Getenv("AK820_DEVICE"); v != "" {
		for _, ch := range v {
			c.Device = c.Device*10 + int(ch-'0')
		}
	}
	all, err := c.Devices()
	if err != nil {
		t.Skipf("no keyboard available: %v", err)
	}
	return c, all
}

func TestReadOnlyInterfacesAreRecognised(t *testing.T) {
	_, all := keyboard(t)
	t.Logf("layout: %s", core.Layout(all))
	for _, i := range all {
		t.Logf("%s if%d page %#04x in %d out %d feat %d rw=%v", i.Node, i.Iface, i.Descriptor.Page, i.Descriptor.InBytes(), i.Descriptor.OutBytes(), i.Descriptor.FeatBytes(), i.Accessible())
		if !i.Accessible() {
			t.Errorf("%s: no read/write permission (run `ak820 setup`)", i.Node)
		}
	}
	if core.Layout(all) == "unknown" {
		t.Fatalf("layout not recognised; run `ak820 report --issue` and share the output")
	}
}

func TestReadOnlyGetFeature(t *testing.T) {
	c, all := keyboard(t)
	ctl, _ := core.SonixLayout(all)
	if ctl == nil {
		t.Skip("not a sonix layout")
	}
	d, err := c.OpenDev(*ctl)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r, err := d.GetFeature(0, 64) // a read; nothing is sent
	if err != nil {
		t.Fatalf("GET_FEATURE: %v", err)
	}
	t.Logf("GET_FEATURE: % x", r[:16])
}

func TestWriteClockSync(t *testing.T) {
	if os.Getenv("AK820_HW_WRITE") != "1" {
		t.Skip("set AK820_HW_WRITE=1 to run (writes the keyboard clock)")
	}
	c, _ := keyboard(t)
	if _, err := c.SetTime(core.Target{}, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	t.Log("clock command accepted. LOOK AT THE KEYBOARD SCREEN and record whether the time changed.")
}

func TestWriteUploadPicture(t *testing.T) {
	if os.Getenv("AK820_HW_UPLOAD") != "1" {
		t.Skip("set AK820_HW_UPLOAD=1 to run (overwrites the picture stored in the keyboard)")
	}
	c, _ := keyboard(t)
	data, err := gallery.Data("bongo_cat")
	if err != nil {
		t.Fatal(err)
	}
	frames, err := img.Decode(data, "cover", img.ParseColor("black"))
	if err != nil {
		t.Fatal(err)
	}
	last := 0
	if _, err := c.Upload(core.Target{}, img.BuildPayload(frames), false, func(d, n int) { last = d * 100 / n }); err != nil {
		t.Fatalf("upload failed at %d%%: %v", last, err)
	}
	t.Log("upload finished. LOOK AT THE KEYBOARD SCREEN, then unplug and replug and look again.")
}
