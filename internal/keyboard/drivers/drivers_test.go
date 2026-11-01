package drivers

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"image"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/legacy"
)

func desc(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

func iface(node string, n int, raw []byte) hidraw.Info {
	return hidraw.Info{Node: node, VID: 0x0C45, PID: 0x800A, Iface: n, Raw: raw, Descriptor: hidraw.ParseDescriptor(raw)}
}

// realUnit is a real 0c45:800a keyboard (descriptors captured from hidraw5-8).
func realUnit() []hidraw.Info {
	return []hidraw.Info{
		iface("/dev/hidraw5", 0, desc("0501 0906 a101 0507 19e0 29e7 1500 2501 7501 9508 8102 9501 7508 8101 9505 7501 0508 1901 2905 9102 9501 7503 9101 9506 7508 1500 26ff00 0507 1900 2a6500 8100 c0")),
		iface("/dev/hidraw7", 2, desc("0668ff0961a1010962150026ff00954075088102 096315 0026ff00 960010 7508 9102 c0")),
		iface("/dev/hidraw8", 3, desc("0613ff0901a1011500 26ff00 7508 9540 0902 8102 0903 9102 0a00ff b102 c0")),
	}
}

// legacyUnit has a config page and the bulk page 0xFF67.
func legacyUnit() []hidraw.Info {
	return []hidraw.Info{
		iface("/dev/hidraw1", 2, desc("0668ff0901a1011500 26ff00 7508 9520 0902 8102 0903 9102 c0")),
		iface("/dev/hidraw2", 3, desc("0667ff0901a1011500 26ff00 7508 9520 0902 8102 0903 960810 9102 c0")),
	}
}

type fakeDev struct {
	r    *rig
	node string
}

func (f *fakeDev) Write(p []byte) error {
	f.r.writes = append(f.r.writes, append([]byte(nil), p...))
	f.r.released++
	f.r.bytes += len(p)
	for f.r.bytes >= 4096 && f.r.ackBig {
		f.r.bytes -= 4096
		f.r.acks++
	}
	return nil
}

func (f *fakeDev) Read(ctx context.Context, t time.Duration) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.r.acks > 0 {
		f.r.acks--
		return []byte{0x01, 0x5A, 0x02}, nil
	}
	if q := f.r.replies[f.node]; len(q) > 0 && f.r.released > 0 { // one reply per request
		f.r.released--
		f.r.replies[f.node] = q[1:]
		return q[0], nil
	}
	time.Sleep(t)
	return nil, os.ErrDeadlineExceeded
}
func (f *fakeDev) GetFeature(byte, int) ([]byte, error) { return make([]byte, 65), nil }
func (f *fakeDev) SetFeature(p []byte, _ int) error {
	f.r.feats = append(f.r.feats, append([]byte(nil), p...))
	return nil
}
func (f *fakeDev) Close() error { f.r.closes++; return nil }

type rig struct {
	f             *keyboard.Finder
	ifaces        []hidraw.Info
	opens, closes int
	writes, feats [][]byte
	replies       map[string][][]byte
	bytes, acks   int
	released      int
	ackBig        bool
}

func newRig(ifaces []hidraw.Info) *rig {
	r := &rig{ifaces: ifaces, replies: map[string][][]byte{}}
	r.f = NewFinder(
		func() ([]hidraw.Info, error) { return r.ifaces, nil },
		func(i hidraw.Info) (keyboard.Device, error) { r.opens++; return &fakeDev{r: r, node: i.Node}, nil },
	)
	return r
}

