// Package gallery holds the ready-made 128x128 animations shipped in the binary.
//
// The GIFs come from github.com/ErvinUrban-Bratislava/ak820pro-linux (MIT);
// the licence text is in gifs/LICENSE-ak820pro-linux.txt.
package gallery

import (
	"embed"
	"fmt"
)

//go:embed gifs/*.gif gifs/LICENSE-ak820pro-linux.txt
var files embed.FS

// Item describes one ready-made animation.
type Item struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Frames      int    `json:"frames"`
}

var items = []Item{
	{"ai_core", "AI Core", "A glowing core that pulses and spins.", 16},
	{"audio_visualizer", "Audio Visualizer", "Bouncing sound bars.", 12},
	{"bongo_cat", "Bongo Cat", "A cat drumming along.", 4},
	{"cyber_radar", "Cyber Radar", "A sweeping radar screen.", 12},
}

// List returns the available animations.
func List() []Item { return append([]Item(nil), items...) }

// Data returns the GIF bytes for id. Only ids from List are accepted.
func Data(id string) ([]byte, error) {
	for _, it := range items {
		if it.ID == id {
			return files.ReadFile("gifs/" + id + ".gif")
		}
	}
	return nil, fmt.Errorf("no such animation %q", id)
}
