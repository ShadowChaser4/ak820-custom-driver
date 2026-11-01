package img

import (
	"image"
	"image/color"
	"testing"
)

func solid(c color.RGBA, w, h int) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(im.Pix); i += 4 {
		im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return im
}

func TestRGB565LE(t *testing.T) {
	cases := map[color.RGBA][2]byte{
		{255, 0, 0, 255}: {0x00, 0xF8},
		{0, 255, 0, 255}: {0xE0, 0x07},
		{0, 0, 255, 255}: {0x1F, 0x00},
	}
	for c, want := range cases {
		got := RGB565LE(solid(c, 1, 1))
		if got[0] != want[0] || got[1] != want[1] {
			t.Errorf("%v: % x want % x", c, got, want)
		}
	}
}

func TestFitKeepsColourAndSize(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	for _, fit := range []string{"cover", "contain", "stretch"} {
		out := Fit(solid(red, 300, 100), fit, color.Black)
		if out.Bounds().Dx() != W || out.Bounds().Dy() != H {
			t.Fatalf("%s size %v", fit, out.Bounds())
		}
		if fit == "contain" {
			if out.RGBAAt(64, 2) != (color.RGBA{0, 0, 0, 255}) || out.RGBAAt(64, 64) != red {
				t.Errorf("contain: bars/centre wrong: %v %v", out.RGBAAt(64, 2), out.RGBAAt(64, 64))
			}
		} else if out.RGBAAt(0, 0) != red || out.RGBAAt(127, 127) != red {
			t.Errorf("%s corners %v %v", fit, out.RGBAAt(0, 0), out.RGBAAt(127, 127))
		}
	}
}

func TestBuildPayload(t *testing.T) {
	f := Frame{solid(color.RGBA{0, 0, 0, 255}, W, H), 100}
	p := BuildPayload([]Frame{f, f, f})
	if len(p) != 256+3*W*H*2 {
		t.Fatalf("len %d", len(p))
	}
	// count, delays of frames 0..N-2 in 2 ms units, terminator, then 0xFF fill
	want := []byte{3, 50, 50, 0, 0xFF}
	for i, w := range want {
		if p[i] != w {
			t.Errorf("hdr[%d]=%#x want %#x", i, p[i], w)
		}
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	a := solid(color.RGBA{255, 0, 0, 255}, W, H)
	b := solid(color.RGBA{0, 0, 255, 255}, W, H)
	p := BuildPayload([]Frame{{a, 100}, {b, 200}})
	fr, err := ParsePayload(p)
	if err != nil || len(fr) != 2 {
		t.Fatalf("%v %d", err, len(fr))
	}
	if fr[0].Img.RGBAAt(5, 5) != (color.RGBA{255, 0, 0, 255}) || fr[1].Img.RGBAAt(5, 5) != (color.RGBA{0, 0, 255, 255}) {
		t.Errorf("pixels %v %v", fr[0].Img.RGBAAt(5, 5), fr[1].Img.RGBAAt(5, 5))
	}
	if fr[0].DelayMs != 100 {
		t.Errorf("delay %d", fr[0].DelayMs)
	}
	if _, err := ParsePayload(p[:300]); err == nil {
		t.Error("truncated payload must error")
	}
}
