package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/history"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/legacy"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/setup"
)

// nodeFlags adds --node and --len, which pick an interface by hand.
func nodeFlags(fs *flag.FlagSet, nodeHelp string) (node *string, reportLen *int) {
	return fs.String("node", "", nodeHelp), fs.Int("len", 0, "output report length (default: from descriptor)")
}

// driver finds the keyboard and returns its driver.
func (a *app) driver(o keyboard.Override) (keyboard.Driver, error) {
	ifaces, err := a.finder.Interfaces()
	if err != nil {
		return nil, err
	}
	return a.finder.Driver(ifaces, o)
}

func (a *app) cmdList() error {
	ifaces, err := a.finder.Interfaces()
	if err != nil {
		return err
	}
	for _, i := range ifaces {
		d := i.Descriptor
		fmt.Fprintf(a.out, "%s  if%d  %04x:%04x  page 0x%04X  in %4d  out %4d  feat %3d  rw=%v  %s\n",
			i.Node, i.Iface, i.VID, i.PID, d.Page, d.InBytes(), d.OutBytes(), d.FeatBytes(), i.Accessible(), i.Name)
	}
	return nil
}

func (a *app) cmdReport(args []string) error {
	fs := a.flags("report")
	issue := fs.Bool("issue", false, "paste-ready bug report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ifaces, err := a.finder.Interfaces()
	if err != nil {
		return err
	}
	if *issue {
		platform := runtime.GOOS + "/" + runtime.GOARCH + " " + kernel()
		fmt.Fprint(a.out, keyboard.IssueReport(ifaces, a.finder.Layout(ifaces), version, platform))
		return nil
	}
	fmt.Fprint(a.out, keyboard.Report(ifaces))
	return nil
}

// kernel names the running kernel for bug reports; it is fine if that is unknown.
func kernel() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(b))
}

func (a *app) cmdSetup(args []string) error {
	fs := a.flags("setup")
	printRule := fs.Bool("print-rule", false, "print the udev rule and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *printRule {
		fmt.Fprint(a.out, setup.Rule)
		return nil
	}
	ifaces, err := a.finder.Interfaces()
	in := setup.Input{RuleDirs: a.ruleDirs, Prog: a.name()}
	switch {
	case errors.Is(err, keyboard.ErrNoDevice): // covered by the default "plug it in" advice
	case err != nil:
		in.FindErr = errors.New(advice(err, a.name()))
	default:
		in.Found, in.Layout = len(ifaces), a.finder.Layout(ifaces).String()
		in.Name = fmt.Sprintf("%s %04x:%04x", strings.TrimPrefix(ifaces[0].Name, "SONiX "), ifaces[0].VID, ifaces[0].PID)
		for _, i := range ifaces {
			if i.Accessible() {
				in.Accessible++
			}
		}
	}
	items := setup.Check(in)
	fmt.Fprint(a.out, "ak820 setup\n\n"+setup.Format(items, a.name()))
	if !setup.AllOK(items) {
		return errExit
	}
	return nil
}

func (a *app) cmdProbe(ctx context.Context, args []string) error {
	fs := a.flags("probe")
	yes := fs.Bool("yes", false, "actually send (read-only GET_DEVICE_INFO) to the keyboard")
	logFile := fs.String("log", "PROBE-LOG.md", "append results here")
	feature := fs.Bool("feature", true, "also try a read-only GET_FEATURE on interfaces that have one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ifaces, err := a.finder.Interfaces()
	if err != nil {
		return err
	}
	p, err := legacy.NewProbe(ifaces, a.finder.Deps(), *feature)
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Fprint(a.out, p.Preview().Text)
		return nil
	}
	res, err := p.Run(ctx)
	if err != nil {
		return err
	}
	fmt.Fprint(a.out, res.Text)
	f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(a.errw, "warning: could not write %s: %v\n", *logFile, err)
		return nil
	}
	defer f.Close()
	if _, err := f.WriteString(res.Text); err != nil {
		fmt.Fprintf(a.errw, "warning: could not write %s: %v\n", *logFile, err)
		return nil
	}
	fmt.Fprintln(a.out, "\nappended to", *logFile)
	return nil
}

func (a *app) cmdInfo(ctx context.Context, args []string) error {
	fs := a.flags("info")
	yes := fs.Bool("yes", false, "send to the keyboard")
	node, reportLen := nodeFlags(fs, "hidraw node (e.g. hidraw8); default: detected legacy config interface")
	if err := fs.Parse(args); err != nil {
		return err
	}
	drv, err := a.driver(keyboard.Override{Node: *node, Len: *reportLen})
	if err != nil {
		return err
	}
	ld, ok := drv.(*legacy.Driver)
	if !ok {
		return keyboard.ErrNoDeviceInfo
	}
	plan, err := ld.PlanInfo()
	if err != nil {
		return err
	}
	if !*yes {
		d := plan.Describe()
		for _, f := range d.Steps {
			fmt.Fprintf(a.out, "dry run: would write %s: %s\n", d.Node, f)
		}
		return nil
	}
	if err := plan.Run(ctx, nil); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%+v\n", plan.Info)
	return nil
}

