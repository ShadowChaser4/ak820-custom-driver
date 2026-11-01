package legacy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
)

// Reply is one report received during a probe.
type Reply struct {
	Node string `json:"node"`
	Hex  string `json:"hex"`
}

// ProbeStep is one interface tried.
type ProbeStep struct {
	Node    string  `json:"node"`
	Len     int     `json:"len"`
	Sent    string  `json:"sent"`
	Replies []Reply `json:"replies"`
	Error   string  `json:"error,omitempty"`
}

// FeatureRead is one feature report read during a probe.
type FeatureRead struct {
	Node  string `json:"node"`
	Len   int    `json:"len"`
	Hex   string `json:"hex,omitempty"`
	Error string `json:"error,omitempty"`
}

// ProbeResult is what a probe found. Text is Markdown, ready for a bug report.
type ProbeResult struct {
	Dry      bool          `json:"dry"`
	Steps    []ProbeStep   `json:"steps"`
	Features []FeatureRead `json:"features"`
	Text     string        `json:"text"`
}

// Probe sends one harmless GET_DEVICE_INFO frame to every vendor interface,
// smallest report first, and listens on all of them. It is how an unknown
// keyboard is investigated.
type Probe struct {
	deps    keyboard.Deps
	vendor  []hidraw.Info
	targets []hidraw.Info
	feature bool
	frames  map[string][]byte
}

// NewProbe prepares a probe of the given interfaces. It does no device I/O.
func NewProbe(ifaces []hidraw.Info, deps keyboard.Deps, feature bool) (*Probe, error) {
	p := &Probe{deps: deps, feature: feature, frames: map[string][]byte{}}
	for _, i := range ifaces {
		if !i.Descriptor.Vendor() {
			continue
		}
		p.vendor = append(p.vendor, i)
		if n := i.Descriptor.OutBytes(); n >= 16 {
			p.targets = append(p.targets, i)
			p.frames[i.Node] = Chunks(n, CmdInfo, nil, 48)[0]
		}
	}
	sort.SliceStable(p.targets, func(a, b int) bool {
		return p.targets[a].Descriptor.OutBytes() < p.targets[b].Descriptor.OutBytes()
	})
	if len(p.targets) == 0 {
		return nil, errors.New("no vendor-page interface with an output report")
	}
	return p, nil
}

func (p *Probe) skeleton(dry bool) *ProbeResult {
	res := &ProbeResult{Dry: dry}
	for _, i := range p.targets {
		res.Steps = append(res.Steps, ProbeStep{Node: i.Node, Len: i.Descriptor.OutBytes(), Sent: keyboard.Hex(p.frames[i.Node])})
	}
	if p.feature {
		for _, i := range p.vendor {
			if n := i.Descriptor.FeatBytes(); n > 0 {
				res.Features = append(res.Features, FeatureRead{Node: i.Node, Len: n})
			}
		}
	}
	return res
}

// Preview returns what Run would send.
func (p *Probe) Preview() *ProbeResult {
	res := p.skeleton(true)
	var out strings.Builder
	fmt.Fprintf(&out, "\n## probe %s\n\n", time.Now().Format(time.RFC3339))
	out.WriteString("dry run: would send, listening on all vendor inputs for 500 ms each:\n")
	for _, s := range res.Steps {
		fmt.Fprintf(&out, "- write %s (%d B report): `%s`\n", s.Node, s.Len, s.Sent)
	}
	for _, f := range res.Features {
		fmt.Fprintf(&out, "- GET_FEATURE id 0 on %s (%d B)\n", f.Node, f.Len)
	}
	res.Text = out.String()
	return res
}

// Run sends the frames and records every reply.
func (p *Probe) Run(ctx context.Context) (*ProbeResult, error) {
	res := p.skeleton(false)
	var out strings.Builder
	say := func(f string, a ...any) { fmt.Fprintf(&out, f, a...) }
	say("\n## probe %s\n\n", time.Now().Format(time.RFC3339))

	p.deps.Lock.Lock()
	defer p.deps.Lock.Unlock()
	devs := map[string]keyboard.Device{}
	for _, i := range p.vendor {
		d, err := p.deps.Open(i)
		if err != nil {
			return nil, err
		}
		defer d.Close()
		devs[i.Node] = d
	}
	// read returns nil on a timeout.
	read := func(i hidraw.Info, t time.Duration) ([]byte, error) {
		r, err := devs[i.Node].Read(ctx, t)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, nil //nolint:nilnil // a timeout just means nothing arrived
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", i.Node, err)
		}
		return r, nil
	}
	for k, i := range p.targets {
		for _, v := range p.vendor {
			for v.Descriptor.InBytes() > 0 { // drain stale input
				r, err := read(v, time.Millisecond)
				if err != nil {
					return res, err
				}
				if r == nil {
					break
				}
			}
		}
		st := &res.Steps[k]
		say("- write %s (%d B): `%s`\n", i.Node, st.Len, st.Sent)
		if err := devs[i.Node].Write(p.frames[i.Node]); err != nil {
			st.Error = err.Error()
			say("  write failed: %v\n", err)
			continue
		}
		end := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(end) {
			for _, v := range p.vendor {
				if v.Descriptor.InBytes() == 0 {
					continue
				}
				r, err := read(v, 20*time.Millisecond)
				if err != nil {
					return res, err
				}
				if r != nil {
					st.Replies = append(st.Replies, Reply{v.Node, keyboard.Hex(r)})
					say("  <- %s: `%s`\n", v.Node, keyboard.Hex(r))
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
			f.Hex = keyboard.Hex(r)
			say("- GET_FEATURE id 0 %s: `%s`\n", f.Node, f.Hex)
		}
	}
	res.Text = out.String()
	return res, nil
}
