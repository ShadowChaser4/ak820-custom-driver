// Package img turns images/GIFs into the AK820 screen payload (128x128 RGB565-LE).
package img

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"strings"
)

const W, H = 128, 128

type Frame struct {
	Img     *image.RGBA // W x H, opaque
	DelayMs int
}

// Load decodes a PNG/JPEG/GIF file and fits every frame to 128x128.
// fit is "cover", "contain" or "stretch".
func Load(path, fit string, bg color.Color) ([]Frame, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(b, fit, bg)
}

// Decode is Load for in-memory data.
func Decode(data []byte, fit string, bg color.Color) ([]Frame, error) {
	if g, err := gif.DecodeAll(bytes.NewReader(data)); err == nil {
		return fromGIF(g, fit, bg), nil
	}
	im, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unsupported image (PNG, JPEG and GIF work): %w", err)
	}
	return []Frame{{Fit(im, fit, bg), 100}}, nil
}

// ParseColor accepts black, white or RRGGBB / #RRGGBB (black on anything else).
func ParseColor(s string) color.Color {
	switch strings.ToLower(s) {
	case "white":
		return color.White
	case "black", "":
		return color.Black
	}
	var r, g, b uint8
	if _, err := fmt.Sscanf(strings.TrimPrefix(s, "#"), "%02x%02x%02x", &r, &g, &b); err == nil {
		return color.RGBA{r, g, b, 255}
	}
	return color.Black
}

// ParsePayload is the inverse of BuildPayload (RGB565 is lossy, so pixels are
// expanded back to 8 bits).
func ParsePayload(p []byte) ([]Frame, error) {
	if len(p) < 257 || p[0] == 0 {
		return nil, fmt.Errorf("payload too short")
	}
	n := int(p[0])
	if len(p) < 256+n*W*H*2 {
		return nil, fmt.Errorf("payload truncated: %d frames need %d bytes, have %d", n, 256+n*W*H*2, len(p))
	}
	out := make([]Frame, 0, n)
	for k := 0; k < n; k++ {
		raw := p[256+k*W*H*2 : 256+(k+1)*W*H*2]
		im := image.NewRGBA(image.Rect(0, 0, W, H))
		for j := 0; j < W*H; j++ {
			v := int(raw[2*j]) | int(raw[2*j+1])<<8
			im.Pix[4*j] = uint8((v >> 11) * 255 / 31)
			im.Pix[4*j+1] = uint8((v >> 5 & 63) * 255 / 63)
			im.Pix[4*j+2] = uint8((v & 31) * 255 / 31)
			im.Pix[4*j+3] = 255
		}
		d := 500
		if n > 1 {
			if k < n-1 {
				d = int(p[k+1]) * 2
			} else {
				d = int(p[n-1]) * 2
			}
		}
		out = append(out, Frame{im, d})
	}
	return out, nil
}

// Scaled returns a nearest-neighbour copy at factor f.
func Scaled(im *image.RGBA, f int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, im.Bounds().Dx()*f, im.Bounds().Dy()*f))
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			out.SetRGBA(x, y, im.RGBAAt(x/f, y/f))
		}
	}
	return out
}

func fromGIF(g *gif.GIF, fit string, bg color.Color) []Frame {
	w, h := g.Config.Width, g.Config.Height
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	var out []Frame
	for i, p := range g.Image {
		var prev *image.RGBA
		if i < len(g.Disposal) && g.Disposal[i] == gif.DisposalPrevious {
			prev = image.NewRGBA(canvas.Bounds())
			copy(prev.Pix, canvas.Pix)
		}
		draw.Draw(canvas, p.Bounds(), p, p.Bounds().Min, draw.Over)
		snap := image.NewRGBA(canvas.Bounds())
		copy(snap.Pix, canvas.Pix)
		d := 100
		if i < len(g.Delay) && g.Delay[i] > 0 {
			d = g.Delay[i] * 10
		}
		out = append(out, Frame{Fit(snap, fit, bg), d})
		if i < len(g.Disposal) {
			switch g.Disposal[i] {
			case gif.DisposalBackground:
				draw.Draw(canvas, p.Bounds(), image.Transparent, image.Point{}, draw.Src)
			case gif.DisposalPrevious:
				canvas = prev
			}
		}
	}
	return out
}

