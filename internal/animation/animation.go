// Package animation turns images and GIFs into 128x128 frames for the keyboard screen.
package animation

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

// Width and Height are the size of the screen in pixels.
const (
	Width  = 128
	Height = 128
)

// Fit says how a picture is mapped onto the square screen.
type Fit int

// The ways a picture can be fitted.
const (
	FitCover   Fit = iota // fill the screen, cropping the edges
	FitContain            // show everything, adding borders
	FitStretch            // ignore the aspect ratio
)

// ParseFit reads a fit name. The empty string means FitCover.
func ParseFit(s string) (Fit, error) {
	switch s {
	case "", "cover":
		return FitCover, nil
	case "contain":
		return FitContain, nil
	case "stretch":
		return FitStretch, nil
	}
	return FitCover, fmt.Errorf("unknown fit %q (use cover, contain or stretch)", s)
}

// Frame is one picture on the screen and how long it stays there.
type Frame struct {
	Img     *image.RGBA // Width x Height, opaque
	DelayMs int
}

// Load decodes a PNG/JPEG/GIF file and fits every frame to 128x128.
func Load(path string, fit Fit, bg color.Color) ([]Frame, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(b, fit, bg)
}

// Limits protect against pictures that are tiny on disk but claim a huge size
// (decompression bombs): the decoder would try to allocate all of it.
const (
	MaxPixels    = 25_000_000 // per picture (e.g. 5000x5000)
	MaxGIFSide   = 2048       // GIF canvas width / height
	MaxGIFFrames = 2000       // we only ever send the first 255
)

// countGIFFrames walks the GIF block structure (no pixel decoding) and counts
// image descriptors. It returns -1 for data that is not a well-formed GIF stream.
func countGIFFrames(b []byte) int {
	if len(b) < 13 || (string(b[:6]) != "GIF87a" && string(b[:6]) != "GIF89a") {
		return -1
	}
	i := 13
	if b[10]&0x80 != 0 {
		i += 3 << (uint(b[10]&7) + 1)
	}
	skip := func() bool { // sub-blocks until a zero-length terminator
		for i < len(b) {
			n := int(b[i])
			i++
			if n == 0 {
				return true
			}
			i += n
		}
		return false
	}
	frames := 0
	for i < len(b) {
		switch b[i] {
		case 0x3B: // trailer
			return frames
		case 0x21: // extension
			i += 2
			if !skip() {
				return -1
			}
		case 0x2C: // image descriptor
			if i+10 > len(b) {
				return -1
			}
			flags := b[i+9]
			i += 10
			if flags&0x80 != 0 {
				i += 3 << (uint(flags&7) + 1)
			}
			i++ // LZW minimum code size
			if !skip() {
				return -1
			}
			if frames++; frames > MaxGIFFrames {
				return frames
			}
		default:
			return -1
		}
	}
	return frames
}

// Decode is Load for in-memory data.
func Decode(data []byte, fit Fit, bg color.Color) ([]Frame, error) {
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		cfg, err := gif.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("this GIF could not be read: %w", err)
		}
		if cfg.Width > MaxGIFSide || cfg.Height > MaxGIFSide {
			return nil, fmt.Errorf("this GIF is %dx%d pixels; the largest supported is %dx%d", cfg.Width, cfg.Height, MaxGIFSide, MaxGIFSide)
		}
		if n := countGIFFrames(data); n < 0 {
			return nil, fmt.Errorf("this GIF could not be read")
		} else if n > MaxGIFFrames {
			return nil, fmt.Errorf("this GIF has more than %d frames; shorten it first", MaxGIFFrames)
		}
		g, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("this GIF could not be read: %w", err)
		}
		return fromGIF(g, fit, bg), nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unsupported image (PNG, JPEG and GIF work): %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxPixels || cfg.Width > 1<<15 || cfg.Height > 1<<15 {
		return nil, fmt.Errorf("this picture is %dx%d pixels, which is too large (limit %d million pixels)", cfg.Width, cfg.Height, MaxPixels/1_000_000)
	}
	im, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unsupported image (PNG, JPEG and GIF work): %w", err)
	}
	return []Frame{{Scale(im, fit, bg), StillDelayMs}}, nil
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

func fromGIF(g *gif.GIF, fit Fit, bg color.Color) []Frame {
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
		out = append(out, Frame{Scale(snap, fit, bg), d})
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

// Scale scales src into a 128x128 opaque canvas over bg.
func Scale(src image.Image, fit Fit, bg color.Color) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	sb := src.Bounds()
	sw, sh := float64(sb.Dx()), float64(sb.Dy())
	srcR := [4]float64{float64(sb.Min.X), float64(sb.Min.Y), float64(sb.Max.X), float64(sb.Max.Y)}
	dr := image.Rect(0, 0, Width, Height)
	switch fit {
	case FitStretch:
	case FitContain:
		s := math.Min(Width/sw, Height/sh)
		w, h := max(1, int(math.Round(sw*s))), max(1, int(math.Round(sh*s)))
		dr = image.Rect((Width-w)/2, (Height-h)/2, (Width-w)/2+w, (Height-h)/2+h)
	default: // cover
		s := math.Max(Width/sw, Height/sh)
		cw, ch := Width/s, Height/s
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
			br, bgG, bb, _ := bgc.RGBA()
			inv := 0xFFFF - uint32(fg.A)
			dst.SetRGBA(x, y, color.RGBA{
				uint8((uint32(fg.R) + br*inv/0xFFFF) >> 8),
				uint8((uint32(fg.G) + bgG*inv/0xFFFF) >> 8),
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

// StillDelayMs is the delay given to a single picture. Nothing changes on screen, but the
// keyboard still reads a delay for it.
const StillDelayMs = 500

// FrameBytes is the size of one frame in RGB565.
const FrameBytes = Width * Height * 2

// FromRGB565LE expands one frame of little-endian RGB565 back to 8 bits per channel.
func FromRGB565LE(raw []byte) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, Width, Height))
	for j := 0; j < Width*Height; j++ {
		v := int(raw[2*j]) | int(raw[2*j+1])<<8
		im.Pix[4*j] = uint8((v >> 11) * 255 / 31)
		im.Pix[4*j+1] = uint8((v >> 5 & 63) * 255 / 63)
		im.Pix[4*j+2] = uint8((v & 31) * 255 / 31)
		im.Pix[4*j+3] = 255
	}
	return im
}

// Quantize returns the frame as the screen will show it, after the colour loss of RGB565.
func Quantize(im *image.RGBA) *image.RGBA { return FromRGB565LE(RGB565LE(im)) }
