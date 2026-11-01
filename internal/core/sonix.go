package core

import (
	"fmt"
	"time"

	"ak820/internal/hidraw"
	"ak820/internal/img"
	"ak820/internal/sonix"
)

// sonixPair returns the control and data interfaces if this keyboard uses the
// feature-report protocol. An explicit --node target means "use the legacy
// path on that node", so it is not routed here.
func (c *Core) sonixPair(t Target) (ctl, dat hidraw.Info, ok bool, err error) {
	if t.Node != "" {
		return
	}
	all, err := c.Devices()
	if err != nil {
		return
	}
	a, b := SonixLayout(all)
	if a == nil || b == nil {
		return
	}
	return *a, *b, true, nil
}

type ctrlAdapter struct{ d DevIO }

func (a ctrlAdapter) SetFeature(p []byte) error { return a.d.SetFeature(p, 64) }
func (a ctrlAdapter) GetFeature() ([]byte, error) {
	r, err := a.d.GetFeature(0, 64)
	if err != nil {
		return nil, err
	}
	if len(r) > 0 {
		r = r[1:] // drop the report-id byte
	}
	return r, nil
}

type dataAdapter struct{ d DevIO }

// WriteData sends one raw output report (the hidraw layer prepends report id 0).
func (a dataAdapter) WriteData(p []byte) error { return a.d.Write(p) }
func (a dataAdapter) ReadAck(t time.Duration) ([]byte, error) {
	return a.d.ReadTimeout(t)
}

func hexFrames(fs [][]byte) []string {
	var out []string
	for _, f := range fs {
		out = append(out, Hex(f))
	}
	return out
}

func (c *Core) sonixOpen(ctl, dat hidraw.Info, needData bool) (*sonix.Port, func(), error) {
	cd, err := c.OpenDev(ctl)
	if err != nil {
		return nil, nil, err
	}
	closeFn := func() { cd.Close() }
	p := &sonix.Port{C: ctrlAdapter{cd}}
	if needData {
		dd, err := c.OpenDev(dat)
		if err != nil {
			cd.Close()
			return nil, nil, err
		}
		closeFn = func() { dd.Close(); cd.Close() }
		p.D = dataAdapter{dd}
	}
	return p, closeFn, nil
}

func (c *Core) sonixClock(ctl, dat hidraw.Info, now time.Time, dry bool) (*SendResult, error) {
	f := sonix.ClockFrames(now)
	res := &SendResult{Dry: dry, Node: ctl.Node, Len: 64, Packets: len(f), First: Hex(f[0]), Steps: hexFrames(f), Flash: true}
	if dry {
		return res, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, closeFn, err := c.sonixOpen(ctl, dat, false)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	if err := p.SyncClock(now); err != nil {
		return res, err
	}
	// The control channel accepted the whole sequence: this is our "confirmed" signal.
	cf := c.Config()
	cf.Cmd = Target{ctl.Node, 64}
	if err := c.SaveConfig(cf); err != nil {
		return res, err
	}
	return res, nil
}

func (c *Core) sonixUpload(ctl, dat hidraw.Info, payload []byte, dry bool, progress func(done, total int)) (*SendResult, error) {
	frames, err := img.ParsePayload(payload)
	if err != nil {
		return nil, fmt.Errorf("bad picture data: %w", err)
	}
	sp, chunks, err := sonix.BuildPayload(frames)
	if err != nil {
		return nil, err
	}
	res := &SendResult{Dry: dry, Node: dat.Node, Len: sonix.ChunkSize, Packets: chunks, First: Hex(sp[:64]), Flash: true}
	if dry {
		return res, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, closeFn, err := c.sonixOpen(ctl, dat, true)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	return res, p.Upload(sp, chunks, progress)
}