// Fit scales src into a 128x128 opaque canvas over bg.
func Fit(src image.Image, fit string, bg color.Color) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	sb := src.Bounds()
	sw, sh := float64(sb.Dx()), float64(sb.Dy())
	srcR := [4]float64{float64(sb.Min.X), float64(sb.Min.Y), float64(sb.Max.X), float64(sb.Max.Y)}
	dr := image.Rect(0, 0, W, H)
	switch fit {
	case "stretch":
	case "contain":
		s := math.Min(W/sw, H/sh)
		w, h := max(1, int(math.Round(sw*s))), max(1, int(math.Round(sh*s)))
		dr = image.Rect((W-w)/2, (H-h)/2, (W-w)/2+w, (H-h)/2+h)
	default: // cover
		s := math.Max(W/sw, H/sh)
		cw, ch := W/s, H/s
		srcR[0] += (sw - cw) / 2
		srcR[1] += (sh - ch) / 2
		srcR[2], srcR[3] = srcR[0]+cw, srcR[1]+ch
	}
	scale(dst, dr, src, srcR)
	return dst
}

// scale area-averages the float source rect into dr, compositing over dst.
func scale(dst *image.RGBA, dr image.Rectangle, src image.Image, sr [4]float64) {
	sx := (sr[2] - sr[0]) / float64(dr.Dx())
	sy := (sr[3] - sr[1]) / float64(dr.Dy())
	sb := src.Bounds()
	for y := dr.Min.Y; y < dr.Max.Y; y++ {
		y0 := sr[1] + float64(y-dr.Min.Y)*sy
		y1 := y0 + sy
		for x := dr.Min.X; x < dr.Max.X; x++ {
			x0 := sr[0] + float64(x-dr.Min.X)*sx
			x1 := x0 + sx
			var r, g, b, a, wsum float64
			for yy := int(math.Floor(y0)); yy < int(math.Ceil(y1)); yy++ {
				wy := math.Min(float64(yy+1), y1) - math.Max(float64(yy), y0)
				for xx := int(math.Floor(x0)); xx < int(math.Ceil(x1)); xx++ {
					wx := math.Min(float64(xx+1), x1) - math.Max(float64(xx), x0)
					px, py := min(max(xx, sb.Min.X), sb.Max.X-1), min(max(yy, sb.Min.Y), sb.Max.Y-1)
					cr, cg, cb, ca := src.At(px, py).RGBA() // premultiplied 16-bit
					w := wx * wy
					r += float64(cr) * w
					g += float64(cg) * w
					b += float64(cb) * w
					a += float64(ca) * w
					wsum += w
				}
			}
			if wsum == 0 {
				continue
			}
			fg := color.RGBA64{uint16(r / wsum), uint16(g / wsum), uint16(b / wsum), uint16(a / wsum)}
			bgc := dst.RGBAAt(x, y)
			br, bg_, bb, _ := bgc.RGBA()
			inv := 0xFFFF - uint32(fg.A)
			dst.SetRGBA(x, y, color.RGBA{
				uint8((uint32(fg.R) + br*inv/0xFFFF) >> 8),
				uint8((uint32(fg.G) + bg_*inv/0xFFFF) >> 8),
				uint8((uint32(fg.B) + bb*inv/0xFFFF) >> 8),
				255})
		}
	}
}

// RGB565LE packs an opaque image as little-endian RGB565.
func RGB565LE(im *image.RGBA) []byte {
	out := make([]byte, 0, im.Bounds().Dx()*im.Bounds().Dy()*2)
	for y := im.Bounds().Min.Y; y < im.Bounds().Max.Y; y++ {
		for x := im.Bounds().Min.X; x < im.Bounds().Max.X; x++ {
			c := im.RGBAAt(x, y)
			v := uint16(c.R&0xF8)<<8 | uint16(c.G&0xFC)<<3 | uint16(c.B>>3)
			out = append(out, byte(v), byte(v>>8))
		}
	}
	return out
}

// BuildPayload: 256-byte 0xFF header (count, per-frame delay in 2 ms units,
// terminating 0) followed by the frames.
func BuildPayload(frames []Frame) []byte {
	n := len(frames)
	hdr := make([]byte, 256)
	for i := range hdr {
		hdr[i] = 0xFF
	}
	hdr[0] = byte(n)
	for k := 0; k < n-1; k++ {
		hdr[k+1] = byte(min(255, max(1, int(math.Round(float64(frames[k].DelayMs)/2)))))
	}
	hdr[n] = 0
	out := hdr
	for _, f := range frames {
		out = append(out, RGB565LE(f.Img)...)
	}
	return out
}
