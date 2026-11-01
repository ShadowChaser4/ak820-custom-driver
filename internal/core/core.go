// Package core holds the device logic shared by the CLI and the web UI.
// Every function that can write to the keyboard takes a dry flag; with
// dry=true nothing is opened or written and the exact frames are returned.
package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ak820/internal/hidraw"
	"ak820/internal/proto"
	"ak820/internal/sonix"
)

const VendorID = 0x0C45

// DevIO is what core needs from an open node (hidraw.Dev, or a fake in tests).
type DevIO interface {
	proto.Conn
	GetFeature(id byte, n int) ([]byte, error)
	SetFeature(payload []byte, n int) error
	Close() error
}

type Core struct {
	mu        sync.Mutex // serialises device access
	Enumerate func() ([]hidraw.Info, error)
	OpenDev   func(hidraw.Info) (DevIO, error)
	DataDir   string // only the upload history is stored on disk
	Device    int    // 1-based keyboard to use when several are plugged in (0 = require exactly one)

	// In-memory session. Nothing about device nodes is ever written to disk:
	// /dev/hidrawN numbers change on every replug and differ between machines.
	smu       sync.Mutex
	connected string // node whose control channel passed the connection check in this process
	dataNode  string // optional upload-interface override chosen in the UI
}

func New() *Core {
	return &Core{
		Enumerate: func() ([]hidraw.Info, error) { return hidraw.Enumerate(VendorID) },
		OpenDev: func(i hidraw.Info) (DevIO, error) {
			d, err := hidraw.Open(i)
			if err != nil {
				return nil, err
			}
			return d, nil
		},
		DataDir: filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "ak820"),
	}
}

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, fallback)
}

func (c *Core) HistoryDir() string { return filepath.Join(c.DataDir, "history") }

var ErrNoDevice = errors.New("no 0c45:* hidraw device found (USB mode, cable plugged in? the 2.4G dongle and BT can't reach the screen)")

// ---- discovery ---------------------------------------------------------------

// SupportedPIDs are the products this tool drives. 0x0c45 is a vendor id shared
// by many unrelated devices, and the 2.4G dongle (0xfefe) cannot reach the screen.
var SupportedPIDs = map[uint16]bool{0x800A: true, 0x8009: true}

// ErrMultiple is returned when several keyboards are connected and none was chosen.
type ErrMultiple struct{ N int }

func (e ErrMultiple) Error() string {
	return fmt.Sprintf("%d AK820 keyboards are connected. Unplug all but one, or choose one with --device 1 ... --device %d.", e.N, e.N)
}

// Devices returns the interfaces of the one keyboard to use.
func (c *Core) Devices() ([]hidraw.Info, error) {
	all, err := c.Enumerate()
	if err != nil {
		return nil, err
	}
	var ours []hidraw.Info
	for _, i := range all {
		if SupportedPIDs[i.PID] {
			ours = append(ours, i)
		}
	}
	groups := hidraw.Groups(ours)
	switch {
	case len(groups) == 0:
		return nil, ErrNoDevice
	case c.Device > 0:
		if c.Device > len(groups) {
			return nil, fmt.Errorf("--device %d: only %d keyboard(s) found", c.Device, len(groups))
		}
		return groups[c.Device-1], nil
	case len(groups) > 1:
		return nil, ErrMultiple{len(groups)}
	}
	return groups[0], nil
}

func Vendor(all []hidraw.Info) []hidraw.Info {
	var v []hidraw.Info
	for _, i := range all {
		if i.D.Vendor() {
			v = append(v, i)
		}
	}
	return v
}

func Find(all []hidraw.Info, node string) (hidraw.Info, error) {
	for _, i := range all {
		if i.Node == node || strings.TrimPrefix(i.Node, "/dev/") == node {
			return i, nil
		}
	}
	return hidraw.Info{}, fmt.Errorf("%s is not a 0c45 hidraw node", node)
}

