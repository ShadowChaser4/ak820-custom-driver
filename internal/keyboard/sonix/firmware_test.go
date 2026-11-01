package sonix

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
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
		t.Fatalf("%v (run: go test ./internal/keyboard/sonix -update)", err)
	}
	if string(want) != b.String() {
		t.Errorf("%s changed.\n--- want\n%s--- got\n%s", name, want, b.String())
	}
}

func solid(c color.RGBA) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, animation.Width, animation.Height))
	for i := 0; i < len(im.Pix); i += 4 {
		im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return im
}

func TestCmdPackets(t *testing.T) {
	if p := Cmd(0x18, 0, false); len(p) != 64 || p[0] != 4 || p[1] != 0x18 || p[8] != 0 {
		t.Errorf("% x", p[:10])
	}
	if p := Cmd(0x18, 0, true); p[8] != 1 {
		t.Error("enable byte")
	}
	if p := imageCfg(0x0102); p[1] != 0x72 || p[2] != 3 || p[8] != 2 || p[9] != 1 {
		t.Errorf("% x", p[:10])
	}
}

func TestClockFramesMatchReference(t *testing.T) {
	f := ClockFrames(time.Date(2026, 11, 1, 13, 40, 59, 0, time.Local))
	if len(f) != 4 || f[0][1] != 0x18 || f[0][8] != 1 || f[1][1] != 0x28 || f[1][8] != 1 || f[3][1] != 0x02 || f[3][8] != 0 {
		t.Fatalf("% x", f)
	}
	if d := f[2]; !bytes.Equal(d[:11], []byte{0, 1, 0x5A, 26, 11, 1, 13, 40, 59, 0, 4}) || d[62] != 0xAA || d[63] != 0x55 {
		t.Errorf("TIME_DATA % x ... % x", d[:11], d[62:])
	}
}

