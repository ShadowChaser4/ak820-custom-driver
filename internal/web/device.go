package web

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/legacy"
)

// Level is how a status should look.
type Level string

// The status levels.
const (
	LevelOK   Level = "ok"
	LevelWarn Level = "warn"
	LevelBad  Level = "bad"
)

// Friendly is the device state in plain language.
type Friendly struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Level   Level  `json:"level"`
}

// describe turns the device state into words for people who are not technical.
func describe(ifaces []hidraw.Info, err error, layout keyboard.Layout, ready bool) Friendly {
	var multi *keyboard.MultipleError
	if errors.As(err, &multi) {
		return Friendly{"More than one keyboard found",
			fmt.Sprintf("%d keyboards are plugged in. Unplug all but one, or start this program with --device 1 to pick one.", multi.N), LevelBad}
	}
	if err != nil || len(ifaces) == 0 {
		return Friendly{"Keyboard not found",
			"Plug the keyboard into this computer with the USB-C cable and slide the switch on the back to USB. Bluetooth and the 2.4G dongle cannot control the screen.", LevelBad}
	}
	for _, i := range ifaces {
		if !i.Accessible() {
			return Friendly{"The keyboard is there, but this program is not allowed to use it",
				"This is a permission setting on your computer. Open a terminal, run \"ak820 setup\" and follow the steps, then unplug and replug the keyboard.", LevelBad}
		}
	}
	if layout == keyboard.LayoutUnknown {
		return Friendly{"This keyboard version is not supported yet",
			"The keyboard was found, but it talks in a way this program does not know. Open Advanced, copy the report and paste it into a new issue on the project page so support can be added.", LevelWarn}
	}
	if ready {
		return Friendly{"Connected", "Your keyboard is ready. Pick a picture below.", LevelOK}
	}
	return Friendly{"Keyboard found", "Click \"Connect & set the keyboard clock\" to start. This also tests that the connection works.", LevelWarn}
}

type nodeRef struct {
	Node string `json:"node"`
	Len  int    `json:"len"`
}

type sessionConfig struct {
	Cmd  nodeRef `json:"cmd"`
	Data nodeRef `json:"data"`
}

type features struct {
	Clock   bool `json:"clock"`
	Builtin bool `json:"builtin"`
	Flash   bool `json:"flash"`
}

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

type devicesResponse struct {
	Config     sessionConfig   `json:"config"`
	Version    string          `json:"version"`
	Friendly   Friendly        `json:"friendly"`
	Ready      bool            `json:"ready"`
	Profile    keyboard.Layout `json:"profile"`
	Layout     keyboard.Layout `json:"layout"`
	Features   features        `json:"features"`
	Interfaces []ifaceDTO      `json:"interfaces,omitempty"`
	Error      string          `json:"error,omitempty"`
}

func (s *Server) devices(w http.ResponseWriter, _ *http.Request) {
	ifaces, err := s.Finder.Interfaces()
	s.mu.Lock()
	resp := devicesResponse{Version: s.Version, Config: sessionConfig{Cmd: nodeRef{Node: s.connected}, Data: nodeRef{Node: s.dataNode}}}
	s.mu.Unlock()
	if err != nil {
		resp.Error = err.Error()
		resp.Friendly = describe(nil, err, keyboard.LayoutUnknown, false)
		writeJSON(w, resp)
		return
	}
	for _, i := range ifaces {
		d := i.Descriptor
		resp.Interfaces = append(resp.Interfaces, ifaceDTO{
			Node: i.Node, Iface: i.Iface, VID: fmt.Sprintf("%04x", i.VID), PID: fmt.Sprintf("%04x", i.PID), Name: i.Name,
			Page: fmt.Sprintf("0x%04X", d.Page), In: d.InBytes(), Out: d.OutBytes(), Feature: d.FeatBytes(), ReportIDs: d.ReportIDs,
			RW: i.Accessible(), Vendor: d.Vendor(), Descriptor: fmt.Sprintf("% x", i.Raw),
		})
	}
	resp.Ready = s.ready(ifaces)
	if drv, err := s.Finder.Driver(ifaces, keyboard.Override{}); err == nil {
		c := drv.Capabilities()
		resp.Layout, resp.Profile = drv.Layout(), drv.Layout()
		resp.Features = features{Clock: c.Clock, Builtin: c.Builtin, Flash: c.Persistent}
	}
	resp.Friendly = describe(ifaces, nil, resp.Layout, resp.Ready)
	writeJSON(w, resp)
}

