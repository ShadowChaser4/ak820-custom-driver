package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/core"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/setup"
)

// vdesc builds a minimal vendor HID report descriptor: usage page, and
// optional input / output / feature fields of n bytes each.
func vdesc(page uint16, in, out, feat int) []byte {
	b := []byte{0x06, byte(page), byte(page >> 8), 0x09, 0x01, 0xa1, 0x01, 0x15, 0x00, 0x26, 0xff, 0x00, 0x75, 0x08}
	field := func(n int, tag, flags byte) {
		if n == 0 {
			return
		}
		if n < 256 {
			b = append(b, 0x95, byte(n))
		} else {
			b = append(b, 0x96, byte(n), byte(n>>8))
		}
		b = append(b, tag, flags)
	}
	field(in, 0x81, 0x02)
	field(out, 0x91, 0x02)
	field(feat, 0xb1, 0x02)
	return append(b, 0xc0)
}

type unit struct{ ifs []hidraw.Info }

// sonixUnit is the real 0c45:800a layout (FF68 4096 B out + FF13 64 B feature).
// Nodes are real temp files so Accessible() is true.
func sonixUnit(t *testing.T, usb string, pid uint16) []hidraw.Info {
	t.Helper()
	mk := func(name string, iface int, raw []byte) hidraw.Info {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, nil, 0o666); err != nil {
			t.Fatal(err)
		}
		return hidraw.Info{Node: p, Device: usb, VID: 0x0c45, PID: pid, Iface: iface, Name: "SONiX AK820", Raw: raw, D: hidraw.ParseDescriptor(raw)}
	}
	return []hidraw.Info{mk("hidraw5", 2, vdesc(0xFF68, 64, 4096, 0)), mk("hidraw6", 3, vdesc(0xFF13, 64, 64, 64))}
}

type fakeDev struct{ w *counters }
type counters struct {
	opens, writes, feats int
	firstFeat            []byte
}

func (f fakeDev) Write([]byte) error                        { f.w.writes++; return nil }
func (f fakeDev) ReadTimeout(time.Duration) ([]byte, error) { return nil, nil }
func (f fakeDev) GetFeature(byte, int) ([]byte, error)      { return make([]byte, 65), nil }
func (f fakeDev) SetFeature(p []byte, n int) error {
	if f.w.feats == 0 {
		f.w.firstFeat = append([]byte(nil), p...)
	}
	f.w.feats++
	return nil
}
func (f fakeDev) Close() error { return nil }

type rig struct {
	a        *app
	out, err *bytes.Buffer
	n        *counters
	uiAddr   string
	uiNoOpen bool
	uiCalled bool
}

func newRig(t *testing.T, ifs []hidraw.Info) *rig {
	t.Helper()
	r := &rig{out: &bytes.Buffer{}, err: &bytes.Buffer{}, n: &counters{}}
	c := &core.Core{
		Enumerate: func() ([]hidraw.Info, error) { return ifs, nil },
		OpenDev:   func(hidraw.Info) (core.DevIO, error) { r.n.opens++; return fakeDev{r.n}, nil },
		DataDir:   t.TempDir(),
	}
	r.a = &app{c: c, out: r.out, errw: r.err, ruleDirs: []string{t.TempDir()}}
	r.a.ui = func(addr string, noOpen bool) error {
		r.uiCalled, r.uiAddr, r.uiNoOpen = true, addr, noOpen
		return nil
	}
	return r
}

func testPNG(t *testing.T) string {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for i := 0; i < len(im.Pix); i += 4 {
		im.Pix[i], im.Pix[i+3] = 200, 255
	}
	p := filepath.Join(t.TempDir(), "pic.png")
	f, _ := os.Create(p)
	_ = png.Encode(f, im)
	f.Close()
	_ = color.Black
	return p
}

// The safety promise: nothing is sent to the keyboard without --yes.
func TestNoCommandWritesWithoutYes(t *testing.T) {
	pic := testPNG(t)
	cases := []struct {
		args []string
		code int
	}{
		{[]string{"time"}, 0},
		{[]string{"upload", pic}, 0},
		{[]string{"upload", "--fit", "contain", pic}, 0},
		{[]string{"probe"}, 0},
		{[]string{"builtin", "3"}, 1}, // sonix has none; must explain, not send
		{[]string{"info"}, 1},         // sonix has none; must explain, not send
	}
	for _, c := range cases {
		r := newRig(t, sonixUnit(t, "1-1", 0x800A))
		if code := r.a.run(c.args); code != c.code {
			t.Errorf("%v: exit %d, want %d\nout: %s\nerr: %s", c.args, code, c.code, r.out, r.err)
		}
		if r.n.opens != 0 || r.n.writes != 0 || r.n.feats != 0 {
			t.Errorf("%v touched the keyboard without --yes: opens %d writes %d feats %d", c.args, r.n.opens, r.n.writes, r.n.feats)
		}
	}
}

