package legacy

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"testing"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
)

func TestInfoChunks32(t *testing.T) {
	c := Chunks(32, CmdInfo, nil, 48)
	if len(c) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(c))
	}
	if !bytes.Equal(c[0][:8], []byte{0xAA, 0x10, 24, 0, 0, 0, 0, 0}) {
		t.Errorf("chunk0 % x", c[0][:8])
	}
	if !bytes.Equal(c[1][:8], []byte{0xAA, 0x10, 24, 24, 0, 0, 1, 0}) {
		t.Errorf("chunk1 % x", c[1][:8])
	}
}

func TestInfoChunk64And4096(t *testing.T) {
	for _, n := range []int{64, 4096} {
		c := Chunks(n, CmdInfo, nil, 48)
		if len(c) != 1 || len(c[0]) != n || !bytes.Equal(c[0][:8], []byte{0xAA, 0x10, 48, 0, 0, 0, 1, 0}) {
			t.Errorf("len %d: %d chunks, % x", n, len(c), c[0][:8])
		}
	}
}

func TestAnimChunks(t *testing.T) {
	c := AnimChunks(4104, make([]byte, 4096+10))
	if len(c) != 2 {
		t.Fatalf("got %d", len(c))
	}
	if !bytes.Equal(c[1][:8], []byte{0xAA, 0x50, 1, 0, 2, 0, 0x50, 0x06}) {
		t.Errorf("% x", c[1][:8])
	}
}

type fake struct {
	replies [][]byte
	writes  [][]byte
	readErr error
}

func (f *fake) Write(p []byte) error { f.writes = append(f.writes, p); return nil }
func (f *fake) Read(ctx context.Context, _ time.Duration) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.readErr != nil {
		return nil, f.readErr
	}
	if len(f.replies) == 0 || len(f.writes) == 0 {
		return nil, os.ErrDeadlineExceeded
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r, nil
}

func TestTransactSkipsOtherReplies(t *testing.T) {
	f := &fake{replies: [][]byte{{0x55, 0x99, 0}, {0x55, 0x10, 1}}}
	r, err := (&Port{Conn: f}).Transact(context.Background(), make([]byte, 64), CmdInfo, 50*time.Millisecond, 1)
	if err != nil || r[1] != 0x10 {
		t.Fatalf("%v % x", err, r)
	}
}

func TestTransactRetriesThenReportsNoReply(t *testing.T) {
	f := &fake{}
	_, err := (&Port{Conn: f}).Transact(context.Background(), make([]byte, 64), CmdInfo, 10*time.Millisecond, 2)
	if !errors.Is(err, keyboard.ErrNoReply) {
		t.Fatalf("%v", err)
	}
	if len(f.writes) != 3 {
		t.Errorf("want 3 attempts, got %d", len(f.writes))
	}
}

func TestRealReadErrorsAreNotRetriedAsTimeouts(t *testing.T) {
	gone := errors.New("no such device")
	f := &fake{readErr: gone}
	_, err := (&Port{Conn: f}).Transact(context.Background(), make([]byte, 64), CmdInfo, 10*time.Millisecond, 3)
	if !errors.Is(err, gone) || len(f.writes) != 0 {
		t.Fatalf("%v, %d writes", err, len(f.writes))
	}
}

func TestCancelledContextStopsAnExchange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fake{}
	_, err := (&Port{Conn: f, Timeout: time.Second, Retries: 3}).Exchange(ctx, Chunks(64, CmdInfo, nil, 48), CmdInfo)
	if !errors.Is(err, context.Canceled) || len(f.writes) != 0 {
		t.Fatalf("%v, %d writes", err, len(f.writes))
	}
}

func solid(c color.RGBA) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, animation.Width, animation.Height))
	for i := 0; i < len(im.Pix); i += 4 {
		im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return im
}

func TestEncodeShape(t *testing.T) {
	f := animation.Frame{Img: solid(color.RGBA{0, 0, 0, 255}), DelayMs: 100}
	p := Encode([]animation.Frame{f, f, f})
	if len(p) != 256+3*animation.FrameBytes {
		t.Fatalf("len %d", len(p))
	}
	// count, delays of frames 0..N-2 in 2 ms units, terminator, then 0xFF fill
	if !bytes.Equal(p[:5], []byte{3, 50, 50, 0, 0xFF}) {
		t.Errorf("% x", p[:5])
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	p := Encode([]animation.Frame{{Img: solid(color.RGBA{255, 0, 0, 255}), DelayMs: 100}, {Img: solid(color.RGBA{0, 0, 255, 255}), DelayMs: 200}})
	fr, err := Decode(p)
	if err != nil || len(fr) != 2 {
		t.Fatalf("%v %d", err, len(fr))
	}
	if fr[0].Img.RGBAAt(5, 5) != (color.RGBA{255, 0, 0, 255}) || fr[1].Img.RGBAAt(5, 5) != (color.RGBA{0, 0, 255, 255}) {
		t.Errorf("pixels %v %v", fr[0].Img.RGBAAt(5, 5), fr[1].Img.RGBAAt(5, 5))
	}
	if fr[0].DelayMs != 100 {
		t.Errorf("delay %d", fr[0].DelayMs)
	}
	if _, err := Decode(p[:300]); err == nil {
		t.Error("truncated payload must error")
	}
}

func FuzzDecode(f *testing.F) {
	ok := Encode([]animation.Frame{{Img: solid(color.RGBA{1, 2, 3, 255}), DelayMs: 100}})
	f.Add(ok)
	f.Add(ok[:300])
	f.Add([]byte{255})
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := Decode(b)
		if err == nil && len(fr) != int(b[0]) {
			t.Fatalf("%d frames from a header that says %d", len(fr), b[0])
		}
	})
}
