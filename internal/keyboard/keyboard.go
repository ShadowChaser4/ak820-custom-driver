// Package keyboard finds an AK820 keyboard and defines what a protocol driver
// must provide. The protocols themselves live in the sonix and legacy
// subpackages; package drivers wires them together.
//
// Nothing here talks to the device until a Plan is run: a Plan describes exactly
// what will be sent, so a dry run and a real run can never disagree.
package keyboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
)

// VendorID is the USB vendor of the keyboard's controller. It is shared by many
// unrelated products, so it is never enough on its own: see SupportedProducts.
const VendorID = 0x0C45

// SupportedProducts are the USB products this tool drives. The 2.4G dongle
// (0xfefe) is left out on purpose: it cannot reach the screen.
var SupportedProducts = map[uint16]bool{0x800A: true, 0x8009: true}

// Errors callers can test for with errors.Is.
var (
	ErrNoDevice = errors.New("no keyboard found")
	// ErrNotConfigured means the keyboard is plugged in, but the computer could not set it up.
	ErrNotConfigured = errors.New("the keyboard is plugged in but could not finish connecting")
	ErrUnknownLayout = errors.New("this keyboard talks in a way this tool does not know yet")
	ErrUnsupported   = errors.New("this keyboard does not support that")
	ErrNoReply       = errors.New("no reply")
	// ErrNoDeviceInfo is the ErrUnsupported returned for the device-info command.
	ErrNoDeviceInfo = fmt.Errorf("%w: this keyboard has no device-info command", ErrUnsupported)
)

// MultipleError reports several connected keyboards when none was chosen.
type MultipleError struct{ N int }

func (e *MultipleError) Error() string { return fmt.Sprintf("%d keyboards connected", e.N) }

// Layout names the protocol a keyboard speaks.
type Layout int

// The known layouts.
const (
	LayoutUnknown Layout = iota
	LayoutLegacy
	LayoutSonix
)

func (l Layout) String() string {
	switch l {
	case LayoutLegacy:
		return "legacy"
	case LayoutSonix:
		return "sonix"
	}
	return "unknown"
}

// MarshalText makes a Layout appear by name in JSON.
func (l Layout) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

// Capabilities says what a driver can do.
type Capabilities struct {
	Clock      bool // can set the screen clock
	Builtin    bool // can select a built-in animation
	Persistent bool // uploads survive unplugging the keyboard
}

// Description is what a Plan will do, for showing before it happens.
type Description struct {
	Node       string   // the interface written to
	ReportSize int      // bytes per report
	Packets    int      // reports that will be sent
	First      string   // hex of the first report
	Steps      []string // hex of every control frame, when there are few
	Persistent bool     // the result survives unplugging
}

// Plan is one prepared operation. Creating it does no device I/O.
type Plan interface {
	Describe() Description
	// Run sends the plan. progress may be nil. Cancelling ctx stops the transfer;
	// a transfer that did not finish is never committed to the keyboard.
	Run(ctx context.Context, progress func(done, total int)) error
}

// Driver prepares operations for one keyboard.
type Driver interface {
	Layout() Layout
	Capabilities() Capabilities
	PlanClock(now time.Time) (Plan, error)
	PlanUpload(frames []animation.Frame) (Plan, error)
	PlanBuiltin(index int) (Plan, error)
}

// Device is an open keyboard interface. *hidraw.Device implements it.
type Device interface {
	Write(report []byte) error
	// Read returns one report. A timeout is reported as os.ErrDeadlineExceeded.
	Read(ctx context.Context, timeout time.Duration) ([]byte, error)
	GetFeature(id byte, n int) ([]byte, error)
	SetFeature(payload []byte, n int) error
	Close() error
}

// Opener opens an interface.
type Opener func(hidraw.Info) (Device, error)

// Deps is what drivers need to reach the device. Lock serialises access: a
// keyboard must not be driven by two operations at once.
type Deps struct {
	Open Opener
	Lock *sync.Mutex
}

// Detector returns a driver if the interfaces match its protocol.
type Detector func(ifaces []hidraw.Info, deps Deps) (Driver, bool)

// DataOverrider is implemented by drivers whose upload interface can be chosen by hand.
type DataOverrider interface {
	WithData(hidraw.Info) Driver
}

// Override chooses interfaces by hand. The zero value detects everything.
type Override struct {
	Node     string // use this node for everything, with Explicit
	Len      int    // report length for Node; 0 means from its descriptor
	DataNode string // upload through this node, when the driver allows it
}

// Config configures a Finder.
type Config struct {
	Enumerate func() ([]hidraw.Info, error)
	Open      Opener
	Detectors []Detector
	// Explicit builds a driver for one hand-picked node.
	Explicit func(i hidraw.Info, reportLen int, deps Deps) Driver
}

// Finder locates the keyboard and picks its driver.
type Finder struct {
	cfg    Config
	lock   *sync.Mutex
	device int // 1-based choice among several keyboards; 0 requires exactly one
	// unconfigured reports a keyboard that is plugged in but could not be set up.
	unconfigured func() bool
}

