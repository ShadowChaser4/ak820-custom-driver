package sonix

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/img"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name>.golden (hex, 16 bytes per line).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < len(got); i += 16 {
		fmt.Fprintf(&b, "%04x  % x\n", i, got[i:min(i+16, len(got))])
	}
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/sonix -update)", err)
	}
	if string(want) != b.String() {
		t.Errorf("%s changed.\n--- want\n%s--- got\n%s", name, want, b.String())
	}
}

func TestGoldenFrames(t *testing.T) {
	now := time.Date(2026, 11, 1, 13, 40, 59, 0, time.Local)
	var all []byte
	for _, f := range ClockFrames(now) {
		all = append(all, f[:16]...) // the first 16 bytes carry every field; the rest is zero
		if !bytes.Equal(f[16:62], make([]byte, 46)) {
			t.Error("bytes 16..61 of a control frame must be zero")
		}
	}
	golden(t, "clock-frames", all)
	golden(t, "image-cfg-3", imageCfg(3)[:16])
	golden(t, "image-cfg-17", imageCfg(17)[:16])
	golden(t, "start-save", append(append([]byte(nil), Cmd(0x18, 0, false)[:16]...), Cmd(0x02, 0, false)[:16]...))
}

func TestGoldenPayloadHeaderAndFirstPixels(t *testing.T) {
	red, blue := solid(color.RGBA{255, 0, 0, 255}), solid(color.RGBA{0, 0, 255, 255})
	p, chunks, err := BuildPayload([]img.Frame{{Img: red, DelayMs: 100}, {Img: blue, DelayMs: 300}})
	if err != nil || chunks != 17 {
		t.Fatalf("%v %d", err, chunks)
	}
	sample := append([]byte(nil), p[:32]...)                      // count + delays + zero fill
	sample = append(sample, p[256:272]...)                        // first red pixels
	sample = append(sample, p[256+128*128*2:256+128*128*2+16]...) // first blue pixels
	golden(t, "payload-sample", sample)
	if tail := p[256+2*128*128*2:]; !bytes.Equal(tail, make([]byte, len(tail))) {
		t.Error("padding to whole chunks must be zero")
	}
}

// ---- a stateful fake of the keyboard firmware (see FINDINGS.md in ak820pro-linux) ----

// fw models what matters: the 64-packet ring counter that is NOT reset by
// IMAGE_CFG, an ACK each time the ring wraps, and a 2 s watchdog (virtual clock).
type fw struct {
	idx       int           // ring position, 0..63
	now       time.Duration // virtual time
	lastPkt   time.Duration
	acks      [][]byte
	ackCount  int
	watchdog  bool
	setFeats  [][]byte
	getFails  int // GET_FEATURE fails this many times, then works
	dropAck   int // 1-based index of the ACK to swallow (0 = none)
	badMagic  int // 1-based index of the ACK to corrupt
	writeFail int // 1-based index of the 4096-byte write that fails
	bigWrites int
	misalign  int // 4096-byte writes that started away from ring position 0
	big       []int
	inData    bool
}

func newFW(startIdx int) *fw { return &fw{idx: startIdx} }

func (f *fw) install(t *testing.T) {
	old := sleep
	sleep = func(d time.Duration) { f.now += d }
	t.Cleanup(func() { sleep = old })
}

func (f *fw) SetFeature(p []byte) error {
	f.setFeats = append(f.setFeats, append([]byte(nil), p...))
	switch p[1] {
	case 0x72:
		f.inData, f.lastPkt = true, f.now // starts the watchdog; does NOT reset f.idx
	case 0x02, 0x18:
		f.inData = false
	}
	return nil
}

func (f *fw) GetFeature() ([]byte, error) {
	if f.getFails > 0 {
		f.getFails--
		return nil, errors.New("stalled")
	}
	return make([]byte, 64), nil
}

func (f *fw) WriteData(p []byte) error {
	if len(p) == ChunkSize {
		f.bigWrites++
		f.big = append(f.big, f.idx)
		if f.idx != 0 {
			f.misalign++
		}
		if f.writeFail == f.bigWrites {
			return errors.New("usb write failed")
		}
	}
	if f.inData && f.now-f.lastPkt > 2*time.Second {
		f.watchdog = true
	}
	for k := 0; k < len(p)/64; k++ {
		f.idx++
		if f.idx == 64 {
			f.idx = 0
			f.ackCount++
			switch {
			case f.ackCount == f.dropAck:
			case f.ackCount == f.badMagic:
				f.acks = append(f.acks, []byte{0xDE, 0xAD, 0xBE, 0xEF})
			default:
				f.acks = append(f.acks, append([]byte{0x01, 0x5A, 0x02}, make([]byte, 61)...))
			}
		}
	}
	f.lastPkt = f.now
	return nil
}

func (f *fw) ReadAck(t time.Duration) ([]byte, error) {
	if len(f.acks) > 0 {
		a := f.acks[0]
		f.acks = f.acks[1:]
		return a, nil
	}
	f.now += t // we waited the full timeout
	return nil, nil
}

func onePic() ([]byte, int) {
	p, n, _ := BuildPayload([]img.Frame{{Img: solid(color.RGBA{0, 200, 0, 255}), DelayMs: 100}})
	return p, n
}