func (r *rig) driver(t *testing.T) keyboard.Driver {
	t.Helper()
	all, err := r.f.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.f.Driver(all, keyboard.Override{})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func pic(delay int) []animation.Frame {
	return []animation.Frame{{Img: image.NewRGBA(image.Rect(0, 0, animation.Width, animation.Height)), DelayMs: delay}}
}

func withUSB(all []hidraw.Info, key string, pid uint16) []hidraw.Info {
	out := append([]hidraw.Info(nil), all...)
	for i := range out {
		out[i].USBPath, out[i].PID = key, pid
		out[i].Node += "-" + key
	}
	return out
}

func TestLayoutDetection(t *testing.T) {
	r := newRig(realUnit())
	if got := r.f.Layout(realUnit()); got != keyboard.LayoutSonix {
		t.Errorf("real unit: %v", got)
	}
	if got := r.f.Layout(legacyUnit()); got != keyboard.LayoutLegacy {
		t.Errorf("legacy unit: %v", got)
	}
	only := realUnit()[:2] // keyboard + FF68 only: no control interface
	if got := r.f.Layout(only); got != keyboard.LayoutUnknown {
		t.Errorf("partial unit: %v", got)
	}
	if _, err := r.f.Driver(only, keyboard.Override{}); !errors.Is(err, keyboard.ErrUnknownLayout) {
		t.Errorf("must refuse to guess: %v", err)
	}
}

func TestLayoutMarshalsByName(t *testing.T) {
	b, err := keyboard.LayoutSonix.MarshalText()
	if err != nil || string(b) != "sonix" || keyboard.LayoutUnknown.String() != "unknown" {
		t.Errorf("%s %v", b, err)
	}
}

// Regression: the keyboard came back as hidraw3-6 after a replug and the tool failed
// with "/dev/hidraw8 is not a 0c45 hidraw node". Every run now works from a fresh
// enumeration, and nothing about nodes is stored.
func TestDriverSurvivesRenumbering(t *testing.T) {
	r := newRig(realUnit())
	if d := mustClock(t, r.driver(t)); d.Node != "/dev/hidraw8" {
		t.Fatalf("before: %s", d.Node)
	}
	renamed := realUnit()
	renamed[1].Node, renamed[2].Node = "/dev/hidraw5", "/dev/hidraw6"
	r.ifaces = renamed
	drv := r.driver(t)
	if d := mustClock(t, drv); d.Node != "/dev/hidraw6" || d.ReportSize != 64 {
		t.Fatalf("clock after replug: %+v", d)
	}
	plan, err := drv.PlanUpload(pic(100))
	if err != nil {
		t.Fatal(err)
	}
	if d := plan.Describe(); d.Node != "/dev/hidraw5" || d.ReportSize != 4096 {
		t.Fatalf("upload after replug: %+v", d)
	}
	if _, err := r.f.Driver(renamed, keyboard.Override{Node: "hidraw8"}); err == nil {
		t.Fatal("an explicit node that no longer exists must be an error")
	}
}

func mustClock(t *testing.T, d keyboard.Driver) keyboard.Description {
	t.Helper()
	p, err := d.PlanClock(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return p.Describe()
}

func TestTwoKeyboardsRequireAChoice(t *testing.T) {
	two := append(withUSB(realUnit(), "1-1", 0x800A), withUSB(realUnit(), "1-2", 0x800A)...)
	r := newRig(two)
	_, err := r.f.Interfaces()
	var multi *keyboard.MultipleError
	if !errors.As(err, &multi) || multi.N != 2 {
		t.Fatalf("%v", err)
	}
	all, err := r.f.WithDevice(2).Interfaces()
	if err != nil || len(all) != 3 || all[0].USBPath != "1-2" {
		t.Fatalf("device 2: %v %d", err, len(all))
	}
	if _, err := r.f.WithDevice(3).Interfaces(); err == nil {
		t.Fatal("device 3 of two must fail")
	}
}

func TestOtherProductsAndTheDongleAreIgnored(t *testing.T) {
	mixed := append(withUSB(realUnit(), "1-1", 0x800A), withUSB(realUnit(), "1-4", 0xFEFE)...) // 2.4G dongle
	mixed = append(mixed, withUSB(realUnit(), "1-5", 0x1234)...)                               // unrelated 0c45 device
	all, err := newRig(mixed).f.Interfaces()
	if err != nil || len(all) != 3 || all[0].USBPath != "1-1" {
		t.Fatalf("%v %d", err, len(all))
	}
	if _, err := newRig(withUSB(realUnit(), "1-4", 0xFEFE)).f.Interfaces(); !errors.Is(err, keyboard.ErrNoDevice) {
		t.Fatalf("only a dongle: %v", err)
	}
	if _, err := newRig(withUSB(realUnit(), "1-1", 0x8009)).f.Interfaces(); err != nil {
		t.Fatalf("the older product is supported: %v", err)
	}
}

func TestDataOverride(t *testing.T) {
	r := newRig(legacyUnit())
	d, err := r.f.Driver(legacyUnit(), keyboard.Override{DataNode: "hidraw1"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := d.PlanUpload(pic(100))
	if got := p.Describe().Node; got != "/dev/hidraw1" {
		t.Errorf("legacy honours the override: %s", got)
	}
	d, _ = r.f.Driver(legacyUnit(), keyboard.Override{DataNode: "hidraw99"})
	p, _ = d.PlanUpload(pic(100))
	if got := p.Describe().Node; got != "/dev/hidraw2" {
		t.Errorf("a vanished override is ignored: %s", got)
	}
	d, _ = r.f.Driver(realUnit(), keyboard.Override{DataNode: "hidraw8"})
	p, _ = d.PlanUpload(pic(100))
	if got := p.Describe().Node; got != "/dev/hidraw7" {
		t.Errorf("sonix has one data interface: %s", got)
	}
}

// Every driver must pass this.
func TestDriverConformance(t *testing.T) {
	for _, c := range []struct {
		name   string
		ifaces []hidraw.Info
		layout keyboard.Layout
		caps   keyboard.Capabilities
	}{
		{"sonix", realUnit(), keyboard.LayoutSonix, keyboard.Capabilities{Clock: true, Persistent: true}},
		{"legacy", legacyUnit(), keyboard.LayoutLegacy, keyboard.Capabilities{Clock: true, Builtin: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(c.ifaces)
			d := r.driver(t)
			if d.Layout() != c.layout || d.Capabilities() != c.caps {
				t.Errorf("%v %+v", d.Layout(), d.Capabilities())
			}
			clock, err := d.PlanClock(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			upload, err := d.PlanUpload(pic(100))
			if err != nil {
				t.Fatal(err)
			}
			builtin, err := d.PlanBuiltin(1)
			if c.caps.Builtin != (err == nil) {
				t.Errorf("Capabilities says Builtin=%v, PlanBuiltin returned %v", c.caps.Builtin, err)
			}
			if err != nil && !errors.Is(err, keyboard.ErrUnsupported) {
				t.Errorf("unsupported must wrap ErrUnsupported: %v", err)
			}
			for _, p := range []keyboard.Plan{clock, upload, builtin} {
				if p == nil {
					continue
				}
				if x := p.Describe(); x.Node == "" || x.Packets == 0 || x.ReportSize == 0 || x.First == "" {
					t.Errorf("incomplete description: %+v", x)
				}
			}
			if r.opens != 0 || len(r.writes)+len(r.feats) != 0 {
				t.Errorf("planning must do no device I/O: %d opens, %d writes", r.opens, len(r.writes)+len(r.feats))
			}
		})
	}
}

func hexPrefix(t *testing.T, s string) []byte {
	t.Helper()
	return desc(strings.Fields(strings.ReplaceAll(strings.Split(s, " ...")[0], " ", ""))[0])
}

// What a dry run shows is what is really sent.
func TestPlanDescriptionMatchesWhatRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Date(2026, 11, 1, 13, 40, 59, 0, time.Local)
		r := newRig(realUnit())
		p, _ := r.driver(t).PlanClock(now)
		if err := p.Run(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		d := p.Describe()
		if len(r.feats) != d.Packets || len(d.Steps) != len(r.feats) {
			t.Fatalf("sonix clock: described %d, sent %d", d.Packets, len(r.feats))
		}
		for i, step := range d.Steps {
			if !bytes.Equal(hexPrefix(t, step), r.feats[i]) {
				t.Errorf("sonix clock frame %d differs", i)
			}
		}
	})
	synctest.Test(t, func(t *testing.T) {
		r := newRig(realUnit())
		r.ackBig = true
		p, _ := r.driver(t).PlanUpload(pic(100))
		if err := p.Run(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		d := p.Describe()
		var real [][]byte
		for _, w := range r.writes {
			if len(w) == 4096 {
				real = append(real, w)
			}
		}
		real = real[1:] // the first is the alignment chunk
		if len(real) != d.Packets {
			t.Fatalf("sonix upload: described %d packets, sent %d", d.Packets, len(real))
		}
		if !bytes.HasPrefix(real[0], hexPrefix(t, d.First)) {
			t.Error("sonix upload: first packet differs from the description")
		}
	})
	synctest.Test(t, func(t *testing.T) {
		r := newRig(legacyUnit())
		p, _ := r.driver(t).PlanBuiltin(7)
		if err := p.Run(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		d := p.Describe()
		if len(r.writes) != d.Packets || !bytes.HasPrefix(r.writes[0], hexPrefix(t, d.First)) {
			t.Errorf("legacy builtin: %d writes, % x", len(r.writes), r.writes[0][:9])
		}
	})
}

func TestDevicesAreClosedAfterRunAndOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(realUnit())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p, _ := r.driver(t).PlanUpload(pic(100))
		if err := p.Run(ctx, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("%v", err)
		}
		if r.opens == 0 || r.opens != r.closes {
			t.Errorf("opened %d, closed %d", r.opens, r.closes)
		}
	})
}

// Regression: sonix uploads went through the legacy format, which caps delays at 510 ms.
func TestLongFrameDelaysReachTheSonixPayload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(realUnit())
		r.ackBig = true
		frames := append(pic(900), pic(900)...)
		p, _ := r.driver(t).PlanUpload(frames)
		if err := p.Run(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		var real [][]byte
		for _, w := range r.writes {
			if len(w) == 4096 {
				real = append(real, w)
			}
		}
		if got := real[1][1]; got != 180 { // 900 ms in 5 ms units
			t.Errorf("delay byte %d, want 180", got)
		}
	})
}

