// Package web is the local web UI. It binds to loopback only, requires a
// random token on every /api call and rejects foreign Host/Origin headers.
package web

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"sync"
	"time"

	"ak820/internal/core"
	"ak820/internal/gallery"
	"ak820/internal/hidraw"
	"ak820/internal/history"
	"ak820/internal/img"
)

//go:embed static
var staticFS embed.FS

const (
	maxUpload  = 20 << 20
	maxPreview = 8
)

type preview struct {
	ID      string
	Name    string
	Payload []byte
}

type Server struct {
	Core         *core.Core
	Token        string
	AllowedHosts []string // e.g. "127.0.0.1:8080"

	mu       sync.Mutex
	previews []*preview
}

func NewToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServer(http.FS(sub)))

	mux.HandleFunc("GET /api/devices", s.devices)
	mux.HandleFunc("GET /api/report", s.report)
	mux.HandleFunc("POST /api/probe", s.probe)
	mux.HandleFunc("POST /api/info", s.info)
	mux.HandleFunc("POST /api/target", s.target)
	mux.HandleFunc("POST /api/preview", s.preview)
	mux.HandleFunc("GET /api/gallery", s.galleryList)
	mux.HandleFunc("GET /api/gallery/thumb", s.galleryThumb)
	mux.HandleFunc("POST /api/gallery/preview", s.galleryPreview)
	mux.HandleFunc("POST /api/upload", s.upload)
	mux.HandleFunc("GET /api/history", s.histList)
	mux.HandleFunc("GET /api/history/gif", s.histGIF)
	mux.HandleFunc("GET /api/history/thumb", s.histThumb)
	mux.HandleFunc("POST /api/history/restore", s.histRestore)
	mux.HandleFunc("POST /api/builtin", s.builtin)
	mux.HandleFunc("POST /api/time", s.setTime)
	return s.guard(mux)
}

