package legacy

import (
	"fmt"
	"math"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
)

const headerSize = 256

// Encode builds the animation payload: a 256-byte 0xFF-filled header (frame count,
// the delay of frames 0..N-2 in 2 ms units, then a 0) followed by the frames.
// Delays are capped at 510 ms by the format.
func Encode(frames []animation.Frame) []byte {
	n := len(frames)
	out := make([]byte, headerSize, headerSize+n*animation.FrameBytes)
	for i := range out {
		out[i] = 0xFF
	}
	out[0] = byte(n)
	for k := 0; k < n-1; k++ {
		out[k+1] = byte(min(255, max(1, int(math.Round(float64(frames[k].DelayMs)/2)))))
	}
	out[n] = 0
	for _, f := range frames {
		out = append(out, animation.RGB565LE(f.Img)...)
	}
	return out
}

// Decode is the inverse of Encode.
func Decode(p []byte) ([]animation.Frame, error) {
	if len(p) <= headerSize || p[0] == 0 {
		return nil, fmt.Errorf("payload too short")
	}
	n := int(p[0])
	if need := headerSize + n*animation.FrameBytes; len(p) < need {
		return nil, fmt.Errorf("payload truncated: %d frames need %d bytes, have %d", n, need, len(p))
	}
	out := make([]animation.Frame, 0, n)
	for k := 0; k < n; k++ {
		raw := p[headerSize+k*animation.FrameBytes : headerSize+(k+1)*animation.FrameBytes]
		d := 500
		if n > 1 {
			d = int(p[min(k+1, n-1)]) * 2
		}
		out = append(out, animation.Frame{Img: animation.FromRGB565LE(raw), DelayMs: d})
	}
	return out, nil
}
