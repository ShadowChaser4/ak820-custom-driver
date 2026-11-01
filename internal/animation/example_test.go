package animation_test

import (
	"fmt"
	"image"
	"image/color"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
)

func ExampleScale() {
	wide := image.NewRGBA(image.Rect(0, 0, 300, 100))
	out := animation.Scale(wide, animation.FitContain, color.Black)
	fmt.Println(out.Bounds().Dx(), out.Bounds().Dy())
	// Output: 128 128
}

func ExampleParseFit() {
	fit, err := animation.ParseFit("contain")
	fmt.Println(fit == animation.FitContain, err)
	// Output: true <nil>
}