// guard enforces Host / Origin / token checks.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostOK(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			got := r.Header.Get("X-Token")
			if s.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				http.Error(w, "bad token", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostOK(h string) bool {
	for _, a := range s.AllowedHosts {
		if h == a {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// confirmed reports whether a command target has answered GET_DEVICE_INFO.
// Real writes (except probe/info) are refused until then.
func (s *Server) confirmed() bool {
	all, err := s.Core.Devices()
	return err == nil && s.Core.Ready(all)
}

func needConfirmed(w http.ResponseWriter) {
	fail(w, http.StatusConflict, fmt.Errorf("the keyboard is not connected yet: use the Connect button first"))
}

// ---- device -------------------------------------------------------------------

type ifaceDTO struct {
	Node       string `json:"node"`
	Iface      int    `json:"iface"`
	VID        string `json:"vid"`
	PID        string `json:"pid"`
	Name       string `json:"name"`
	Page       string `json:"page"`
	In         int    `json:"in"`
	Out        int    `json:"out"`
	Feature    int    `json:"feature"`
	ReportIDs  bool   `json:"reportIds"`
	RW         bool   `json:"rw"`
	Vendor     bool   `json:"vendor"`
	Descriptor string `json:"descriptor"`
}

func dto(i hidraw.Info) ifaceDTO {
	return ifaceDTO{i.Node, i.Iface, fmt.Sprintf("%04x", i.VID), fmt.Sprintf("%04x", i.PID), i.Name,
		fmt.Sprintf("0x%04X", i.D.Page), i.D.InBytes(), i.D.OutBytes(), i.D.FeatBytes(), i.D.ReportIDs,
		i.Accessible(), i.D.Vendor(), fmt.Sprintf("% x", i.Raw)}
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	all, err := s.Core.Devices()
	resp := map[string]any{"config": s.Core.Config(), "friendly": s.Core.Describe(all, err)}
	if err != nil {
		resp["error"] = err.Error()
		resp["ready"], resp["profile"], resp["layout"] = false, "unknown", "unknown"
		resp["features"] = map[string]bool{"clock": false, "builtin": false, "flash": false}
		writeJSON(w, resp)
		return
	}
	var d []ifaceDTO
	for _, i := range all {
		d = append(d, dto(i))
	}
	prof := core.Layout(all)
	resp["interfaces"], resp["layout"], resp["profile"] = d, prof, prof
	resp["ready"] = s.Core.Ready(all)
	resp["features"] = map[string]bool{"clock": prof != "unknown", "builtin": prof == "legacy", "flash": prof == "sonix"}
	writeJSON(w, resp)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	all, err := s.Core.Devices()
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, map[string]string{"text": core.Report(all)})
}

func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Dry     bool `json:"dry"`
		Feature bool `json:"feature"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	res, err := s.Core.Probe(q.Dry, q.Feature)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Dry  bool   `json:"dry"`
		Node string `json:"node"`
		Len  int    `json:"len"`
		Save bool   `json:"save"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	res, err := s.Core.Info(core.Target{Node: q.Node, Len: q.Len}, q.Dry, q.Save)
	if err != nil {
		fail(w, 502, err)
		return
	}
	writeJSON(w, res)
}

// target sets the upload (data) interface. No device I/O.
func (s *Server) target(w http.ResponseWriter, r *http.Request) {
	var q core.Target
	if err := readJSON(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	all, err := s.Core.Devices()
	if err == nil && q.Node != "" {
		_, err = core.Find(all, q.Node)
	}
	if err != nil {
		fail(w, 400, err)
		return
	}
	cf := s.Core.Config()
	cf.Data = q
	if err := s.Core.SaveConfig(cf); err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, cf)
}

// ---- preview / upload -----------------------------------------------------------

func dataURL(im *img.Frame) string {
	var b bytes.Buffer
	_ = png.Encode(&b, im.Img)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		fail(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, fmt.Errorf("missing file"))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		fail(w, 400, err)
		return
	}
	delay, _ := strconv.Atoi(r.FormValue("delay"))
	mf, _ := strconv.Atoi(r.FormValue("max_frames"))
	s.makePreview(w, data, hdr.Filename, r.FormValue("fit"), r.FormValue("bg"), delay, mf)
}

// makePreview decodes data, builds the payload and answers with exactly the
// frames that will be sent (decoded back from the payload).
func (s *Server) makePreview(w http.ResponseWriter, data []byte, name, fit, bg string, delay, mf int) {
	if fit != "contain" && fit != "stretch" {
		fit = "cover"
	}
	frames, err := img.Decode(data, fit, img.ParseColor(bg))
	if err != nil {
		fail(w, 400, err)
		return
	}
	if delay > 0 {
		for i := range frames {
			frames[i].DelayMs = delay
		}
	}
	if mf <= 0 || mf > 255 {
		mf = 255
	}
	if len(frames) > mf {
		frames = frames[:mf]
	}
	payload := img.BuildPayload(frames)
	back, err := img.ParsePayload(payload)
	if err != nil {
		fail(w, 500, err)
		return
	}
	type fr struct {
		URL   string `json:"url"`
		Delay int    `json:"delay"`
	}
	var out []fr
	for i := range back {
		out = append(out, fr{dataURL(&back[i]), back[i].DelayMs})
	}
	id := NewToken()[:16]
	s.mu.Lock()
	s.previews = append(s.previews, &preview{id, name, payload})
	if len(s.previews) > maxPreview {
		s.previews = s.previews[1:]
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"id": id, "frames": out, "bytes": len(payload)})
}

// ---- gallery ------------------------------------------------------------------------

func (s *Server) galleryList(w http.ResponseWriter, r *http.Request) { writeJSON(w, gallery.List()) }