// LegacyLayout returns the interfaces fromagge/ak820-screen expects:
// config = first FF68-family page, bulk = page 0xFF67. Both nil if no FF67.
func LegacyLayout(all []hidraw.Info) (cfg, bulk *hidraw.Info) {
	pages := map[uint32]bool{0xFF68: true, 0xFF80: true, 0xFF60: true, 0xFF00: true, 0xFF01: true, 0xFF1B: true}
	for k := range all {
		i := &all[k]
		if i.D.Page == 0xFF67 {
			bulk = i
		} else if pages[i.D.Page] && cfg == nil {
			cfg = i
		}
	}
	if bulk == nil {
		return nil, nil
	}
	return
}

// SonixLayout returns the (control, data) interfaces of the feature-report
// protocol: control = page 0xFF13 with a 64-byte feature report, data = page
// 0xFF68 with a 4096-byte output report.
func SonixLayout(all []hidraw.Info) (ctrl, data *hidraw.Info) {
	for k := range all {
		i := &all[k]
		switch {
		case i.D.Page == 0xFF13 && i.D.FeatBytes() == 64 && ctrl == nil:
			ctrl = i
		case i.D.Page == 0xFF68 && i.D.OutBytes() == sonix.ChunkSize && data == nil:
			data = i
		}
	}
	if ctrl == nil || data == nil {
		return nil, nil
	}
	return
}

// Layout names the protocol profile: "sonix", "legacy" or "unknown".
func Layout(all []hidraw.Info) string {
	if c, d := SonixLayout(all); c != nil && d != nil {
		return "sonix"
	}
	if cfg, bulk := LegacyLayout(all); cfg != nil && bulk != nil {
		return "legacy"
	}
	return "unknown"
}

var ErrUnsupported = errors.New("this keyboard does not support that")

// MarkConnected records that the control channel on node answered in this
// process. It lives in memory only.
func (c *Core) MarkConnected(node string) {
	c.smu.Lock()
	c.connected = node
	c.smu.Unlock()
}

// SetDataNode overrides the upload interface for this session ("" clears it).
func (c *Core) SetDataNode(node string) {
	c.smu.Lock()
	c.dataNode = node
	c.smu.Unlock()
}

// Ready reports whether the keyboard was connected in this process AND that
// node still exists (hidraw numbers change when the cable is replugged).
func (c *Core) Ready(all []hidraw.Info) bool {
	c.smu.Lock()
	n := c.connected
	c.smu.Unlock()
	if n == "" {
		return false
	}
	_, err := Find(all, n)
	return err == nil
}

type Friendly struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Level   string `json:"level"` // ok | warn | bad
}

// Describe turns the device state into plain language for non-technical users.
func (c *Core) Describe(all []hidraw.Info, err error) Friendly {
	if err != nil || len(all) == 0 {
		return Friendly{"Keyboard not found",
			"Plug the keyboard into this computer with the USB-C cable and slide the switch on the back to USB. Bluetooth and the 2.4G dongle cannot control the screen.", "bad"}
	}
	for _, i := range all {
		if !i.Accessible() {
			return Friendly{"The keyboard is there, but this program is not allowed to use it",
				"This is a permission setting on your computer. Install the included permission file (udev/70-ak820.rules), then unplug and replug the keyboard.", "bad"}
		}
	}
	switch Layout(all) {
	case "unknown":
		return Friendly{"This keyboard version is not supported yet",
			"The keyboard was found, but it talks in a way this program does not know. Open Advanced and copy the report to share it with the developers.", "warn"}
	}
	if c.Ready(all) {
		return Friendly{"Connected", "Your keyboard is ready. Pick a picture below.", "ok"}
	}
	return Friendly{"Keyboard found", "Click \"Connect & set the keyboard clock\" to start. This also tests that the connection works.", "warn"}
}

// ---- targets -----------------------------------------------------------------

type Target struct {
	Node string `json:"node"`
	Len  int    `json:"len"`
}

// Config is the in-memory session state, shaped for the UI.
type Config struct {
	Cmd  Target `json:"cmd"`  // node connected in this session (Len unused)
	Data Target `json:"data"` // upload-interface override, if any
}