// NewFinder returns a Finder.
func NewFinder(cfg Config) *Finder { return &Finder{cfg: cfg, lock: &sync.Mutex{}} }

// WithDevice returns a Finder that uses the n-th keyboard (from 1) when several are connected.
func (f *Finder) WithDevice(n int) *Finder {
	c := *f
	c.device = n
	return &c
}

// WithUnconfigured returns a Finder that uses check to tell a keyboard that could not
// be set up from one that is not plugged in.
func (f *Finder) WithUnconfigured(check func() bool) *Finder {
	c := *f
	c.unconfigured = check
	return &c
}

// Deps returns what drivers built outside this Finder need.
func (f *Finder) Deps() Deps { return Deps{Open: f.cfg.Open, Lock: f.lock} }

// Interfaces returns the interfaces of the keyboard to use, freshly enumerated:
// hidraw numbers change on every replug, so they are never stored.
func (f *Finder) Interfaces() ([]hidraw.Info, error) {
	all, err := f.cfg.Enumerate()
	if err != nil {
		return nil, err
	}
	var ours []hidraw.Info
	for _, i := range all {
		if SupportedProducts[i.PID] {
			ours = append(ours, i)
		}
	}
	groups := hidraw.Groups(ours)
	switch {
	case len(groups) == 0:
		if f.unconfigured != nil && f.unconfigured() {
			return nil, ErrNotConfigured
		}
		return nil, ErrNoDevice
	case f.device > len(groups):
		return nil, fmt.Errorf("keyboard %d was requested, but only %d found", f.device, len(groups))
	case f.device > 0:
		return groups[f.device-1], nil
	case len(groups) > 1:
		return nil, &MultipleError{len(groups)}
	}
	return groups[0], nil
}

// Layout reports the layout of the given interfaces.
func (f *Finder) Layout(ifaces []hidraw.Info) Layout {
	for _, detect := range f.cfg.Detectors {
		if d, ok := detect(ifaces, f.Deps()); ok {
			return d.Layout()
		}
	}
	return LayoutUnknown
}

// Driver returns the driver for the given interfaces.
func (f *Finder) Driver(ifaces []hidraw.Info, o Override) (Driver, error) {
	if o.Node != "" {
		i, err := Find(ifaces, o.Node)
		if err != nil {
			return nil, err
		}
		n := o.Len
		if n == 0 {
			n = i.Descriptor.OutBytes()
		}
		return f.cfg.Explicit(i, n, f.Deps()), nil
	}
	for _, detect := range f.cfg.Detectors {
		d, ok := detect(ifaces, f.Deps())
		if !ok {
			continue
		}
		if v, can := d.(DataOverrider); can && o.DataNode != "" {
			// a chosen node that vanished in a replug is ignored
			if i, err := Find(ifaces, o.DataNode); err == nil {
				return v.WithData(i), nil
			}
		}
		return d, nil
	}
	return nil, ErrUnknownLayout
}

// Find returns the interface with the given node name ("hidraw8" or "/dev/hidraw8").
func Find(ifaces []hidraw.Info, node string) (hidraw.Info, error) {
	for _, i := range ifaces {
		if i.Node == node || strings.TrimPrefix(i.Node, "/dev/") == node {
			return i, nil
		}
	}
	return hidraw.Info{}, fmt.Errorf("%s is not a 0c45 hidraw node", node)
}

// Hex formats bytes for display, shortening long reports.
func Hex(b []byte) string {
	if len(b) > 64 {
		return fmt.Sprintf("% x ... (%d bytes)", b[:64], len(b))
	}
	return fmt.Sprintf("% x", b)
}

// Report is a Markdown table of the interfaces, plus their raw descriptors.
func Report(ifaces []hidraw.Info) string {
	var b strings.Builder
	b.WriteString("| node | iface | VID:PID | name | usage page | in B | out B | feature B | report IDs | rw |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, i := range ifaces {
		d := i.Descriptor
		fmt.Fprintf(&b, "| %s | %d | %04x:%04x | %s | 0x%04X | %d | %d | %d | %v | %v |\n",
			i.Node, i.Iface, i.VID, i.PID, i.Name, d.Page, d.InBytes(), d.OutBytes(), d.FeatBytes(), d.ReportIDs, i.Accessible())
	}
	b.WriteString("\nDescriptors:\n")
	for _, i := range ifaces {
		fmt.Fprintf(&b, "- `%s` (%d B): `% x`\n", i.Node, len(i.Raw), i.Raw)
	}
	return b.String()
}

// IssueReport is a paste-ready Markdown block for a bug report or a new-keyboard request.
func IssueReport(ifaces []hidraw.Info, l Layout, version, platform string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**ak820** %s on %s\n\n", version, platform)
	fmt.Fprintf(&b, "**Layout detected:** %s\n\n", l)
	b.WriteString(Report(ifaces))
	return b.String()
}