func TestLegacyPlanInfoAndClockAnswerNoReply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(legacyUnit())
		ld, ok := r.driver(t).(*legacy.Driver)
		if !ok {
			t.Fatal("expected the legacy driver")
		}
		plan, err := ld.PlanInfo()
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Run(context.Background(), nil); !errors.Is(err, keyboard.ErrNoReply) {
			t.Errorf("no answer: %v", err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		r := newRig(legacyUnit())
		b := make([]byte, 32)
		b[0], b[1] = 0x55, 0x10
		r.replies["/dev/hidraw1"] = [][]byte{append([]byte(nil), b...), append([]byte(nil), b...)}
		ld := r.driver(t).(*legacy.Driver)
		plan, _ := ld.PlanInfo()
		if err := plan.Run(context.Background(), nil); err != nil {
			t.Errorf("replies arrived: %v", err)
		}
	})
}

func TestReportSizeTooSmallIsAnErrorNotAPanic(t *testing.T) {
	r := newRig(realUnit())
	d, err := r.f.Driver(realUnit(), keyboard.Override{Node: "hidraw5"}) // the keyboard interface: 1-byte output
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.PlanClock(time.Now()); err == nil {
		t.Error("must refuse")
	}
	if _, err := d.PlanUpload(pic(100)); err == nil {
		t.Error("must refuse")
	}
	if _, err := d.PlanBuiltin(1); err == nil {
		t.Error("must refuse")
	}
}