func (c *Core) State() Config {
	c.smu.Lock()
	defer c.smu.Unlock()
	return Config{Cmd: Target{Node: c.connected}, Data: Target{Node: c.dataNode}}
}

// Resolve picks the interface and report length for role "cmd" or "data".
// Order: explicit t (a missing explicit node is an error), the session's
// upload override, then layout detection on the current enumeration.
func (c *Core) Resolve(all []hidraw.Info, role string, t Target) (hidraw.Info, int, error) {
	if t.Node == "" && role == "data" {
		c.smu.Lock()
		o := c.dataNode
		c.smu.Unlock()
		if o != "" {
			if _, err := Find(all, o); err == nil { // a stale override is ignored
				t = Target{Node: o}
			}
		}
	}
	if t.Node != "" {
		i, err := Find(all, t.Node)
		if err != nil {
			return i, 0, err
		}
		if t.Len == 0 {
			t.Len = i.D.OutBytes()
		}
		return i, t.Len, nil
	}
	if ctl, dat := SonixLayout(all); ctl != nil && dat != nil {
		if role == "data" {
			return *dat, dat.D.OutBytes(), nil
		}
		return *ctl, ctl.D.FeatBytes(), nil
	}
	cfg, bulk := LegacyLayout(all)
	if cfg == nil || bulk == nil {
		return hidraw.Info{}, 0, errors.New("this keyboard does not use a known layout; run the probe in Advanced and share the report")
	}
	i := cfg
	if role == "data" {
		i = bulk
	}
	return *i, i.D.OutBytes(), nil
}

// ---- probe -------------------------------------------------------------------

type Reply struct {
	Node string `json:"node"`
	Hex  string `json:"hex"`
}

type ProbeStep struct {
	Node    string  `json:"node"`
	Len     int     `json:"len"`
	Sent    string  `json:"sent"`
	Replies []Reply `json:"replies"`
	Error   string  `json:"error,omitempty"`
}

type FeatureRead struct {
	Node  string `json:"node"`
	Len   int    `json:"len"`
	Hex   string `json:"hex,omitempty"`
	Error string `json:"error,omitempty"`
}

type ProbeResult struct {
	Dry      bool          `json:"dry"`
	Steps    []ProbeStep   `json:"steps"`
	Features []FeatureRead `json:"features"`
	Text     string        `json:"text"` // Markdown, ready for a bug report
}

func Hex(b []byte) string {
	if len(b) > 64 {
		return fmt.Sprintf("% x ... (%d bytes)", b[:64], len(b))
	}
	return fmt.Sprintf("% x", b)
}

