package core

import (
	"encoding/hex"
	"errors"
	"image"
	"os"
	"strings"
	"testing"
	"time"

	"ak820/internal/hidraw"
	"ak820/internal/img"
)

func desc(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

// The four interfaces of a real 0c45:800a unit (descriptors captured from hidraw5-8).
func realUnit() []hidraw.Info {
	mk := func(node string, iface int, raw []byte) hidraw.Info {
		return hidraw.Info{Node: node, VID: 0x0C45, PID: 0x800A, Iface: iface, Raw: raw, D: hidraw.ParseDescriptor(raw)}
	}
	return []hidraw.Info{
		mk("/dev/hidraw5", 0, desc("0501 0906 a101 0507 19e0 29e7 1500 2501 7501 9508 8102 9501 7508 8101 9505 7501 0508 1901 2905 9102 9501 7503 9101 9506 7508 1500 26ff00 0507 1900 2a6500 8100 c0")),
		mk("/dev/hidraw7", 2, desc("0668ff0961a1010962150026ff00954075088102 096315 0026ff00 960010 7508 9102 c0")),
		mk("/dev/hidraw8", 3, desc("0613ff0901a1011500 26ff00 7508 9540 0902 8102 0903 9102 0a00ff b102 c0")),
	}
}

type fakeDev struct {
	node    string
	writes  *[][]byte
	feats   *[][]byte
	replies map[string][][]byte // node -> queued replies, released after any write
	armed   bool
}

func (f *fakeDev) Write(p []byte) error { *f.writes = append(*f.writes, p); f.armed = true; return nil }
func (f *fakeDev) ReadTimeout(time.Duration) ([]byte, error) {
	q := f.replies[f.node]
	if !f.armed || len(q) == 0 {
		return nil, nil
	}
	f.replies[f.node] = q[1:]
	return q[0], nil
}
func (f *fakeDev) GetFeature(id byte, n int) ([]byte, error) { return make([]byte, n+1), nil }
func (f *fakeDev) SetFeature(p []byte, n int) error {
	*f.feats = append(*f.feats, append([]byte(nil), p...))
	return nil
}
func (f *fakeDev) Close() error { return nil }

type rig struct {
	c      *Core
	opens  int
	writes [][]byte
	feats  [][]byte
	reply  map[string][][]byte
}

func newRig(t *testing.T) *rig {
	r := &rig{reply: map[string][][]byte{}}
	r.c = &Core{
		Enumerate: func() ([]hidraw.Info, error) { return realUnit(), nil },
		OpenDev: func(i hidraw.Info) (DevIO, error) {
			r.opens++
			return &fakeDev{node: i.Node, writes: &r.writes, feats: &r.feats, replies: r.reply}, nil
		},
		DataDir: t.TempDir(),
	}
	return r
}

func infoReply() []byte {
	b := make([]byte, 64)
	b[0], b[1] = 0x55, 0x10
	b[8+4], b[8+5], b[8+6], b[8+7] = 0x45, 0x0c, 0x0a, 0x80
	b[8+8] = 0x16
	b[8+8+1] = 0x01
	b[8+22] = 100
	return b
}

func TestRealUnitIsSonixLayout(t *testing.T) {
	r := newRig(t)
	all, _ := r.c.Devices()
	if Layout(all) != "sonix" {
		t.Fatalf("layout %q", Layout(all))
	}
	ctl, dat, _ := r.c.Resolve(all, "cmd", Target{})
	if ctl.Node != "/dev/hidraw8" || dat != 64 {
		t.Errorf("cmd -> %s %d", ctl.Node, dat)
	}
	d, n, _ := r.c.Resolve(all, "data", Target{})
	if d.Node != "/dev/hidraw7" || n != 4096 {
		t.Errorf("data -> %s %d", d.Node, n)
	}
}

func TestUnknownLayoutIsRefused(t *testing.T) {
	r := newRig(t)
	only := realUnit()[:2] // keyboard + FF68 only: no control interface
	if Layout(only) != "unknown" {
		t.Fatal("expected unknown")
	}
	if _, _, err := r.c.Resolve(only, "cmd", Target{}); err == nil {
		t.Fatal("Resolve must refuse to guess")
	}
}

func TestProbeDryTouchesNothing(t *testing.T) {
	r := newRig(t)
	res, err := r.c.Probe(true, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.opens != 0 || len(r.writes) != 0 {
		t.Fatalf("dry run opened %d / wrote %d", r.opens, len(r.writes))
	}
	if len(res.Steps) != 2 || res.Steps[0].Node != "/dev/hidraw8" || res.Steps[1].Node != "/dev/hidraw7" {
		t.Fatalf("steps %+v", res.Steps)
	}
	if len(res.Features) != 1 || res.Features[0].Node != "/dev/hidraw8" {
		t.Fatalf("features %+v", res.Features)
	}
}

func TestProbeRealSendsOneFramePerCandidateAndCollectsReplies(t *testing.T) {
	r := newRig(t)
	r.reply["/dev/hidraw8"] = [][]byte{infoReply()}
	res, err := r.c.Probe(false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.writes) != 2 || len(r.writes[0]) != 64 || len(r.writes[1]) != 4096 {
		t.Fatalf("writes: %d", len(r.writes))
	}
	if len(res.Steps[0].Replies) != 1 || len(res.Steps[1].Replies) != 0 {
		t.Fatalf("replies %+v", res.Steps)
	}
}

func TestInfoDryOpensNothingAndRealSavesTarget(t *testing.T) {
	r := newRig(t)
	if _, err := r.c.Info(Target{Node: "hidraw8"}, true); err != nil || r.opens != 0 {
		t.Fatalf("dry: err %v opens %d", err, r.opens)
	}
	if r.c.State().Cmd.Node != "" {
		t.Fatal("dry run must not save a target")
	}
	r.reply["/dev/hidraw8"] = [][]byte{infoReply()}
	res, err := r.c.Info(Target{Node: "hidraw8"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Info.Firmware != "1.16" || res.Info.PID != 0x800a {
		t.Fatalf("%+v", res.Info)
	}
	if got := r.c.State().Cmd; got.Node != "/dev/hidraw8" {
		t.Fatalf("saved %+v", got)
	}
}

func TestInfoNoReplyDoesNotSave(t *testing.T) {
	r := newRig(t)
	if _, err := r.c.Info(Target{Node: "hidraw8"}, false); err == nil {
		t.Fatal("expected timeout")
	}
	if r.c.State().Cmd.Node != "" {
		t.Fatal("must not save an unconfirmed target")
	}
}

func TestUploadDryNeedsNoDeviceAndUsesSavedTarget(t *testing.T) {
	r := newRig(t)
	if _, err := r.c.Upload(Target{}, make([]byte, 100), true, nil); err == nil {
		t.Fatal("garbage payload must be refused")
	}
	pl := validPayload()
	res, err := r.c.Upload(Target{}, pl, true, nil)
	if err != nil || r.opens != 0 || len(r.writes)+len(r.feats) != 0 {
		t.Fatalf("%+v %v opens %d", res, err, r.opens)
	}
	if res.Node != "/dev/hidraw7" || res.Len != 4096 || res.Packets != 9 || !res.Flash {
		t.Fatalf("%+v", res)
	}
}

func validPayload() []byte {
	im := image.NewRGBA(image.Rect(0, 0, img.W, img.H))
	return img.BuildPayload([]img.Frame{{Img: im, DelayMs: 100}})
}

func TestClockDryDoesNothingAndRealConfirmsTarget(t *testing.T) {
	r := newRig(t)
	now := time.Date(2026, 11, 1, 13, 40, 59, 0, time.Local)
	res, err := r.c.SetTime(Target{}, now, true)
	if err != nil || r.opens != 0 || len(r.feats) != 0 || len(res.Steps) != 4 {
		t.Fatalf("dry: %v opens %d feats %d steps %d", err, r.opens, len(r.feats), len(res.Steps))
	}
	if r.c.State().Cmd.Node != "" {
		t.Fatal("dry run must not confirm anything")
	}
	if _, err := r.c.SetTime(Target{}, now, false); err != nil {
		t.Fatal(err)
	}
	if len(r.feats) != 4 {
		t.Fatalf("expected 4 control frames, got %d", len(r.feats))
	}
	if got := r.c.State().Cmd; got.Node != "/dev/hidraw8" {
		t.Fatalf("saved %+v", got)
	}
	all, _ := r.c.Devices()
	if !r.c.Ready(all) {
		t.Error("should be ready after a successful clock sync")
	}
}

func TestReadyRequiresTheSavedNodeToStillExist(t *testing.T) {
	r := newRig(t)
	r.c.MarkConnected("/dev/hidraw42")
	all, _ := r.c.Devices()
	if r.c.Ready(all) {
		t.Fatal("stale node must not count as ready")
	}
}

func TestBuiltinUnsupportedOnSonix(t *testing.T) {
	r := newRig(t)
	if _, err := r.c.Builtin(Target{}, 1, true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestDescribeIsPlainLanguage(t *testing.T) {
	r := newRig(t)
	if f := r.c.Describe(nil, ErrNoDevice); f.Level != "bad" || !strings.Contains(f.Message, "USB") {
		t.Errorf("%+v", f)
	}
	all, _ := r.c.Devices()
	if f := r.c.Describe(all, nil); f.Level == "bad" {
		// nodes in the rig do not exist on disk, so Accessible() is false -> permission message
		if !strings.Contains(f.Title, "not allowed") {
			t.Errorf("%+v", f)
		}
	}
	if f := r.c.Describe(realUnit()[:2], nil); f.Level == "ok" {
		t.Errorf("unsupported layout must not look connected: %+v", f)
	}
}

// renumber returns a copy of the unit with hidraw nodes renamed, as happens on replug.
func renumber(all []hidraw.Info, m map[string]string) []hidraw.Info {
	out := append([]hidraw.Info(nil), all...)
	for i := range out {
		if n, ok := m[out[i].Node]; ok {
			out[i].Node = n
		}
	}
	return out
}

// Regression: the keyboard came back as hidraw3-6 after a replug and the tool
// failed with "/dev/hidraw8 is not a 0c45 hidraw node". Nothing about nodes is
// stored any more, so every run resolves from the current enumeration.
func TestResolveSurvivesRenumberingWithNoSavedState(t *testing.T) {
	r := newRig(t)
	all, _ := r.c.Devices()
	if i, _, err := r.c.Resolve(all, "cmd", Target{}); err != nil || i.Node != "/dev/hidraw8" {
		t.Fatalf("before: %v %s", err, i.Node)
	}
	m := map[string]string{"/dev/hidraw5": "/dev/hidraw3", "/dev/hidraw7": "/dev/hidraw5", "/dev/hidraw8": "/dev/hidraw6"}
	r.c.Enumerate = func() ([]hidraw.Info, error) { return renumber(realUnit(), m), nil }
	all, _ = r.c.Devices()
	i, n, err := r.c.Resolve(all, "cmd", Target{})
	if err != nil || i.Node != "/dev/hidraw6" || n != 64 {
		t.Fatalf("cmd after replug: %v %s %d", err, i.Node, n)
	}
	i, n, err = r.c.Resolve(all, "data", Target{})
	if err != nil || i.Node != "/dev/hidraw5" || n != 4096 {
		t.Fatalf("data after replug: %v %s %d", err, i.Node, n)
	}
	if _, _, err := r.c.Resolve(all, "cmd", Target{Node: "hidraw8"}); err == nil {
		t.Fatal("an explicit node that no longer exists must still be an error")
	}
}

func TestNothingAboutNodesIsEverWrittenToDisk(t *testing.T) {
	r := newRig(t)
	if _, err := r.c.SetTime(Target{}, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	r.c.SetDataNode("/dev/hidraw7")
	entries, _ := os.ReadDir(r.c.DataDir)
	if len(entries) != 0 {
		t.Fatalf("DataDir should stay empty, found %v", entries)
	}
}

func TestSessionIsNotSharedBetweenProcesses(t *testing.T) {
	a, b := newRig(t), newRig(t)
	if _, err := a.c.SetTime(Target{}, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	all, _ := a.c.Devices()
	if !a.c.Ready(all) || b.c.Ready(all) {
		t.Fatal("a fresh Core (a new run) must start disconnected")
	}
}

func TestReadyGoesFalseWhenTheKeyboardIsReplugged(t *testing.T) {
	r := newRig(t)
	if _, err := r.c.SetTime(Target{}, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	all, _ := r.c.Devices()
	if !r.c.Ready(all) {
		t.Fatal("should be ready after connecting")
	}
	m := map[string]string{"/dev/hidraw8": "/dev/hidraw6"}
	if r.c.Ready(renumber(all, m)) {
		t.Fatal("the connected node vanished: must require connecting again")
	}
}

func TestInfoOnSonixExplainsWhatToRunInstead(t *testing.T) {
	r := newRig(t)
	_, err := r.c.Info(Target{}, false)
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "time --yes") || r.opens != 0 {
		t.Fatalf("%v opens %d", err, r.opens)
	}
}

func withDevice(all []hidraw.Info, key string, pid uint16) []hidraw.Info {
	out := append([]hidraw.Info(nil), all...)
	for i := range out {
		out[i].Device, out[i].PID = key, pid
		out[i].Node += "-" + key
	}
	return out
}

func TestTwoKeyboardsRequireAChoice(t *testing.T) {
	r := newRig(t)
	two := append(withDevice(realUnit(), "1-1", 0x800A), withDevice(realUnit(), "1-2", 0x800A)...)
	r.c.Enumerate = func() ([]hidraw.Info, error) { return two, nil }
	_, err := r.c.Devices()
	var em ErrMultiple
	if !errors.As(err, &em) || em.N != 2 || !strings.Contains(err.Error(), "--device") {
		t.Fatalf("%v", err)
	}
	r.c.Device = 2
	all, err := r.c.Devices()
	if err != nil || len(all) != 3 || all[0].Device != "1-2" {
		t.Fatalf("--device 2: %v %d %q", err, len(all), all[0].Device)
	}
	r.c.Device = 3
	if _, err := r.c.Devices(); err == nil {
		t.Fatal("--device 3 with two keyboards must fail")
	}
}

func TestOtherProductsAndTheDongleAreIgnored(t *testing.T) {
	r := newRig(t)
	mixed := append(withDevice(realUnit(), "1-1", 0x800A), withDevice(realUnit(), "1-4", 0xFEFE)...) // 2.4G dongle
	mixed = append(mixed, withDevice(realUnit(), "1-5", 0x1234)...)                                  // unrelated 0c45 device
	r.c.Enumerate = func() ([]hidraw.Info, error) { return mixed, nil }
	all, err := r.c.Devices()
	if err != nil || len(all) != 3 || all[0].Device != "1-1" {
		t.Fatalf("%v %d", err, len(all))
	}
	r.c.Enumerate = func() ([]hidraw.Info, error) { return withDevice(realUnit(), "1-4", 0xFEFE), nil }
	if _, err := r.c.Devices(); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("only a dongle: want ErrNoDevice, got %v", err)
	}
}

func TestOlderAK820ProductIsSupported(t *testing.T) {
	r := newRig(t)
	r.c.Enumerate = func() ([]hidraw.Info, error) { return withDevice(realUnit(), "1-1", 0x8009), nil }
	if _, err := r.c.Devices(); err != nil {
		t.Fatal(err)
	}
}
