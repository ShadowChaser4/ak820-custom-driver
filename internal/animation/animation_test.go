package animation

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"strings"
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
	for _, fit := range []Fit{FitCover, FitContain, FitStretch} {
		out := Scale(solid(red, 300, 100), fit, color.Black)
		if out.Bounds().Dx() != Width || out.Bounds().Dy() != Height {
			t.Fatalf("%d size %v", fit, out.Bounds())
		}
		if fit == FitContain {
			if out.RGBAAt(64, 2) != (color.RGBA{0, 0, 0, 255}) || out.RGBAAt(64, 64) != red {
				t.Errorf("contain: bars/centre wrong: %v %v", out.RGBAAt(64, 2), out.RGBAAt(64, 64))
			}
		} else if out.RGBAAt(0, 0) != red || out.RGBAAt(127, 127) != red {
			t.Errorf("%d corners %v %v", fit, out.RGBAAt(0, 0), out.RGBAAt(127, 127))
		}
	}
}

func TestHugeDeclaredSizeIsRejectedBeforeAllocating(t *testing.T) {
	// A well-formed PNG header (correct checksum) that claims 20000 x 20000 pixels,
	// with no pixel data at all: decoding it for real would try to allocate ~1.6 GB.
	ihdr := []byte{0, 0, 0x4e, 0x20, 0, 0, 0x4e, 0x20, 8, 2, 0, 0, 0}
	var png bytes.Buffer
	png.WriteString("\x89PNG\r\n\x1a\n")
	binary.Write(&png, binary.BigEndian, uint32(len(ihdr)))
	png.WriteString("IHDR")
	png.Write(ihdr)
	binary.Write(&png, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	if _, err := Decode(png.Bytes(), FitCover, color.Black); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("got %v", err)
	}
}

func TestGIFLimits(t *testing.T) {
	// logical screen 4000x4000 with no frames
	big := []byte("GIF89a\xa0\x0f\xa0\x0f\x00\x00\x00;")
	if _, err := Decode(big, FitCover, color.Black); err == nil {
		t.Fatal("4000x4000 GIF must be refused")
	}
	if n := countGIFFrames([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")); n != 0 {
		t.Errorf("empty GIF has %d frames", n)
	}
	for _, junk := range [][]byte{nil, []byte("GIF89a"), []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00\x2c"), []byte("nope")} {
		if n := countGIFFrames(junk); n >= 0 && len(junk) < 13 {
			t.Errorf("%q -> %d", junk, n)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"))
	f.Add([]byte("\xff\xd8\xff\xe0"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := Decode(b, FitCover, color.Black)
		if err == nil {
			for _, x := range fr {
				if x.Img.Bounds().Dx() != Width || x.Img.Bounds().Dy() != Height {
					t.Fatalf("frame is %v", x.Img.Bounds())
				}
			}
		}
	})
}

func TestParseFit(t *testing.T) {
	for in, want := range map[string]Fit{"": FitCover, "cover": FitCover, "contain": FitContain, "stretch": FitStretch} {
		if got, err := ParseFit(in); err != nil || got != want {
			t.Errorf("ParseFit(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseFit("zoom"); err == nil {
		t.Error("an unknown fit must be an error")
	}
}

func TestQuantizeMatchesTheScreen(t *testing.T) {
	got := Quantize(solid(color.RGBA{255, 3, 7, 255}, Width, Height))
	if got.RGBAAt(9, 9) != (color.RGBA{255, 0, 0, 255}) {
		t.Errorf("%v", got.RGBAAt(9, 9))
	}
}
