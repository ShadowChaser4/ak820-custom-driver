// ak820 — drive the AJAZZ / Epomaker AK820 Pro TFT screen over hidraw.
//
// Safety: every command that writes to a HID node needs --yes. Without it the
// tool prints the target node and the exact bytes, then exits.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/core"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/history"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/img"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/setup"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/web"
)

// version is stamped at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

// app holds everything a command needs, so tests can swap the pieces.
type app struct {
	c        *core.Core
	out      io.Writer
	errw     io.Writer
	ui       func(addr string, noOpen bool) error
	ruleDirs []string
	prog     string // how the user invoked us ("ak820" via the PATH, else e.g. "./ak820"); used in hints
}

func main() {
	a := &app{c: core.New(), out: os.Stdout, errw: os.Stderr, ruleDirs: setup.RuleDirs, prog: progName(os.Args[0])}
	a.ui = a.serveUI
	os.Exit(a.run(os.Args[1:]))
}

const usageText = `ak820 — change the picture on the AK820 Pro keyboard screen

  ak820              start the app in your browser (same as "ak820 ui")
  ak820 setup        check that this computer is ready, and say how to fix it if not
  ak820 list         show the keyboard's connections (no device I/O)
  ak820 report       same, as a table;  "report --issue" is a paste-ready bug report
  ak820 time         connect and set the keyboard clock (needs --yes)
  ak820 upload FILE  send a PNG, JPEG or GIF to the screen (needs --yes)
  ak820 builtin N    pick a built-in animation, older keyboards only (needs --yes)
  ak820 probe        send a harmless test message to every connection (needs --yes)
  ak820 info         read device info, older keyboards only (needs --yes)
  ak820 version

Anything that writes to the keyboard only prints what it WOULD send until you add --yes.
Several keyboards plugged in?  ak820 --device 2 <command>   (or set AK820_DEVICE=2)
`

// progName is the command name to show in hints: exactly what the user typed
// ("ak820" when run through the PATH, "./ak820" when run from a folder).
func progName(arg0 string) string { return arg0 }

func (a *app) name() string {
	if a.prog == "" {
		return "ak820"
	}
	return a.prog
}

func (a *app) run(args []string) int {
	// leading global option: --device N
	if v := os.Getenv("AK820_DEVICE"); v != "" {
		a.c.Device, _ = strconv.Atoi(v)
	}
	for len(args) >= 2 && (args[0] == "--device" || args[0] == "-device") {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			fmt.Fprintln(a.errw, "error: --device needs a number like 1 or 2")
			return 2
		}
		a.c.Device = n
		args = args[2:]
	}
	cmd, rest := "ui", []string(nil)
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "ui":
		err = a.cmdUI(rest)
	case "setup":
		err = a.cmdSetup(rest)
	case "list":
		err = a.cmdList(false, false)
	case "report":
		err = a.cmdReport(rest)
	case "probe":
		err = a.cmdProbe(rest)
	case "info":
		err = a.cmdInfo(rest)
	case "upload":
		err = a.cmdUpload(rest)
	case "builtin":
		err = a.cmdBuiltin(rest)
	case "time":
		err = a.cmdTime(rest)
	case "version", "--version", "-v":
		fmt.Fprintf(a.out, "ak820 %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	case "help", "--help", "-h":
		fmt.Fprint(a.out, usageText)
	default:
		fmt.Fprintf(a.errw, "unknown command %q\n\n%s", cmd, usageText)
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0 // the flag package already printed the usage
	}
	if err != nil {
		if err != errExit {
			fmt.Fprintln(a.errw, strings.ReplaceAll(friendlyErr(err), "`ak820 ", "`"+a.name()+" "))
		}
		return 1
	}
	return 0
}

// friendlyErr turns internal errors into what to do next, in plain words.
func friendlyErr(err error) string {
	var multi core.ErrMultiple
	switch {
	case errors.Is(err, core.ErrNoDevice):
		return "No AK820 keyboard found.\n  Plug it in with the USB-C cable and slide the switch on the back to USB.\n  (Bluetooth and the 2.4G dongle cannot control the screen.)\n  Run `ak820 setup` to check this computer."
	case errors.Is(err, syscall.ENODEV):
		return "The keyboard disappeared while talking to it.\n  Check the cable and that the switch on the back is on USB, then try again."
	case errors.Is(err, hidraw.ErrPermission):
		return "This computer is not letting ak820 use the keyboard yet.\n  Run `ak820 setup` and follow the steps, then unplug and replug the keyboard."
	case errors.Is(err, core.ErrUnknownLayout):
		return "Your keyboard was found, but it talks in a way ak820 does not know yet.\n  Run `ak820 report --issue` and paste the result into a new issue so support can be added."
	case errors.As(err, &multi), errors.Is(err, core.ErrUnsupported):
		return err.Error()
	case strings.Contains(err.Error(), "no reply"), strings.Contains(err.Error(), "not answering"), strings.Contains(err.Error(), "timed out"):
		return "error: " + err.Error() + "\n  The keyboard did not answer. Check the cable and that the switch on the back is on USB, then try again."
	}
	return "error: " + err.Error()
}