func (s *Server) galleryThumb(w http.ResponseWriter, r *http.Request) {
	data, err := gallery.Data(r.URL.Query().Get("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}
	fr, err := img.Decode(data, "stretch", img.ParseColor("black"))
	if err != nil || len(fr) == 0 {
		fail(w, 500, fmt.Errorf("could not read animation"))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_ = png.Encode(w, fr[0].Img)
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
		fail(w, 400, err)
		return
	}
	data, err := gallery.Data(q.ID)
	if err != nil {
		fail(w, 404, err)
		return
	}
	s.makePreview(w, data, q.ID+".gif", q.Fit, q.BG, q.Delay, q.Max)
}

func (s *Server) getPreview(id string) *preview {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.previews {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// send streams progress as SSE for a real send, or returns JSON for a dry run.
func (s *Server) send(w http.ResponseWriter, payload []byte, name string, dry bool, save bool) {
	if dry {
		res, err := s.Core.Upload(core.Target{}, payload, true, nil)
		if err != nil {
			fail(w, 502, err)
			return
		}
		writeJSON(w, res)
		return
	}
	if !s.confirmed() {
		needConfirmed(w)
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ev := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
	}
	_, err := s.Core.Upload(core.Target{}, payload, false, func(d, n int) {
		ev(map[string]any{"done": d, "total": n})
	})
	if err != nil {
		ev(map[string]any{"event": "error", "error": err.Error()})
		return
	}
	if save {
		_ = history.Save(s.Core.HistoryDir(), payload, name)
	}
	ev(map[string]any{"event": "done"})
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID  string `json:"previewId"`
		Dry bool   `json:"dry"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	p := s.getPreview(q.ID)
	if p == nil {
		fail(w, 404, fmt.Errorf("preview expired; choose the image again"))
		return
	}
	s.send(w, p.Payload, p.Name, q.Dry, true)
}

// ---- history ----------------------------------------------------------------------

func (s *Server) histList(w http.ResponseWriter, r *http.Request) {
	es, err := history.List(s.Core.HistoryDir())
	if err != nil {
		fail(w, 500, err)
		return
	}
	if es == nil {
		es = []history.Entry{}
	}
	writeJSON(w, es)
}

func (s *Server) histFrames(w http.ResponseWriter, r *http.Request) ([]byte, []img.Frame, bool) {
	p, err := history.Load(s.Core.HistoryDir(), r.URL.Query().Get("id"))
	if err != nil {
		fail(w, 404, err)
		return nil, nil, false
	}
	fr, err := img.ParsePayload(p)
	if err != nil {
		fail(w, 500, err)
		return nil, nil, false
	}
	return p, fr, true
}

func (s *Server) histThumb(w http.ResponseWriter, r *http.Request) {
	_, fr, ok := s.histFrames(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_ = png.Encode(w, fr[0].Img)
}

func (s *Server) histGIF(w http.ResponseWriter, r *http.Request) {
	_, fr, ok := s.histFrames(w, r)
	if !ok {
		return
	}
	g := &gif.GIF{LoopCount: 0}
	for _, f := range fr {
		big := img.Scaled(f.Img, 2)
		pm := imagePaletted(big)
		g.Image = append(g.Image, pm)
		g.Delay = append(g.Delay, max(1, f.DelayMs/10))
	}
	w.Header().Set("Content-Type", "image/gif")
	_ = gif.EncodeAll(w, g)
}

func (s *Server) histRestore(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID  string `json:"id"`
		Dry bool   `json:"dry"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	p, err := history.Load(s.Core.HistoryDir(), q.ID)
	if err != nil {
		fail(w, 404, err)
		return
	}
	s.send(w, p, "restored", q.Dry, true)
}

// ---- builtin / time ------------------------------------------------------------------

func (s *Server) builtin(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Index int  `json:"index"`
		Dry   bool `json:"dry"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if !q.Dry && !s.confirmed() {
		needConfirmed(w)
		return
	}
	res, err := s.Core.Builtin(core.Target{}, q.Index, q.Dry)
	if err != nil {
		code := 502
		if errors.Is(err, core.ErrUnsupported) {
			code = http.StatusNotImplemented
		}
		fail(w, code, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) setTime(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Dry bool `json:"dry"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if !q.Dry && !s.confirmed() {
		needConfirmed(w)
		return
	}
	res, err := s.Core.SetTime(core.Target{}, time.Now(), q.Dry)
	if err != nil {
		fail(w, 502, err)
		return
	}
	writeJSON(w, res)
}

func imagePaletted(im *image.RGBA) *image.Paletted {
	p := image.NewPaletted(im.Bounds(), palette.Plan9)
	draw.FloydSteinberg.Draw(p, p.Bounds(), im, image.Point{})
	return p
}
