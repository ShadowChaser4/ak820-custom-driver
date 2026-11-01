package web

import (
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"log"
	"net/http"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/history"
)

func (s *Server) histList(w http.ResponseWriter, _ *http.Request) {
	entries, err := history.List(s.HistoryDir)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if entries == nil {
		entries = []history.Entry{}
	}
	writeJSON(w, entries)
}

func (s *Server) histFrames(w http.ResponseWriter, r *http.Request) ([]animation.Frame, bool) {
	frames, err := history.Load(s.HistoryDir, r.URL.Query().Get("id"))
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return nil, false
	}
	return frames, true
}

func (s *Server) histThumb(w http.ResponseWriter, r *http.Request) {
	if frames, ok := s.histFrames(w, r); ok {
		writePNG(w, frames[0])
	}
}

func (s *Server) histGIF(w http.ResponseWriter, r *http.Request) {
	frames, ok := s.histFrames(w, r)
	if !ok {
		return
	}
	g := &gif.GIF{}
	for _, f := range frames {
		big := animation.Scaled(f.Img, 2)
		pal := image.NewPaletted(big.Bounds(), palette.Plan9)
		draw.FloydSteinberg.Draw(pal, pal.Bounds(), big, image.Point{})
		g.Image = append(g.Image, pal)
		g.Delay = append(g.Delay, max(1, f.DelayMs/10))
	}
	w.Header().Set("Content-Type", "image/gif")
	if err := gif.EncodeAll(w, g); err != nil {
		log.Printf("writing GIF: %v", err)
	}
}

func (s *Server) histRestore(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID  string `json:"id"`
		Dry bool   `json:"dry"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	frames, err := history.Load(s.HistoryDir, q.ID)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	s.send(w, r, frames, "restored", q.Dry)
}