// ---- list / report ----------------------------------------------------------

func (a *app) cmdList(md, issue bool) error {
	all, err := a.c.Devices()
	if err != nil {
		return err
	}
	switch {
	case issue:
		fmt.Fprint(a.out, core.IssueReport(all, version, runtime.GOOS+"/"+runtime.GOARCH+" "+kernel()))
	case md:
		fmt.Fprint(a.out, core.Report(all))
	default:
		for _, i := range all {
			d := i.Descriptor
			fmt.Fprintf(a.out, "%s  if%d  %04x:%04x  page 0x%04X  in %4d  out %4d  feat %3d  rw=%v  %s\n",
				i.Node, i.Iface, i.VID, i.PID, d.Page, d.InBytes(), d.OutBytes(), d.FeatBytes(), i.Accessible(), i.Name)
		}
	}
	return nil
}

func (a *app) cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	issue := fs.Bool("issue", false, "paste-ready bug report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return a.cmdList(true, *issue)
}

func kernel() string {
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(b))
}

// ---- setup --------------------------------------------------------------------

func (a *app) cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	printRule := fs.Bool("print-rule", false, "print the udev rule and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *printRule {
		fmt.Fprint(a.out, setup.Rule)
		return nil
	}
	all, err := a.c.Devices()
	in := setup.Input{FindErr: err, RuleDirs: a.ruleDirs, Prog: a.name()}
	if errors.Is(err, core.ErrNoDevice) {
		in.FindErr = nil // handled by the default "plug it in" advice
	}
	if err == nil {
		in.Found, in.Layout = len(all), core.Layout(all)
		in.Name = fmt.Sprintf("%s %04x:%04x", strings.TrimPrefix(all[0].Name, "SONiX "), all[0].VID, all[0].PID)
		for _, i := range all {
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

// errExit makes a command exit non-zero without printing another message.
var errExit = errors.New("")

// ---- probe ------------------------------------------------------------------

func (a *app) cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	yes := fs.Bool("yes", false, "actually send (read-only GET_DEVICE_INFO) to the keyboard")
	logf := fs.String("log", "PROBE-LOG.md", "append results here")
	feat := fs.Bool("feature", true, "also try a read-only GET_FEATURE on interfaces that have one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	res, err := a.c.Probe(!*yes, *feat)
	if err != nil {
		return err
	}
	fmt.Fprint(a.out, res.Text)
	if *yes {
		if f, err := os.OpenFile(*logf, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			f.WriteString(res.Text)
			f.Close()
			fmt.Fprintln(a.out, "\nappended to", *logf)
		}
	}
	return nil
}

// ---- info / upload / builtin / time ------------------------------------------

func (a *app) cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	yes := fs.Bool("yes", false, "send to the keyboard")
	node := fs.String("node", "", "hidraw node (e.g. hidraw8); default: detected legacy config interface")
	plen := fs.Int("len", 0, "output report length (default: from descriptor)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := a.c.Info(core.Target{Node: *node, Len: *plen}, !*yes)
	if r != nil && r.Dry {
		for _, f := range r.Frames {
			fmt.Fprintf(a.out, "dry run: would write %s: %s\n", r.Node, f)
		}
		return err
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%+v\n", *r.Info)
	return nil
}

func (a *app) cmdUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	yes := fs.Bool("yes", false, "send to the keyboard")
	node := fs.String("node", "", "data interface (default: detected from the layout)")
	plen := fs.Int("len", 0, "output report length (default: from descriptor)")
	fit := fs.String("fit", "cover", "cover | contain | stretch")
	bg := fs.String("bg", "black", "background: black, white or RRGGBB")
	delay := fs.Int("delay", 0, "override per-frame delay (ms)")
	maxFrames := fs.Int("max-frames", 255, "truncate to this many frames")
	preview := fs.String("preview", "", "write a PNG preview of frame 0 and continue")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ak820 upload [flags] <image>")
	}
	frames, err := img.Load(fs.Arg(0), *fit, img.ParseColor(*bg))
	if err != nil {
		return err
	}
	if *delay > 0 {
		for i := range frames {
			frames[i].DelayMs = *delay
		}
	}
	frames = frames[:min(len(frames), *maxFrames, 255)]
	payload := img.BuildPayload(frames)
	if *preview != "" {
		if err := savePNG(*preview, frames[0].Img); err != nil {
			return err
		}
	}
	t := core.Target{Node: *node, Len: *plen}
	r, err := a.c.Upload(t, payload, !*yes, func(d, n int) { fmt.Fprintf(a.errw, "\r  uploading %3d%%", 100*d/n) })
	if r != nil {
		fmt.Fprintf(a.out, "%d frame(s), %d bytes, %d packets of %d B to %s\n", len(frames), len(payload), r.Packets, r.Len, r.Node)
	}
	if err != nil {
		return err
	}
	if r.Dry {
		fmt.Fprintln(a.out, "dry run: first packet:", r.First)
		if r.Flash {
			fmt.Fprintln(a.out, "(this keyboard saves the picture in its flash memory; it stays after unplugging)")
		} else {
			fmt.Fprintln(a.out, "(note: on firmware 1.16 uploads live in RAM and are lost on unplug / mode change)")
		}
		fmt.Fprintln(a.out, "nothing was sent. Add --yes to send it.")
		return nil
	}
	fmt.Fprintln(a.errw)
	if err := history.Save(a.c.HistoryDir(), payload, fs.Arg(0)); err != nil {
		fmt.Fprintf(a.errw, "warning: the picture was sent, but could not be added to your history: %v\n", err)
	}
	fmt.Fprintln(a.out, "done")
	return nil
}

func (a *app) cmdBuiltin(args []string) error {
	fs := flag.NewFlagSet("builtin", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	yes := fs.Bool("yes", false, "send to the keyboard")
	node := fs.String("node", "", "data interface (default: detected from the layout)")
	plen := fs.Int("len", 0, "output report length")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ak820 builtin [flags] <index>")
	}
	idx, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("the animation number must be a number, got %q", fs.Arg(0))
	}
	r, err := a.c.Builtin(core.Target{Node: *node, Len: *plen}, idx, !*yes)
	if err != nil {
		return err
	}
	if r.Dry {
		fmt.Fprintf(a.out, "dry run: would write %s: %s\n", r.Node, r.First)
	} else {
		fmt.Fprintln(a.out, "sent")
	}
	return nil
}