func (a *app) cmdUpload(ctx context.Context, args []string) error {
	fs := a.flags("upload")
	yes := fs.Bool("yes", false, "send to the keyboard")
	node, reportLen := nodeFlags(fs, "data interface (default: detected from the layout)")
	fit := fs.String("fit", "cover", "cover | contain | stretch")
	bg := fs.String("bg", "black", "background: black, white or RRGGBB")
	delay := fs.Int("delay", 0, "override per-frame delay (ms)")
	maxFrames := fs.Int("max-frames", 255, "truncate to this many frames")
	preview := fs.String("preview", "", "write a PNG preview of frame 0 and continue")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: ak820 upload [flags] <image>")
	}
	mode, err := animation.ParseFit(*fit)
	if err != nil {
		return err
	}
	frames, err := animation.Load(fs.Arg(0), mode, animation.ParseColor(*bg))
	if err != nil {
		return err
	}
	if *delay > 0 {
		for i := range frames {
			frames[i].DelayMs = *delay
		}
	}
	frames = frames[:min(len(frames), *maxFrames, 255)]
	if *preview != "" {
		if err := savePNG(*preview, frames[0].Img); err != nil {
			return err
		}
	}
	drv, err := a.driver(keyboard.Override{Node: *node, Len: *reportLen})
	if err != nil {
		return err
	}
	plan, err := drv.PlanUpload(frames)
	if err != nil {
		return err
	}
	d := plan.Describe()
	fmt.Fprintf(a.out, "%d frame(s), %d bytes, %d packets of %d B to %s\n", len(frames), len(legacy.Encode(frames)), d.Packets, d.ReportSize, d.Node)
	if !*yes {
		fmt.Fprintln(a.out, "dry run: first packet:", d.First)
		if d.Persistent {
			fmt.Fprintln(a.out, "(this keyboard saves the picture in its flash memory; it stays after unplugging)")
		} else {
			fmt.Fprintln(a.out, "(note: on firmware 1.16 uploads live in RAM and are lost on unplug / mode change)")
		}
		fmt.Fprintln(a.out, "nothing was sent. Add --yes to send it.")
		return nil
	}
	if err := plan.Run(ctx, func(done, total int) { fmt.Fprintf(a.errw, "\r  uploading %3d%%", 100*done/total) }); err != nil {
		return err
	}
	fmt.Fprintln(a.errw)
	if err := history.Save(a.historyDir, frames, fs.Arg(0)); err != nil {
		fmt.Fprintf(a.errw, "warning: the picture was sent, but could not be added to your history: %v\n", err)
	}
	fmt.Fprintln(a.out, "done")
	return nil
}

func savePNG(path string, im *image.RGBA) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	return png.Encode(f, im)
}

func (a *app) cmdBuiltin(ctx context.Context, args []string) error {
	fs := a.flags("builtin")
	yes := fs.Bool("yes", false, "send to the keyboard")
	node, reportLen := nodeFlags(fs, "data interface (default: detected from the layout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: ak820 builtin [flags] <index>")
	}
	index, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("the animation number must be a number, got %q", fs.Arg(0))
	}
	drv, err := a.driver(keyboard.Override{Node: *node, Len: *reportLen})
	if err != nil {
		return err
	}
	plan, err := drv.PlanBuiltin(index)
	if err != nil {
		return err
	}
	if !*yes {
		d := plan.Describe()
		fmt.Fprintf(a.out, "dry run: would write %s: %s\n", d.Node, d.First)
		return nil
	}
	if err := plan.Run(ctx, nil); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "sent")
	return nil
}

func (a *app) cmdTime(ctx context.Context, args []string) error {
	fs := a.flags("time")
	yes := fs.Bool("yes", false, "send to the keyboard")
	node, reportLen := nodeFlags(fs, "command interface (default: detected)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	drv, err := a.driver(keyboard.Override{Node: *node, Len: *reportLen})
	if err != nil {
		return err
	}
	plan, err := drv.PlanClock(time.Now())
	if err != nil {
		return err
	}
	if !*yes {
		d := plan.Describe()
		fmt.Fprintf(a.out, "dry run: would write %s: %s\n", d.Node, d.First)
		fmt.Fprintln(a.out, "nothing was sent. Add --yes to send it.")
		return nil
	}
	if err := plan.Run(ctx, nil); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "clock set")
	return nil
}
