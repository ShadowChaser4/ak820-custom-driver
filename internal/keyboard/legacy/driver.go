package legacy

import (
	"context"
	"fmt"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
)

// target is an interface and the report length to use on it.
type target struct {
	info hidraw.Info
	size int
}

// Driver implements keyboard.Driver for the legacy protocol.
type Driver struct {
	deps      keyboard.Deps
	cmd, data target
}

// configPages are the usage pages the vendor software accepts for the config interface.
var configPages = map[uint32]bool{0xFF68: true, 0xFF80: true, 0xFF60: true, 0xFF00: true, 0xFF01: true, 0xFF1B: true}

// Detect recognises the layout: a config page plus a bulk page 0xFF67.
func Detect(ifaces []hidraw.Info, deps keyboard.Deps) (keyboard.Driver, bool) {
	var cfg, bulk *hidraw.Info
	for k := range ifaces {
		i := &ifaces[k]
		if i.Descriptor.Page == 0xFF67 {
			bulk = i
		} else if configPages[i.Descriptor.Page] && cfg == nil {
			cfg = i
		}
	}
	if cfg == nil || bulk == nil {
		return nil, false
	}
	return &Driver{deps: deps,
		cmd:  target{*cfg, cfg.Descriptor.OutBytes()},
		data: target{*bulk, bulk.Descriptor.OutBytes()},
	}, true
}

// ForNode returns a driver that uses one hand-picked node for everything.
func ForNode(i hidraw.Info, reportLen int, deps keyboard.Deps) keyboard.Driver {
	t := target{i, reportLen}
	return &Driver{deps: deps, cmd: t, data: t}
}

// WithData returns a copy that uploads through the given interface.
func (d *Driver) WithData(i hidraw.Info) keyboard.Driver {
	c := *d
	c.data = target{i, i.Descriptor.OutBytes()}
	return &c
}

// Layout implements keyboard.Driver.
func (d *Driver) Layout() keyboard.Layout { return keyboard.LayoutLegacy }

// Capabilities implements keyboard.Driver. Uploads are held in RAM on firmware 1.16.
func (d *Driver) Capabilities() keyboard.Capabilities {
	return keyboard.Capabilities{Clock: true, Builtin: true}
}

func checkSize(t target) error {
	if t.size <= 8 {
		return fmt.Errorf("%s: a report of %d bytes is too small for this protocol", t.info.Node, t.size)
	}
	return nil
}

// plan is a list of packets and how to send them.
type plan struct {
	deps keyboard.Deps
	t    target
	pkts [][]byte
	run  func(ctx context.Context, p *Port, progress func(done, total int)) error
	// steps lists every frame in the description
	steps bool
}

// Describe implements keyboard.Plan.
func (p *plan) Describe() keyboard.Description {
	desc := keyboard.Description{Node: p.t.info.Node, ReportSize: p.t.size, Packets: len(p.pkts), First: keyboard.Hex(p.pkts[0])}
	if p.steps {
		for _, f := range p.pkts {
			desc.Steps = append(desc.Steps, keyboard.Hex(f))
		}
	}
	return desc
}

// Run implements keyboard.Plan.
func (p *plan) Run(ctx context.Context, progress func(done, total int)) error {
	p.deps.Lock.Lock()
	defer p.deps.Lock.Unlock()
	dev, err := p.deps.Open(p.t.info)
	if err != nil {
		return err
	}
	defer dev.Close()
	return p.run(ctx, &Port{Conn: dev, Timeout: 500 * time.Millisecond, Retries: 3}, progress)
}

// PlanClock implements keyboard.Driver.
func (d *Driver) PlanClock(now time.Time) (keyboard.Plan, error) {
	if err := checkSize(d.cmd); err != nil {
		return nil, err
	}
	data := []byte{0x5A, 1, 0x5A, byte(now.Year() % 100), byte(now.Month()), byte(now.Day()),
		byte(now.Hour()), byte(now.Minute()), byte(now.Second()), byte(now.Weekday())}
	pkts := Chunks(d.cmd.size, CmdClock, data, 24)
	return &plan{deps: d.deps, t: d.cmd, pkts: pkts,
		run: func(ctx context.Context, p *Port, _ func(int, int)) error {
			_, err := p.Exchange(ctx, pkts, CmdClock)
			return err
		}}, nil
}

// PlanUpload implements keyboard.Driver.
func (d *Driver) PlanUpload(frames []animation.Frame) (keyboard.Plan, error) {
	if err := checkSize(d.data); err != nil {
		return nil, err
	}
	pkts := AnimChunks(d.data.size, Encode(frames))
	return &plan{deps: d.deps, t: d.data, pkts: pkts,
		run: func(ctx context.Context, p *Port, progress func(int, int)) error {
			return p.Upload(ctx, pkts, progress)
		}}, nil
}

// PlanBuiltin implements keyboard.Driver. The keyboard sends no reply to this command.
func (d *Driver) PlanBuiltin(index int) (keyboard.Plan, error) {
	if index < 0 || index > 255 {
		return nil, fmt.Errorf("animation number must be 0-255, got %d", index)
	}
	if err := checkSize(d.data); err != nil {
		return nil, err
	}
	pkts := Chunks(d.data.size, CmdBuiltin, []byte{byte(index)}, 1)
	return &plan{deps: d.deps, t: d.data, pkts: pkts,
		run: func(_ context.Context, p *Port, _ func(int, int)) error {
			for _, pkt := range pkts {
				if err := p.Conn.Write(pkt); err != nil {
					return err
				}
			}
			return nil
		}}, nil
}

// InfoPlan reads the device info. Info is valid after Run succeeds.
type InfoPlan struct {
	plan
	Info Info
}

// PlanInfo prepares GET_DEVICE_INFO, which only this protocol has.
func (d *Driver) PlanInfo() (*InfoPlan, error) {
	if err := checkSize(d.cmd); err != nil {
		return nil, err
	}
	pkts := Chunks(d.cmd.size, CmdInfo, nil, 48)
	ip := &InfoPlan{plan: plan{deps: d.deps, t: d.cmd, pkts: pkts, steps: true}}
	ip.run = func(ctx context.Context, p *Port, _ func(int, int)) error {
		r, err := p.Exchange(ctx, pkts, CmdInfo)
		if err != nil {
			return err
		}
		ip.Info, err = ParseInfo(r)
		return err
	}
	return ip, nil
}
