package sonix

import (
	"bytes"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
)

func init() { sleep = func(time.Duration) {} }

type fakeCtrl struct {
	sets [][]byte
	gets int
}

func (f *fakeCtrl) SetFeature(p []byte) error {
	f.sets = append(f.sets, append([]byte(nil), p...))
	return nil
}
func (f *fakeCtrl) GetFeature() ([]byte, error) { f.gets++; return make([]byte, 64), nil }

// fakeData acknowledges the way the firmware does: one 01 5A 02 reply for every
// 4096 bytes written. acks=false simulates a keyboard that never answers.
type fakeData struct {
	written int
	pending int
	acks    bool
	chunks  [][]byte
}

func (f *fakeData) WriteData(p []byte) error {
	f.written += len(p)
	if len(p) == ChunkSize {
		f.chunks = append(f.chunks, append([]byte(nil), p...))
	}
	for f.written >= ChunkSize {
		f.written -= ChunkSize
		if f.acks {
			f.pending++
		}
	}
	return nil
}
func (f *fakeData) ReadAck(time.Duration) ([]byte, error) {
	if f.pending > 0 {
		f.pending--
		return append([]byte{0x01, 0x5A, 0x02}, make([]byte, 61)...), nil
	}
	return nil, nil
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
	now := time.Date(2026, 11, 1, 13, 40, 59, 0, time.Local)
	f := ClockFrames(now)
	if len(f) != 4 {
		t.Fatal(len(f))
	}
	want := []struct {
		i int
		b byte
	}{{0, 4}, {1, 0x18}, {8, 1}}
	for _, w := range want {
		if f[0][w.i] != w.b {
			t.Errorf("START[%d]=%#x", w.i, f[0][w.i])
		}
	}
	if f[1][1] != 0x28 || f[1][8] != 1 {
		t.Errorf("TIME_CFG % x", f[1][:10])
	}
	d := f[2]
	if !bytes.Equal(d[:11], []byte{0, 1, 0x5A, 26, 11, 1, 13, 40, 59, 0, 4}) || d[62] != 0xAA || d[63] != 0x55 {
		t.Errorf("TIME_DATA % x ... % x", d[:11], d[62:])
	}
	if f[3][1] != 0x02 || f[3][8] != 0 {
		t.Errorf("SAVE % x", f[3][:10])
	}
}

func TestBuildPayloadShape(t *testing.T) {
	fr := []animation.Frame{{Img: solid(color.RGBA{255, 0, 0, 255}), DelayMs: 100}, {Img: solid(color.RGBA{0, 0, 255, 255}), DelayMs: 10}}
	p, chunks, err := BuildPayload(fr)
	if err != nil {
		t.Fatal(err)
	}
	if len(p)%ChunkSize != 0 || len(p) != chunks*ChunkSize || chunks != 17 {
		t.Fatalf("len %d chunks %d", len(p), chunks)
	}
	if p[0] != 2 || p[1] != 20 || p[2] != 16 { // 100ms/5 = 20; 10ms/5 = 2 -> clamped to 16
		t.Errorf("header % x", p[:4])
	}
	if p[256] != 0x00 || p[257] != 0xF8 { // red, RGB565 little-endian
		t.Errorf("first pixel % x", p[256:258])
	}
}

func TestBuildPayloadLimits(t *testing.T) {
	if _, _, err := BuildPayload(nil); err == nil {
		t.Error("no frames must fail")
	}
	many := make([]animation.Frame, 256)
	if _, _, err := BuildPayload(many); err == nil {
		t.Error("256 frames must fail")
	}
}

func TestSyncClockSendsFourFramesInOrder(t *testing.T) {
	c := &fakeCtrl{}
	p := &Port{C: c, D: &fakeData{}}
	if err := p.SyncClock(time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(c.sets) != 4 || c.sets[0][1] != 0x18 || c.sets[1][1] != 0x28 || c.sets[2][1] != 0x01 || c.sets[3][1] != 0x02 {
		t.Fatalf("sequence wrong: %d frames", len(c.sets))
	}
	if c.gets != 2 {
		t.Errorf("handshakes: %d", c.gets)
	}
}

func TestUploadCommitsOnlyAfterEveryChunkIsAcked(t *testing.T) {
	c, d := &fakeCtrl{}, &fakeData{acks: true}
	p := &Port{C: c, D: d}
	pl, n, _ := BuildPayload([]animation.Frame{{Img: solid(color.RGBA{0, 255, 0, 255}), DelayMs: 100}})
	done := 0
	if err := p.Upload(pl, n, func(a, b int) { done = a }); err != nil {
		t.Fatal(err)
	}
	if done != n {
		t.Errorf("progress %d/%d", done, n)
	}
	last := c.sets[len(c.sets)-1]
	if last[1] != 0x02 {
		t.Errorf("last control frame must be SAVE, got % x", last[:3])
	}
}

func TestUploadWithoutAcksNeverSaves(t *testing.T) {
	c, d := &fakeCtrl{}, &fakeData{acks: false}
	p := &Port{C: c, D: d}
	pl, n, _ := BuildPayload([]animation.Frame{{Img: solid(color.RGBA{}), DelayMs: 100}})
	if err := p.Upload(pl, n, nil); err == nil {
		t.Fatal("expected failure")
	}
	// Align() itself ends with a SAVE; what must not happen is a SAVE after IMAGE_CFG with the real chunk count.
	for _, f := range c.sets {
		if f[1] == 0x72 && (f[8] == byte(n)) && n != 3 {
			t.Fatal("real IMAGE_CFG was sent although alignment failed")
		}
	}
}

func TestUploadRejectsMisSizedPayload(t *testing.T) {
	p := &Port{C: &fakeCtrl{}, D: &fakeData{acks: true}}
	if err := p.Upload(make([]byte, 100), 1, nil); err == nil {
		t.Error("must reject")
	}
}
