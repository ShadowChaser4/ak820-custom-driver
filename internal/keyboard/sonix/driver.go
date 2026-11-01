package sonix

import (
	"context"
	"fmt"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
)

const controlSize = 64

// Driver implements keyboard.Driver for this protocol.
type Driver struct {
	deps          keyboard.Deps
	control, data hidraw.Info
}

// Detect recognises the layout: page 0xFF13 with a 64-byte feature report and
// page 0xFF68 with a 4096-byte output report.
func Detect(ifaces []hidraw.Info, deps keyboard.Deps) (keyboard.Driver, bool) {
	var ctl, dat *hidraw.Info
	for k := range ifaces {
		i := &ifaces[k]
		switch {
		case i.Descriptor.Page == 0xFF13 && i.Descriptor.FeatBytes() == controlSize && ctl == nil:
			ctl = i
		case i.Descriptor.Page == 0xFF68 && i.Descriptor.OutBytes() == ChunkSize && dat == nil:
			dat = i
		}
	}
	if ctl == nil || dat == nil {
		return nil, false
	}
	return &Driver{deps: deps, control: *ctl, data: *dat}, true
}

// Layout implements keyboard.Driver.
func (d *Driver) Layout() keyboard.Layout { return keyboard.LayoutSonix }

// Capabilities implements keyboard.Driver.
func (d *Driver) Capabilities() keyboard.Capabilities {
	return keyboard.Capabilities{Clock: true, Persistent: true}
}

// featurePipe adapts a device to Control.
type featurePipe struct{ dev keyboard.Device }

func (f featurePipe) SetFeature(p []byte) error { return f.dev.SetFeature(p, controlSize) }

func (f featurePipe) GetFeature() ([]byte, error) {
	r, err := f.dev.GetFeature(0, controlSize)
	if err != nil {
		return nil, err
	}
	if len(r) > 0 {
		r = r[1:] // drop the report-id byte
	}
	return r, nil
}

// withPort opens what is needed and calls fn with exclusive access to the keyboard.
func (d *Driver) withPort(needData bool, fn func(*Port) error) error {
	d.deps.Lock.Lock()
	defer d.deps.Lock.Unlock()
	ctl, err := d.deps.Open(d.control)
	if err != nil {
		return err
	}
	defer ctl.Close()
	p := &Port{Control: featurePipe{ctl}}
	if needData {
		dat, err := d.deps.Open(d.data)
		if err != nil {
			return err
		}
		defer dat.Close()
		p.Data = dat
	}
	return fn(p)
}

type clockPlan struct {
	d      *Driver
	frames [][]byte
}

func (p *clockPlan) Describe() keyboard.Description {
	var steps []string
	for _, f := range p.frames {
		steps = append(steps, keyboard.Hex(f))
	}
	return keyboard.Description{Node: p.d.control.Node, ReportSize: controlSize, Packets: len(p.frames),
		First: steps[0], Steps: steps, Persistent: true}
}

func (p *clockPlan) Run(ctx context.Context, _ func(int, int)) error {
	return p.d.withPort(false, func(port *Port) error { return port.SyncClock(ctx, p.frames) })
}

// PlanClock implements keyboard.Driver.
func (d *Driver) PlanClock(now time.Time) (keyboard.Plan, error) {
	return &clockPlan{d, ClockFrames(now)}, nil
}

type uploadPlan struct {
	d       *Driver
	payload []byte
	chunks  int
}

func (p *uploadPlan) Describe() keyboard.Description {
	return keyboard.Description{Node: p.d.data.Node, ReportSize: ChunkSize, Packets: p.chunks,
		First: keyboard.Hex(p.payload[:64]), Persistent: true}
}

func (p *uploadPlan) Run(ctx context.Context, progress func(int, int)) error {
	return p.d.withPort(true, func(port *Port) error { return port.Upload(ctx, p.payload, p.chunks, progress) })
}

// PlanUpload implements keyboard.Driver.
func (d *Driver) PlanUpload(frames []animation.Frame) (keyboard.Plan, error) {
	payload, chunks, err := BuildPayload(frames)
	if err != nil {
		return nil, err
	}
	return &uploadPlan{d, payload, chunks}, nil
}

// PlanBuiltin implements keyboard.Driver. This protocol has no built-in animations.
func (d *Driver) PlanBuiltin(int) (keyboard.Plan, error) {
	return nil, fmt.Errorf("%w: built-in animations are not available on this keyboard", keyboard.ErrUnsupported)
}