func TestProbe(t *testing.T) {
	r := newRig(realUnit())
	p, err := legacy.NewProbe(realUnit(), r.f.Deps(), true)
	if err != nil {
		t.Fatal(err)
	}
	prev := p.Preview()
	if r.opens != 0 || len(r.writes) != 0 {
		t.Fatalf("a preview must do no I/O: %d %d", r.opens, len(r.writes))
	}
	if !prev.Dry || len(prev.Steps) != 2 || prev.Steps[0].Node != "/dev/hidraw8" || prev.Steps[1].Node != "/dev/hidraw7" || len(prev.Features) != 1 {
		t.Fatalf("%+v", prev)
	}
	synctest.Test(t, func(t *testing.T) {
		r := newRig(realUnit())
		b := []byte{0x55, 0x10}
		r.replies["/dev/hidraw8"] = [][]byte{b}
		p, _ := legacy.NewProbe(realUnit(), r.f.Deps(), false)
		res, err := p.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(r.writes) != 2 || len(r.writes[0]) != 64 || len(r.writes[1]) != 4096 {
			t.Fatalf("writes %d", len(r.writes))
		}
		if len(res.Steps[0].Replies) != 1 || len(res.Steps[1].Replies) != 0 {
			t.Fatalf("%+v", res.Steps)
		}
		if r.opens != r.closes {
			t.Errorf("opened %d, closed %d", r.opens, r.closes)
		}
	})
}