func TestGoldenFrames(t *testing.T) {
	var all []byte
	for _, f := range ClockFrames(time.Date(2026, 11, 1, 13, 40, 59, 0, time.Local)) {
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
	p, chunks, err := BuildPayload([]animation.Frame{{Img: red, DelayMs: 100}, {Img: blue, DelayMs: 300}})
	if err != nil || chunks != 17 {
		t.Fatalf("%v %d", err, chunks)
	}
	sample := append([]byte(nil), p[:32]...)                                            // count + delays + zero fill
	sample = append(sample, p[256:272]...)                                              // first red pixels
	sample = append(sample, p[256+animation.FrameBytes:256+animation.FrameBytes+16]...) // first blue pixels
	golden(t, "payload-sample", sample)
	if tail := p[256+2*animation.FrameBytes:]; !bytes.Equal(tail, make([]byte, len(tail))) {
		t.Error("padding to whole chunks must be zero")
	}
}

func TestBuildPayloadDelays(t *testing.T) {
	fr := []animation.Frame{
		{Img: solid(color.RGBA{}), DelayMs: 100},
		{Img: solid(color.RGBA{}), DelayMs: 10},   // below the firmware minimum
		{Img: solid(color.RGBA{}), DelayMs: 900},  // beyond what the legacy format can hold
		{Img: solid(color.RGBA{}), DelayMs: 9000}, // beyond a byte
	}
	p, _, err := BuildPayload(fr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p[:5], []byte{4, 20, 16, 180, 255}) {
		t.Errorf("header % x", p[:5])
	}
}

func TestBuildPayloadLimits(t *testing.T) {
	if _, _, err := BuildPayload(nil); err == nil {
		t.Error("no frames must fail")
	}
	if _, _, err := BuildPayload(make([]animation.Frame, 256)); err == nil {
		t.Error("256 frames must fail")
	}
}

// ---- a stateful fake of the keyboard firmware (see FINDINGS.md in ak820pro-linux) ----

// fw models what matters: the 64-packet ring counter that is NOT reset by IMAGE_CFG,
// an ACK each time the ring wraps, and a 2 s watchdog. It runs in a synctest bubble,
// so time.Sleep and time.Now are virtual and tests take no real time.
type fw struct {
	idx       int // ring position, 0..63
	lastPkt   time.Time
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
	writeTime time.Duration // how long each 4096-byte write blocks
	onBig     func(n int)   // called before the n-th 4096-byte write
}

func (f *fw) SetFeature(p []byte) error {
	f.setFeats = append(f.setFeats, append([]byte(nil), p...))
	switch p[1] {
	case 0x72:
		f.inData, f.lastPkt = true, time.Now() // starts the watchdog; does NOT reset f.idx
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

func (f *fw) Write(p []byte) error {
	if len(p) == ChunkSize {
		f.bigWrites++
		if f.onBig != nil {
			f.onBig(f.bigWrites)
		}
		f.big = append(f.big, f.idx)
		if f.idx != 0 {
			f.misalign++
		}
		if f.writeFail == f.bigWrites {
			return errors.New("usb write failed")
		}
		time.Sleep(f.writeTime)
	}
	if f.inData && time.Since(f.lastPkt) > 2*time.Second {
		f.watchdog = true
	}
	for range len(p) / 64 {
		f.idx++
		if f.idx == 64 {
			f.idx = 0
			f.ackCount++
			switch f.ackCount {
			case f.dropAck:
			case f.badMagic:
				f.acks = append(f.acks, []byte{0xDE, 0xAD, 0xBE, 0xEF})
			default:
				f.acks = append(f.acks, append([]byte{0x01, 0x5A, 0x02}, make([]byte, 61)...))
			}
		}
	}
	f.lastPkt = time.Now()
	return nil
}

func (f *fw) Read(ctx context.Context, t time.Duration) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(f.acks) > 0 {
		a := f.acks[0]
		f.acks = f.acks[1:]
		return a, nil
	}
	time.Sleep(t) // we waited the full timeout
	return nil, os.ErrDeadlineExceeded
}

func onePic() ([]byte, int) {
	p, n, _ := BuildPayload([]animation.Frame{{Img: solid(color.RGBA{0, 200, 0, 255}), DelayMs: 100}})
	return p, n
}

func port(f *fw) *Port { return &Port{Control: f, Data: f} }

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

// The firmware bug the alignment step exists for: whatever ring position the
// previous session left behind, every real chunk must start at position 0.
func TestUploadAlignsFromEveryRingPosition(t *testing.T) {
	pl, n := onePic()
	for start := range 64 {
		synctest.Test(t, func(t *testing.T) {
			f := &fw{idx: start}
			if err := port(f).Upload(context.Background(), pl, n, nil); err != nil {
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
		})
	}
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
		synctest.Test(t, func(t *testing.T) {
			f := &fw{idx: 11}
			c.set(f)
			err := port(f).Upload(context.Background(), pl, n, nil)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.wantErr)
			}
			if savesAfterRealTransfer(f, n) {
				t.Errorf("%s: SAVE was sent after a transfer that did not complete", c.name)
			}
		})
	}
}

// noAcks swallows every ACK.
type noAcks struct{ *fw }

func (n *noAcks) Write(p []byte) error { err := n.fw.Write(p); n.acks = nil; return err }

func TestAlignmentThatNeverLocksGivesUpWithAHumanMessage(t *testing.T) {
	pl, n := onePic()
	synctest.Test(t, func(t *testing.T) {
		f := &fw{}
		err := (&Port{Control: f, Data: &noAcks{f}}).Upload(context.Background(), pl, n, nil)
		if err == nil || !strings.Contains(err.Error(), "replug the cable") {
			t.Fatalf("%v", err)
		}
		if savesAfterRealTransfer(f, n) {
			t.Error("the real transfer must not have started")
		}
	})
}

func TestUploadRejectsMisSizedPayload(t *testing.T) {
	if err := port(&fw{}).Upload(context.Background(), make([]byte, 100), 1, nil); err == nil {
		t.Error("must reject")
	}
}

