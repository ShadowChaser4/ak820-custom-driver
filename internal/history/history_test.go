package history

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
)

func frames(n int) []animation.Frame {
	out := make([]animation.Frame, n)
	for i := range out {
		out[i] = animation.Frame{Img: image.NewRGBA(image.Rect(0, 0, animation.Width, animation.Height)), DelayMs: 100}
	}
	return out
}

func TestSaveListLoad(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, frames(3), "../evil name?.png"); err != nil {
		t.Fatal(err)
	}
	es, err := List(dir)
	if err != nil || len(es) != 1 || es[0].Frames != 3 {
		t.Fatalf("%v %+v", err, es)
	}
	if es[0].ID != filepath.Base(es[0].ID) {
		t.Fatal("id must be a plain file name")
	}
	if fr, err := Load(dir, es[0].ID); err != nil || len(fr) != 3 {
		t.Fatalf("%v %d", err, len(fr))
	}
}

func TestLoadRejectsPaths(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "secret.bin")
	os.WriteFile(outside, []byte("x"), 0o644)
	defer os.Remove(outside)
	for _, id := range []string{"../secret.bin", "/etc/passwd", "nope.bin", ""} {
		if _, err := Load(dir, id); err == nil {
			t.Errorf("Load(%q) should fail", id)
		}
	}
}

func TestPruneToMax(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < Max+3; i++ {
		os.WriteFile(filepath.Join(dir, "20200101-0000"+string(rune('0'+i/10))+string(rune('0'+i%10))+"_a.bin"), []byte{1}, 0o644)
	}
	if err := Save(dir, frames(1), "new"); err != nil {
		t.Fatal(err)
	}
	if es, _ := List(dir); len(es) != Max {
		t.Fatalf("got %d entries", len(es))
	}
}