func (s *Server) report(w http.ResponseWriter, _ *http.Request) {
	ifaces, err := s.Finder.Interfaces()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, struct {
		Text string `json:"text"`
	}{keyboard.Report(ifaces)})
}

func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Dry     bool `json:"dry"`
		Feature bool `json:"feature"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ifaces, err := s.Finder.Interfaces()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	p, err := legacy.NewProbe(ifaces, s.Finder.Deps(), q.Feature)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if q.Dry {
		writeJSON(w, p.Preview())
		return
	}
	res, err := p.Run(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, res)
}

type infoResponse struct {
	Dry    bool         `json:"dry"`
	Node   string       `json:"node"`
	Len    int          `json:"len"`
	Frames []string     `json:"frames"`
	Info   *legacy.Info `json:"info,omitempty"`
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Dry  bool   `json:"dry"`
		Node string `json:"node"`
		Len  int    `json:"len"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ifaces, err := s.Finder.Interfaces()
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	drv, err := s.Finder.Driver(ifaces, keyboard.Override{Node: q.Node, Len: q.Len})
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	ld, ok := drv.(*legacy.Driver)
	if !ok {
		fail(w, http.StatusBadGateway, keyboard.ErrNoDeviceInfo)
		return
	}
	plan, err := ld.PlanInfo()
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	d := plan.Describe()
	resp := infoResponse{Dry: q.Dry, Node: d.Node, Len: d.ReportSize, Frames: d.Steps}
	if !q.Dry {
		if err := plan.Run(r.Context(), nil); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		resp.Info = &plan.Info
		s.markConnected(d.Node)
	}
	writeJSON(w, resp)
}

// target sets the upload interface for this session only. No device I/O.
func (s *Server) target(w http.ResponseWriter, r *http.Request) {
	var q nodeRef
	if err := readJSON(r, &q); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	ifaces, err := s.Finder.Interfaces()
	if err == nil && q.Node != "" {
		_, err = keyboard.Find(ifaces, q.Node)
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dataNode = q.Node
	writeJSON(w, sessionConfig{Cmd: nodeRef{Node: s.connected}, Data: nodeRef{Node: s.dataNode}})
}

// sendResponse describes a plan, in the shape the page expects.
type sendResponse struct {
	Dry     bool     `json:"dry"`
	Node    string   `json:"node"`
	Len     int      `json:"len"`
	Packets int      `json:"packets"`
	First   string   `json:"first"`
	Steps   []string `json:"steps,omitempty"`
	Flash   bool     `json:"flash"`
}

func newSendResponse(p keyboard.Plan, dry bool) sendResponse {
	d := p.Describe()
	return sendResponse{Dry: dry, Node: d.Node, Len: d.ReportSize, Packets: d.Packets, First: d.First, Steps: d.Steps, Flash: d.Persistent}
}

func (s *Server) builtin(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Index int  `json:"index"`
		Dry   bool `json:"dry"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if !q.Dry && !s.checkConnected(w) {
		return
	}
	drv, err := s.driver()
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	plan, err := drv.PlanBuiltin(q.Index)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, keyboard.ErrUnsupported) {
			code = http.StatusNotImplemented
		}
		fail(w, code, err)
		return
	}
	if !q.Dry {
		if err := plan.Run(r.Context(), nil); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
	}
	writeJSON(w, newSendResponse(plan, q.Dry))
}

// setTime is the Connect step: setting the clock tests the control channel. It must
// not require a connection itself, or the Connect button could never succeed.
func (s *Server) setTime(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Dry bool `json:"dry"`
	}
	if err := readJSON(r, &q); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	drv, err := s.driver()
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	plan, err := drv.PlanClock(time.Now())
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	if !q.Dry {
		if err := plan.Run(r.Context(), nil); err != nil {
			fail(w, http.StatusBadGateway, err)
			return
		}
		s.markConnected(plan.Describe().Node)
	}
	writeJSON(w, newSendResponse(plan, q.Dry))
}