// The firmware bug the alignment step exists for: whatever ring position the
// previous session left behind, every real chunk must start at position 0.
func TestUploadAlignsFromEveryRingPosition(t *testing.T) {
	pl, n := onePic()
	for start := 0; start < 64; start++ {
		f := newFW(start)
		f.install(t)
		if err := (&Port{C: f, D: f}).Upload(pl, n, nil); err != nil {
			t.Fatalf("start %d: %v", start, err)
		}
		if f.misalign != 0 {
			t.Fatalf("start %d: %d chunk(s) started off-position (frames would be split in half): %v", start, f.misalign, f.big)
		}
		if f.watchdog {
			t.Fatalf("start %d: the 2 s watchdog fired", start)
		}
		if f.bigWrites != n+1 { // one alignment chunk + the real ones
			t.Fatalf("start %d: %d chunk writes, want %d", start, f.bigWrites, n+1)
		}
		if last := f.setFeats[len(f.setFeats)-1]; last[1] != 0x02 {
			t.Fatalf("start %d: transfer must end with SAVE, ended with % x", start, last[:3])
		}
		if len(f.acks) != 0 {
			t.Fatalf("start %d: %d unread ACKs left behind would corrupt the next upload", start, len(f.acks))
		}
	}
}

func realImageCfgs(f *fw, chunks int) int {
	n := 0
	for _, p := range f.setFeats {
		if p[1] == 0x72 && int(p[8])|int(p[9])<<8 == chunks {
			n++
		}
	}
	return n
}

func savesAfterRealTransfer(f *fw, chunks int) bool {
	seen := false
	for _, p := range f.setFeats {
		if p[1] == 0x72 && int(p[8])|int(p[9])<<8 == chunks {
			seen = true
		}
		if seen && p[1] == 0x02 {
			return true
		}
	}
	return false
}

func TestFailedTransfersNeverCommit(t *testing.T) {
	pl, n := onePic()
	// The alignment pass consumes 3 ACKs (coarse, fine, third chunk); real chunk k is ACK 3+k.
	cases := []struct {
		name    string
		set     func(f *fw)
		wantErr string
	}{
		{"a chunk acknowledgement never arrives", func(f *fw) { f.dropAck = 3 + 5 }, "confirmed only"},
		{"the first real chunk is not acknowledged", func(f *fw) { f.dropAck = 4 }, "confirmed only"},
		{"an acknowledgement has the wrong magic", func(f *fw) { f.badMagic = 3 + 2 }, "unexpected reply"},
		{"the USB write fails mid-transfer", func(f *fw) { f.writeFail = 1 + 4 }, "usb write failed"},
	}
	for _, c := range cases {
		f := newFW(11)
		f.install(t)
		c.set(f)
		err := (&Port{C: f, D: f}).Upload(pl, n, nil)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.wantErr)
		}
		if savesAfterRealTransfer(f, n) {
			t.Errorf("%s: SAVE was sent after a transfer that did not complete", c.name)
		}
	}
}

func TestAlignmentThatNeverLocksGivesUpWithAHumanMessage(t *testing.T) {
	pl, n := onePic()
	f := newFW(0)
	f.install(t)
	f.dropAck = 1 // never see the coarse ACK on either attempt... drop every ACK
	g := &noAcks{fw: f}
	err := (&Port{C: g, D: g}).Upload(pl, n, nil)
	if err == nil || realImageCfgs(f, n) != 0 {
		t.Fatalf("err %v, real IMAGE_CFG sent %d times", err, realImageCfgs(f, n))
	}
	if !strings.Contains(err.Error(), "replug the cable") && !strings.Contains(err.Error(), "confirmed") && !strings.Contains(err.Error(), "line up") {
		t.Errorf("message should tell the user what to do: %v", err)
	}
}

// noAcks swallows every ACK.
type noAcks struct{ *fw }

func (n *noAcks) WriteData(p []byte) error { err := n.fw.WriteData(p); n.fw.acks = nil; return err }

func TestControlChannelHandshake(t *testing.T) {
	t.Run("one stall is tolerated", func(t *testing.T) {
		f := newFW(0)
		f.install(t)
		f.getFails = 1
		if err := (&Port{C: f, D: f}).SyncClock(time.Now()); err != nil {
			t.Fatalf("a single failed read must not abort: %v", err)
		}
	})
	t.Run("two stalls in a row mean the channel is dead", func(t *testing.T) {
		f := newFW(0)
		f.install(t)
		f.getFails = 5
		err := (&Port{C: f, D: f}).SyncClock(time.Now())
		if err == nil || !strings.Contains(err.Error(), "not answering") {
			t.Fatalf("%v", err)
		}
	})
	t.Run("stalls that are not consecutive never add up", func(t *testing.T) {
		f := newFW(0)
		f.install(t)
		p := &Port{C: &flaky{fw: f}, D: f}
		if err := p.SyncClock(time.Now()); err != nil {
			t.Fatal(err)
		}
	})
}

// flaky fails every other GET_FEATURE.
type flaky struct {
	*fw
	n int
}

func (x *flaky) GetFeature() ([]byte, error) {
	x.n++
	if x.n%2 == 1 {
		return nil, errors.New("blip")
	}
	return make([]byte, 64), nil
}

func TestSetFeatureErrorsPropagate(t *testing.T) {
	f := newFW(0)
	f.install(t)
	err := (&Port{C: failSet{f}, D: f}).SyncClock(time.Now())
	if err == nil || !strings.Contains(err.Error(), "ioctl") {
		t.Fatalf("%v", err)
	}
}

type failSet struct{ *fw }

func (failSet) SetFeature([]byte) error { return errors.New("ioctl: no such device") }

func TestProgressIsReportedPerChunk(t *testing.T) {
	pl, n := onePic()
	f := newFW(3)
	f.install(t)
	var seen []int
	if err := (&Port{C: f, D: f}).Upload(pl, n, func(d, total int) {
		if total != n {
			t.Errorf("total %d", total)
		}
		seen = append(seen, d)
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != n || seen[0] != 1 || seen[n-1] != n {
		t.Errorf("progress %v", seen)
	}
}