func TestControlChannelHandshake(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	synctest.Test(t, func(t *testing.T) {
		f := &fw{getFails: 1}
		if err := port(f).SyncClock(context.Background(), ClockFrames(now)); err != nil {
			t.Errorf("a single failed read must not abort: %v", err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		f := &fw{getFails: 5}
		err := port(f).SyncClock(context.Background(), ClockFrames(now))
		if err == nil || !strings.Contains(err.Error(), "not answering") {
			t.Errorf("two stalls in a row mean the channel is dead: %v", err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		f := &fw{}
		p := &Port{Control: &flaky{fw: f}, Data: f}
		if err := p.SyncClock(context.Background(), ClockFrames(now)); err != nil {
			t.Errorf("stalls that are not consecutive never add up: %v", err)
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

type failSet struct{ *fw }

func (failSet) SetFeature([]byte) error { return errors.New("ioctl: no such device") }

func TestSetFeatureErrorsPropagate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fw{}
		err := (&Port{Control: failSet{f}, Data: f}).SyncClock(context.Background(), ClockFrames(time.Now()))
		if err == nil || !strings.Contains(err.Error(), "ioctl") {
			t.Fatalf("%v", err)
		}
	})
}

func TestProgressIsReportedPerChunk(t *testing.T) {
	pl, n := onePic()
	synctest.Test(t, func(t *testing.T) {
		var seen []int
		err := port(&fw{idx: 3}).Upload(context.Background(), pl, n, func(d, total int) {
			if total != n {
				t.Errorf("total %d", total)
			}
			seen = append(seen, d)
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != n || seen[0] != 1 || seen[n-1] != n {
			t.Errorf("progress %v", seen)
		}
	})
}

// unplugged starts failing every read after the given number of reads, as a real
// device does when the cable is pulled.
type unplugged struct {
	*fw
	after, reads int
	gone         bool
	lateWrites   int
}

func (u *unplugged) Write(p []byte) error {
	if u.gone {
		u.lateWrites++
		return nil
	}
	return u.fw.Write(p)
}

var errGone = errors.New("no such device")

func (u *unplugged) Read(ctx context.Context, t time.Duration) ([]byte, error) {
	if u.reads++; u.reads > u.after {
		u.gone = true
		return nil, errGone
	}
	return u.fw.Read(ctx, t)
}

// Regression: read errors were dropped while draining and aligning, so the code
// kept writing to a keyboard that was already gone.
func TestUnplugDuringUploadReportsTheRealError(t *testing.T) {
	pl, n := onePic()
	for after := range 12 {
		synctest.Test(t, func(t *testing.T) {
			f := &fw{idx: 5}
			u := &unplugged{fw: f, after: after}
			err := (&Port{Control: f, Data: u}).Upload(context.Background(), pl, n, nil)
			if !errors.Is(err, errGone) {
				t.Errorf("unplug after %d reads: got %v", after, err)
			}
			if savesAfterRealTransfer(f, n) {
				t.Errorf("unplug after %d reads: SAVE sent", after)
			}
			if u.lateWrites != 0 {
				t.Errorf("unplug after %d reads: %d writes after the device failed", after, u.lateWrites)
			}
		})
	}
}

// Ctrl-C or closing the browser tab at any point must stop cleanly and never commit.
func TestCancelAtAnyChunkNeverCommits(t *testing.T) {
	pl, n := onePic()
	for cancelAt := 1; cancelAt <= n+1; cancelAt++ { // 1 is the alignment chunk
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fw{idx: 9, onBig: func(k int) {
				if k == cancelAt {
					cancel()
				}
			}}
			err := port(f).Upload(ctx, pl, n, nil)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancel at chunk %d: got %v", cancelAt, err)
			}
			if savesAfterRealTransfer(f, n) {
				t.Errorf("cancel at chunk %d: SAVE sent", cancelAt)
			}
		})
	}
}

func TestCancelDuringSetupAndDelaysStopsPromptly(t *testing.T) {
	pl, n := onePic()
	for _, wait := range []time.Duration{time.Millisecond, 60 * time.Millisecond, 500 * time.Millisecond} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			defer cancel()
			f := &fw{idx: 2}
			err := port(f).Upload(ctx, pl, n, nil)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("after %v: got %v", wait, err)
			}
			if savesAfterRealTransfer(f, n) {
				t.Errorf("after %v: SAVE sent", wait)
			}
		})
	}
}

// A keyboard that keeps answering, but too slowly, must not hold an upload open
// forever. This needs virtual time: the real limit is minutes away.
func TestUploadDeadlineFires(t *testing.T) {
	frames := make([]animation.Frame, 5)
	for i := range frames {
		frames[i] = animation.Frame{Img: solid(color.RGBA{}), DelayMs: 100}
	}
	pl, n, err := BuildPayload(frames)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		f := &fw{writeTime: 3 * time.Second}
		err := port(f).Upload(context.Background(), pl, n, nil)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("%v", err)
		}
		if savesAfterRealTransfer(f, n) {
			t.Error("SAVE sent after the deadline")
		}
	})
}
