package main

import (
	"image"
	"image/png"
	"os"
)

func savePNG(path string, im *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, im)
}
