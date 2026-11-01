package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/gallery"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/history"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/legacy"
)

type previewFrame struct {
	URL   string `json:"url"`
	Delay int    `json:"delay"`
}

type previewResponse struct {
	ID     string         `json:"id"`
	Frames []previewFrame `json:"frames"`
	Bytes  int            `json:"bytes"`
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(maxUpload); err != nil { //nolint:gosec // the body is capped by MaxBytesReader above
		fail(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, http.StatusBadRequest, errors.New("missing file"))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	// Bad numbers mean "use the default", as they do for an empty field.
	delay, _ := strconv.Atoi(r.FormValue("delay"))
	maxFrames, _ := strconv.Atoi(r.FormValue("max_frames"))
	s.makePreview(w, data, hdr.Filename, r.FormValue("fit"), r.FormValue("bg"), delay, maxFrames)
}

// makePreview decodes data and answers with exactly what the screen will show.
func (s *Server) makePreview(w http.ResponseWriter, data []byte, name, fit, bg string, delay, maxFrames int) {
	mode, err := animation.ParseFit(fit)
	if err != nil {
		mode = animation.FitCover // the page only sends known values
	}
	frames, err := animation.Decode(data, mode, animation.ParseColor(bg))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if delay > 0 {
		for i := range frames {
			frames[i].DelayMs = delay
		}
	}
	if maxFrames <= 0 || maxFrames > 255 {
		maxFrames = 255
	}
	frames = frames[:min(len(frames), maxFrames)]
	resp := previewResponse{ID: NewToken(), Bytes: len(legacy.Encode(frames))}
	for _, f := range frames {
		var b bytes.Buffer
		if err := png.Encode(&b, animation.Quantize(f.Img)); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		resp.Frames = append(resp.Frames, previewFrame{"data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()), f.DelayMs})
	}
	s.mu.Lock()
	s.previews = append(s.previews, &preview{resp.ID, name, frames})
	if len(s.previews) > maxPreview {
		s.previews = s.previews[1:]
	}
	s.mu.Unlock()
	writeJSON(w, resp)
}

func (s *Server) galleryList(w http.ResponseWriter, _ *http.Request) { writeJSON(w, gallery.List()) }

func (s *Server) galleryThumb(w http.ResponseWriter, r *http.Request) {
	data, err := gallery.Data(r.URL.Query().Get("id"))
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	frames, err := animation.Decode(data, animation.FitStretch, animation.ParseColor("black"))
	if err != nil || len(frames) == 0 {
		fail(w, http.StatusInternalServerError, errors.New("could not read animation"))
		return
	}
	writePNG(w, frames[0])
}

func writePNG(w http.ResponseWriter, f animation.Frame) {
	w.Header().Set("Content-Type", "image/png")
	if err := png.Encode(w, f.Img); err != nil {
		log.Printf("writing picture: %v", err)
	}
}

func (s *Server) galleryPreview(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID    string `json:"id"`
		Fit   string `json:"fit"`
		BG    string `json:"bg"`
		Delay int    `json:"delay"`
		Max   int    `json:"max_frames"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	data, err := gallery.Data(q.ID)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	s.makePreview(w, data, q.ID+".gif", q.Fit, q.BG, q.Delay, q.Max)
}

func (s *Server) findPreview(id string) *preview {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.previews {
		if p.id == id {
			return p
		}
	}
	return nil
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID  string `json:"previewId"`
		Dry bool   `json:"dry"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	p := s.findPreview(q.ID)
	if p == nil {
		fail(w, http.StatusNotFound, errors.New("preview expired; choose the image again"))
		return
	}
	s.send(w, r, p.frames, p.name, q.Dry)
}

type sendEvent struct {
	Done    int    `json:"done,omitempty"`
	Total   int    `json:"total,omitempty"`
	Event   string `json:"event,omitempty"`
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
}

// send answers a dry run with JSON and a real send with a stream of progress events.
// If the browser goes away the transfer stops, and an unfinished one is never committed.
func (s *Server) send(w http.ResponseWriter, r *http.Request, frames []animation.Frame, name string, dry bool) {
	if !dry && !s.checkConnected(w) {
		return
	}
	drv, err := s.driver()
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	plan, err := drv.PlanUpload(frames)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if dry {
		writeJSON(w, newSendResponse(plan, true))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)
	emit := func(e sendEvent) {
		b, err := json.Marshal(e)
		if err != nil {
			log.Printf("encoding event: %v", err)
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return // the browser went away; the request context stops the transfer
		}
		if err := rc.Flush(); err != nil {
			log.Printf("flushing event: %v", err)
		}
	}
	if err := plan.Run(r.Context(), func(d, n int) { emit(sendEvent{Done: d, Total: n}) }); err != nil {
		emit(sendEvent{Event: "error", Error: err.Error()})
		return
	}
	done := sendEvent{Event: "done"}
	if err := history.Save(s.HistoryDir, frames, name); err != nil {
		log.Printf("saving history: %v", err)
		done.Warning = "The picture was sent, but could not be added to your recent pictures."
	}
	emit(done)
}