func TestDryRunTellsYouNothingWasSent(t *testing.T) {
	for _, args := range [][]string{{"time"}, {"upload", testPNG(t)}} {
		r := newRig(t, sonixUnit(t, "1-1", 0x800A))
		r.a.run(args)
		if !strings.Contains(r.out.String(), "nothing was sent") || !strings.Contains(r.out.String(), "--yes") {
			t.Errorf("%v: %q", args, r.out)
		}
	}
}

func TestTimeYesSendsTheFourClockFrames(t *testing.T) {
	r := newRig(t, sonixUnit(t, "1-1", 0x800A))
	if code := r.a.run([]string{"time", "--yes"}); code != 0 {
		t.Fatalf("exit %d: %s", code, r.err)
	}
	if r.n.feats != 4 || r.n.firstFeat[0] != 0x04 || r.n.firstFeat[1] != 0x18 || !strings.Contains(r.out.String(), "clock set") {
		t.Fatalf("feats %d first % x out %q", r.n.feats, r.n.firstFeat[:3], r.out)
	}
}

func TestProbeYesWritesOneFramePerInterfaceAndLogs(t *testing.T) {
	r := newRig(t, sonixUnit(t, "1-1", 0x800A))
	logf := filepath.Join(t.TempDir(), "probe.md")
	if code := r.a.run([]string{"probe", "--yes", "--log", logf}); code != 0 {
		t.Fatalf("exit %d: %s", code, r.err)
	}
	if r.n.writes != 2 {
		t.Errorf("writes %d", r.n.writes)
	}
	if b, _ := os.ReadFile(logf); !strings.Contains(string(b), "probe") {
		t.Errorf("log: %q", b)
	}
}

func TestFriendlyErrorsSayWhatToDoNext(t *testing.T) {
	t.Run("no keyboard", func(t *testing.T) {
		r := newRig(t, nil)
		if r.a.run([]string{"list"}) != 1 || !strings.Contains(r.err.String(), "Plug it in") || !strings.Contains(r.err.String(), "ak820 setup") {
			t.Errorf("%q", r.err)
		}
	})
	t.Run("permission", func(t *testing.T) {
		r := newRig(t, sonixUnit(t, "1-1", 0x800A))
		r.a.c.OpenDev = func(i hidraw.Info) (core.DevIO, error) {
			return nil, fmt.Errorf("%w on %s", hidraw.ErrPermission, i.Node)
		}
		if r.a.run([]string{"time", "--yes"}) != 1 || !strings.Contains(r.err.String(), "ak820 setup") || strings.Contains(r.err.String(), "hidraw") {
			t.Errorf("%q", r.err)
		}
	})
	t.Run("unknown layout", func(t *testing.T) {
		unknown := []hidraw.Info{{Node: "/dev/x", VID: 0x0c45, PID: 0x800a, Raw: vdesc(0xFF00, 8, 8, 0), D: hidraw.ParseDescriptor(vdesc(0xFF00, 8, 8, 0))}}
		r := newRig(t, unknown)
		if r.a.run([]string{"time", "--yes"}) != 1 || !strings.Contains(r.err.String(), "ak820 report --issue") || r.n.opens != 0 {
			t.Errorf("%q opens %d", r.err, r.n.opens)
		}
	})
	t.Run("info on sonix", func(t *testing.T) {
		r := newRig(t, sonixUnit(t, "1-1", 0x800A))
		if r.a.run([]string{"info", "--yes"}) != 1 || !strings.Contains(r.err.String(), "ak820 time --yes") || r.n.opens != 0 {
			t.Errorf("%q", r.err)
		}
	})
	t.Run("no reply", func(t *testing.T) {
		if m := friendlyErr(errors.New("cmd 0x10: no reply (4 tries)")); !strings.Contains(m, "switch on the back") {
			t.Error(m)
		}
	})
}

func TestSeveralKeyboardsNeedAChoice(t *testing.T) {
	two := append(sonixUnit(t, "1-1", 0x800A), sonixUnit(t, "1-2", 0x800A)...)
	r := newRig(t, two)
	if r.a.run([]string{"list"}) != 1 || !strings.Contains(r.err.String(), "--device") {
		t.Fatalf("%q", r.err)
	}
	r = newRig(t, two)
	if code := r.a.run([]string{"--device", "2", "list"}); code != 0 || strings.Count(r.out.String(), "\n") != 2 || !strings.Contains(r.out.String(), two[2].Node) {
		t.Fatalf("exit %d out %q", code, r.out)
	}
	t.Setenv("AK820_DEVICE", "1")
	r = newRig(t, two)
	if code := r.a.run([]string{"list"}); code != 0 || !strings.Contains(r.out.String(), two[0].Node) {
		t.Fatalf("env: exit %d %q", code, r.out)
	}
}

func TestBadDeviceFlag(t *testing.T) {
	r := newRig(t, sonixUnit(t, "1-1", 0x800A))
	if r.a.run([]string{"--device", "two", "list"}) != 2 {
		t.Error("want exit 2")
	}
}

