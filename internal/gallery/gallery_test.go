package gallery

import (
	"bytes"
	"image/gif"
	"testing"
)

func TestEveryItemIsA128SquareGIFWithTheDeclaredFrameCount(t *testing.T) {
	for _, it := range List() {
		b, err := Data(it.ID)
		if err != nil {
			t.Fatal(err)
		}
		g, err := gif.DecodeAll(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", it.ID, err)
		}
		if g.Config.Width != 128 || g.Config.Height != 128 || len(g.Image) != it.Frames {
			t.Errorf("%s: %dx%d, %d frames (declared %d)", it.ID, g.Config.Width, g.Config.Height, len(g.Image), it.Frames)
		}
	}
}

func TestUnknownIDRejected(t *testing.T) {
	for _, id := range []string{"", "../x", "ai_core/../../x", "nope"} {
		if _, err := Data(id); err == nil {
			t.Errorf("Data(%q) should fail", id)
		}
	}
}