// Probe sends one GET_DEVICE_INFO frame (cmd 0x10, chunk 0) to every vendor
// interface that has an output report, smallest report first, listening on all
// vendor inputs. With dry=true it only reports what it would send.
func (c *Core) Probe(dry, feature bool) (*ProbeResult, error) {
	all, err := c.Devices()
	if err != nil {
		return nil, err
	}
	vend := Vendor(all)
	var cands []hidraw.Info
	for _, i := range vend {
		if i.D.OutBytes() >= 16 {
			cands = append(cands, i)
		}
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].D.OutBytes() < cands[b].D.OutBytes() })
	if len(cands) == 0 {
		return nil, errors.New("no vendor-page interface with an output report")
	}
	res := &ProbeResult{Dry: dry}
	var out strings.Builder
	say := func(f string, a ...any) { fmt.Fprintf(&out, f, a...) }
	say("\n## probe %s\n\n", time.Now().Format(time.RFC3339))
	if dry {
		say("dry run: would send, listening on all vendor inputs for 500 ms each:\n")
	}
	for _, i := range cands {
		pkt := proto.Chunks(i.D.OutBytes(), proto.CmdInfo, nil, 48)[0]
		res.Steps = append(res.Steps, ProbeStep{Node: i.Node, Len: i.D.OutBytes(), Sent: Hex(pkt)})
	}
	if feature {
		for _, i := range vend {
			if i.D.FeatBytes() > 0 {
				res.Features = append(res.Features, FeatureRead{Node: i.Node, Len: i.D.FeatBytes()})
			}
		}
	}
	if dry {
		for _, s := range res.Steps {
			say("- write %s (%d B report): `%s`\n", s.Node, s.Len, s.Sent)
		}
		for _, f := range res.Features {
			say("- GET_FEATURE id 0 on %s (%d B)\n", f.Node, f.Len)
		}
		res.Text = out.String()
		return res, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	devs := map[string]DevIO{}
	for _, i := range vend {
		d, err := c.OpenDev(i)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		devs[i.Node] = d
	}
	for k, i := range cands {
		for _, v := range vend {
			for v.D.InBytes() > 0 { // drain stale input
				if r, _ := devs[v.Node].ReadTimeout(time.Millisecond); r == nil {
					break
				}
			}
		}
		pkt := proto.Chunks(i.D.OutBytes(), proto.CmdInfo, nil, 48)[0]
		st := &res.Steps[k]
		say("- write %s (%d B): `%s`\n", i.Node, st.Len, Hex(pkt))
		if err := devs[i.Node].Write(pkt); err != nil {
			st.Error = err.Error()
			say("  write failed: %v\n", err)
			continue
		}
		end := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(end) {
			for _, v := range vend {
				if v.D.InBytes() == 0 {
					continue
				}
				if r, _ := devs[v.Node].ReadTimeout(20 * time.Millisecond); r != nil {
					st.Replies = append(st.Replies, Reply{v.Node, Hex(r)})
					say("  <- %s: `%s`\n", v.Node, Hex(r))
				}
			}
		}
		if len(st.Replies) == 0 {
			say("  <- (no reply)\n")
		}
	}
	for k := range res.Features {
		f := &res.Features[k]
		r, err := devs[f.Node].GetFeature(0, f.Len)
		if err != nil {
			f.Error = err.Error()
			say("- GET_FEATURE id 0 %s: %v\n", f.Node, err)
		} else {
			f.Hex = Hex(r)
			say("- GET_FEATURE id 0 %s: `%s`\n", f.Node, f.Hex)
		}
	}
	res.Text = out.String()
	return res, nil
}

// ---- info / upload / builtin / time ------------------------------------------

type InfoResult struct {
	Dry    bool        `json:"dry"`
	Node   string      `json:"node"`
	Len    int         `json:"len"`
	Frames []string    `json:"frames"`
	Info   *proto.Info `json:"info,omitempty"`
}