func TestNoArgumentsStartsTheUI(t *testing.T) {
	r := newRig(t, nil)
	if code := r.a.run(nil); code != 0 || !r.uiCalled || r.uiAddr != "127.0.0.1:0" || r.uiNoOpen {
		t.Fatalf("exit %d called %v addr %q noOpen %v", code, r.uiCalled, r.uiAddr, r.uiNoOpen)
	}
	r = newRig(t, nil)
	r.a.run([]string{"ui", "--no-open", "--addr", "127.0.0.1:9"})
	if !r.uiNoOpen || r.uiAddr != "127.0.0.1:9" {
		t.Errorf("flags not passed: %q %v", r.uiAddr, r.uiNoOpen)
	}
}

func TestHelpVersionAndUnknownCommand(t *testing.T) {
	r := newRig(t, nil)
	if r.a.run([]string{"help"}) != 0 || !strings.Contains(r.out.String(), "ak820 setup") {
		t.Error("help")
	}
	r = newRig(t, nil)
	if r.a.run([]string{"version"}) != 0 || !strings.HasPrefix(r.out.String(), "ak820 ") {
		t.Errorf("version %q", r.out)
	}
	r = newRig(t, nil)
	if r.a.run([]string{"frobnicate"}) != 2 || !strings.Contains(r.err.String(), "unknown command") {
		t.Errorf("unknown: %q", r.err)
	}
}

func TestReportIssueIsPasteReady(t *testing.T) {
	r := newRig(t, sonixUnit(t, "1-1", 0x800A))
	if code := r.a.run([]string{"report", "--issue"}); code != 0 {
		t.Fatal(r.err)
	}
	for _, want := range []string{"**ak820**", "Layout detected:** sonix", "| node |", "Descriptors:", "06 68 ff"} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, r.out)
		}
	}
}

func TestSetupChecklist(t *testing.T) {
	ruleDir := t.TempDir()
	r := newRig(t, sonixUnit(t, "1-1", 0x800A))
	r.a.ruleDirs = []string{ruleDir}
	if code := r.a.run([]string{"setup"}); code != 1 || !strings.Contains(r.out.String(), "✗ Permission rule installed") || !strings.Contains(r.out.String(), setup.InstallCommand("ak820")) {
		t.Fatalf("no rule: exit %d\n%s", code, r.out)
	}
	if strings.Contains(r.err.String(), "error") {
		t.Errorf("setup should not print an extra error: %q", r.err)
	}
	os.WriteFile(filepath.Join(ruleDir, "99-mine.rules"), []byte(`ATTRS{idVendor}=="0c45", ATTRS{idProduct}=="800a", TAG+="uaccess"`), 0o644)
	r = newRig(t, sonixUnit(t, "1-1", 0x800A))
	r.a.ruleDirs = []string{ruleDir}
	if code := r.a.run([]string{"setup"}); code != 0 || !strings.Contains(r.out.String(), "All good") || strings.Contains(r.out.String(), "✗") {
		t.Fatalf("with rule: exit %d\n%s", code, r.out)
	}
	r = newRig(t, nil)
	r.a.ruleDirs = []string{ruleDir}
	if code := r.a.run([]string{"setup"}); code != 1 || !strings.Contains(r.out.String(), "✗ Keyboard found") || !strings.Contains(r.out.String(), "USB-C") {
		t.Fatalf("no keyboard: exit %d\n%s", code, r.out)
	}
}

func TestSetupPrintRuleMatchesTheShippedFile(t *testing.T) {
	r := newRig(t, nil)
	if r.a.run([]string{"setup", "--print-rule"}) != 0 || r.out.String() != setup.Rule || !strings.Contains(r.out.String(), "800a") {
		t.Error("print-rule output differs from the embedded rule")
	}
}

func TestOlderProductAndDongleHandling(t *testing.T) {
	r := newRig(t, sonixUnit(t, "1-1", 0x8009))
	if r.a.run([]string{"list"}) != 0 {
		t.Errorf("0x8009 must be supported: %s", r.err)
	}
	r = newRig(t, sonixUnit(t, "1-4", 0xFEFE))
	if r.a.run([]string{"list"}) != 1 || !strings.Contains(r.err.String(), "No AK820 keyboard found") {
		t.Errorf("a lone 2.4G dongle is not a keyboard we can drive: %q", r.err)
	}
}

func TestHintsNameTheProgramTheUserActuallyRan(t *testing.T) {
	// "ak820: command not found": a binary run as ./ak820 must not tell people to type plain ak820.
	r := newRig(t, sonixUnit(t, "1-1", 0x800A))
	r.a.prog = "./ak820"
	r.a.run([]string{"info", "--yes"})
	if !strings.Contains(r.err.String(), "`./ak820 time --yes`") || strings.Contains(r.err.String(), "`ak820 ") {
		t.Errorf("%q", r.err)
	}
	r = newRig(t, nil)
	r.a.prog = "./ak820"
	r.a.run([]string{"list"})
	if !strings.Contains(r.err.String(), "`./ak820 setup`") {
		t.Errorf("%q", r.err)
	}
	r = newRig(t, sonixUnit(t, "1-1", 0x800A))
	r.a.prog = "./ak820"
	r.a.ruleDirs = []string{t.TempDir()}
	r.a.run([]string{"setup"})
	if !strings.Contains(r.out.String(), "./ak820 setup --print-rule") {
		t.Errorf("%s", r.out)
	}
}
