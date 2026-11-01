// Package web is the local web UI. It binds to loopback only, requires a random
// token on every /api call and rejects foreign Host and Origin headers.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
)

//go:embed static
var staticFS embed.FS

const (
	maxUpload  = 20 << 20
	maxPreview = 8
)

// Server serves the page and its API. Use one per process.
type Server struct {
	Finder       *keyboard.Finder
	HistoryDir   string
	Token        string
	Version      string   // shown in the page footer
	AllowedHosts []string // e.g. "127.0.0.1:8080"

	mu sync.Mutex
	// Session state, in memory only: /dev/hidrawN numbers change on every replug.
	connected string // node whose control channel passed the Connect step
	dataNode  string // upload interface chosen under Advanced
	previews  []*preview
}

type preview struct {
	id     string
	name   string
	frames []animation.Frame
}

// NewToken returns a random access token.
func NewToken() string { return rand.Text() }

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Panicf("embedded page: %v", err) // cannot happen: "static" is embedded above
	}
	mux.Handle("GET /", http.FileServer(http.FS(static)))

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

// guard enforces the Host, Origin and token checks.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(s.AllowedHosts, r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			got := r.Header.Get("X-Token")
			if s.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				http.Error(w, "bad token", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}

type errorResponse struct {
	Error string `json:"error"`
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if e := json.NewEncoder(w).Encode(errorResponse{err.Error()}); e != nil {
		log.Printf("writing response: %v", e)
	}
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ready reports whether Connect succeeded in this session on a node that still exists.
func (s *Server) ready(ifaces []hidraw.Info) bool {
	s.mu.Lock()
	node := s.connected
	s.mu.Unlock()
	if node == "" {
		return false
	}
	_, err := keyboard.Find(ifaces, node)
	return err == nil
}

// checkConnected refuses real writes before Connect, answering the request if so.
func (s *Server) checkConnected(w http.ResponseWriter) bool {
	ifaces, err := s.Finder.Interfaces()
	if err == nil && s.ready(ifaces) {
		return true
	}
	fail(w, http.StatusConflict, errors.New("the keyboard is not connected yet: use the Connect button first"))
	return false
}

func (s *Server) markConnected(node string) {
	s.mu.Lock()
	s.connected = node
	s.mu.Unlock()
}

// driver returns the driver for the current keyboard and this session's choices.
func (s *Server) driver() (keyboard.Driver, error) {
	ifaces, err := s.Finder.Interfaces()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	o := keyboard.Override{DataNode: s.dataNode}
	s.mu.Unlock()
	return s.Finder.Driver(ifaces, o)
}