func (a *app) cmdTime(args []string) error {
	fs := flag.NewFlagSet("time", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	yes := fs.Bool("yes", false, "send to the keyboard")
	node := fs.String("node", "", "command interface (default: detected)")
	plen := fs.Int("len", 0, "output report length")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := a.c.SetTime(core.Target{Node: *node, Len: *plen}, time.Now(), !*yes)
	if err != nil {
		return err
	}
	if r.Dry {
		fmt.Fprintf(a.out, "dry run: would write %s: %s\n", r.Node, r.First)
		fmt.Fprintln(a.out, "nothing was sent. Add --yes to send it.")
	} else {
		fmt.Fprintln(a.out, "clock set")
	}
	return nil
}

// ---- ui -----------------------------------------------------------------------

func (a *app) cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(a.errw)
	addr := fs.String("addr", "127.0.0.1:0", "listen address (loopback only)")
	noOpen := fs.Bool("no-open", false, "don't open the browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return a.ui(*addr, *noOpen)
}

func (a *app) serveUI(addr string, noOpen bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("--addr must be a loopback address (127.0.0.1 or localhost), got %q", host)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s := &web.Server{
		Core:    a.c,
		Token:   web.NewToken(),
		Version: version,
		AllowedHosts: []string{
			fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("localhost:%d", port),
		},
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/#token=%s", port, s.Token)
	fmt.Fprintln(a.out, "ak820 is running. Open this address in your browser:\n  "+url)
	fmt.Fprintln(a.out, "Nothing is written to the keyboard until you confirm in the browser. Press Ctrl-C to stop.")
	if !noOpen && os.Getenv("DISPLAY")+os.Getenv("WAYLAND_DISPLAY") != "" {
		_ = exec.Command("xdg-open", url).Start()
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && !strings.Contains(err.Error(), "closed") {
		return err
	}
	return nil
}