// Info sends GET_DEVICE_INFO to t (or the detected legacy command interface).
// A real reply marks the keyboard connected for this session.
func (c *Core) Info(t Target, dry bool) (*InfoResult, error) {
	all, err := c.Devices()
	if err != nil {
		return nil, err
	}
	if _, _, ok, _ := c.sonixPair(t); ok {
		return nil, fmt.Errorf("%w: this keyboard has no device-info command. Run `ak820 time --yes` to connect and set the clock instead", ErrUnsupported)
	}
	i, n, err := c.Resolve(all, "cmd", t)
	if err != nil {
		return nil, err
	}
	res := &InfoResult{Dry: dry, Node: i.Node, Len: n}
	for _, p := range proto.Chunks(n, proto.CmdInfo, nil, 48) {
		res.Frames = append(res.Frames, Hex(p))
	}
	if dry {
		return res, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d, err := c.OpenDev(i)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	p := &proto.Port{C: d, Len: n, Timeout: 500 * time.Millisecond, Retries: 3}
	r, err := p.Command(proto.CmdInfo, nil, 48)
	if err != nil {
		return res, err
	}
	in, err := proto.ParseInfo(r)
	if err != nil {
		return res, err
	}
	res.Info = &in
	c.MarkConnected(i.Node)
	return res, nil
}

type SendResult struct {
	Dry     bool     `json:"dry"`
	Node    string   `json:"node"`
	Len     int      `json:"len"`
	Packets int      `json:"packets"`
	First   string   `json:"first"`
	Steps   []string `json:"steps,omitempty"` // every control frame, for the details view
	Flash   bool     `json:"flash"`           // true: the change is saved in the keyboard and survives unplugging
}

func (c *Core) openFor(role string, t Target) (hidraw.Info, int, error) {
	all, err := c.Devices()
	if err != nil {
		return hidraw.Info{}, 0, err
	}
	return c.Resolve(all, role, t)
}

// Upload sends an animation payload (0x50 stream) to the data interface.
func (c *Core) Upload(t Target, payload []byte, dry bool, progress func(done, total int)) (*SendResult, error) {
	if ctl, dat, ok, err := c.sonixPair(t); err != nil {
		return nil, err
	} else if ok {
		return c.sonixUpload(ctl, dat, payload, dry, progress)
	}
	i, n, err := c.openFor("data", t)
	if err != nil {
		return nil, err
	}
	pk := proto.AnimChunks(n, payload)
	res := &SendResult{Dry: dry, Node: i.Node, Len: n, Packets: len(pk), First: Hex(pk[0])}
	if dry {
		return res, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d, err := c.OpenDev(i)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	p := &proto.Port{C: d, Len: n, Timeout: 500 * time.Millisecond, Retries: 3}
	return res, p.Upload(payload, progress)
}

// Builtin selects a built-in animation (cmd 0x51, no reply expected).
func (c *Core) Builtin(t Target, idx int, dry bool) (*SendResult, error) {
	if idx < 0 || idx > 255 {
		return nil, errors.New("index must be 0-255")
	}
	if _, _, ok, _ := c.sonixPair(t); ok {
		return nil, fmt.Errorf("%w: built-in animations are not available on this keyboard", ErrUnsupported)
	}
	i, n, err := c.openFor("data", t)
	if err != nil {
		return nil, err
	}
	pk := proto.Chunks(n, proto.CmdBuiltin, []byte{byte(idx)}, 1)
	res := &SendResult{Dry: dry, Node: i.Node, Len: n, Packets: len(pk), First: Hex(pk[0])}
	if dry {
		return res, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d, err := c.OpenDev(i)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	p := &proto.Port{C: d, Len: n}
	return res, p.Send(proto.CmdBuiltin, []byte{byte(idx)}, 1)
}

// SetTime syncs the screen clock (cmd 0x34 on the command interface).
func (c *Core) SetTime(t Target, now time.Time, dry bool) (*SendResult, error) {
	if ctl, dat, ok, err := c.sonixPair(t); err != nil {
		return nil, err
	} else if ok {
		return c.sonixClock(ctl, dat, now, dry)
	}
	i, n, err := c.openFor("cmd", t)
	if err != nil {
		return nil, err
	}
	data := []byte{0x5A, 1, 0x5A, byte(now.Year() % 100), byte(now.Month()), byte(now.Day()),
		byte(now.Hour()), byte(now.Minute()), byte(now.Second()), byte(now.Weekday())}
	pk := proto.Chunks(n, proto.CmdTemp, data, 24)
	res := &SendResult{Dry: dry, Node: i.Node, Len: n, Packets: len(pk), First: Hex(pk[0])}
	if dry {
		return res, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	d, err := c.OpenDev(i)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	p := &proto.Port{C: d, Len: n, Timeout: 500 * time.Millisecond, Retries: 3}
	_, err = p.Command(proto.CmdTemp, data, 24)
	return res, err
}

// ---- report ------------------------------------------------------------------

func Report(all []hidraw.Info) string {
	var b strings.Builder
	b.WriteString("| node | iface | VID:PID | name | usage page | in B | out B | feature B | report IDs | rw |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, i := range all {
		d := i.D
		fmt.Fprintf(&b, "| %s | %d | %04x:%04x | %s | 0x%04X | %d | %d | %d | %v | %v |\n",
			i.Node, i.Iface, i.VID, i.PID, i.Name, d.Page, d.InBytes(), d.OutBytes(), d.FeatBytes(), d.ReportIDs, i.Accessible())
	}
	b.WriteString("\nDescriptors:\n")
	for _, i := range all {
		fmt.Fprintf(&b, "- `%s` (%d B): `% x`\n", i.Node, len(i.Raw), i.Raw)
	}
	return b.String()
}
