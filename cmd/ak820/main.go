// ak820 — drive the AJAZZ / Epomaker AK820 Pro TFT screen over hidraw.
//
// Safety: every command that writes to a HID node needs --yes. Without it the
// tool prints the target node and the exact bytes, then exits.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"ak820/internal/core"
	"ak820/internal/history"
	"ak820/internal/img"
	"ak820/internal/web"
)

var c = core.New()

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "list":
		err = cmdList(false)
	case "report":
		err = cmdList(true)
	case "probe":
		err = cmdProbe(args)
	case "info":
		err = cmdInfo(args)
	case "upload":
		err = cmdUpload(args)
	case "builtin":
		err = cmdBuiltin(args)
	case "time":
		err = cmdTime(args)
	case "ui":
		err = cmdUI(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ak820 <command> [flags]

  list      show HID interfaces (sysfs only, no device I/O)
  report    same, as Markdown for a bug report
  probe     send the read-only GET_DEVICE_INFO to each candidate interface (needs --yes)
  info      GET_DEVICE_INFO on a chosen interface (needs --yes)
  upload    send an image/GIF to the screen (needs --yes)
  builtin   select a built-in animation by index (needs --yes)
  time      sync the screen clock (needs --yes)
  ui        start the local web UI on 127.0.0.1

Commands that write to the keyboard print what they would send and exit unless --yes is given.
`)
	os.Exit(2)
}

// ---- list / report ----------------------------------------------------------

func cmdList(md bool) error {
	all, err := c.Devices()
	if err != nil {
		return err
	}
	if md {
		fmt.Print(core.Report(all))
		return nil
	}
	for _, i := range all {
		d := i.D
		fmt.Printf("%s  if%d  %04x:%04x  page 0x%04X  in %4d  out %4d  feat %3d  rw=%v  %s\n",
			i.Node, i.Iface, i.VID, i.PID, d.Page, d.InBytes(), d.OutBytes(), d.FeatBytes(), i.Accessible(), i.Name)
	}
	return nil
}

// ---- probe ------------------------------------------------------------------

func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	yes := fs.Bool("yes", false, "actually send (read-only GET_DEVICE_INFO) to the keyboard")
	logf := fs.String("log", "PROBE-LOG.md", "append results here")
	feat := fs.Bool("feature", true, "also try a read-only GET_FEATURE on interfaces that have one")
	fs.Parse(args)
	res, err := c.Probe(!*yes, *feat)
	if err != nil {
		return err
	}
	fmt.Print(res.Text)
	if *yes {
		if f, err := os.OpenFile(*logf, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			f.WriteString(res.Text)
			f.Close()
			fmt.Println("\nappended to", *logf)
		}
	}
	return nil
}

// ---- info / upload / builtin / time ------------------------------------------

func cmdInfo(args []string) error {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	yes := fs.Bool("yes", false, "send to the keyboard")
	node := fs.String("node", "", "hidraw node (e.g. hidraw8); default: detected legacy config interface")
	plen := fs.Int("len", 0, "output report length (default: from descriptor)")
	fs.Parse(args)
	r, err := c.Info(core.Target{Node: *node, Len: *plen}, !*yes)
	if r != nil && r.Dry {
		for _, f := range r.Frames {
			fmt.Printf("dry run: would write %s: %s\n", r.Node, f)
		}
		return err
	}
	if err != nil {
		return err
	}
	fmt.Printf("%+v\n", *r.Info)
	return nil
}

func cmdUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ExitOnError)
	yes := fs.Bool("yes", false, "send to the keyboard")
	node := fs.String("node", "", "data interface (default: detected from the layout)")
	plen := fs.Int("len", 0, "output report length (default: from descriptor)")
	fit := fs.String("fit", "cover", "cover | contain | stretch")
	bg := fs.String("bg", "black", "background: black, white or RRGGBB")
	delay := fs.Int("delay", 0, "override per-frame delay (ms)")
	maxFrames := fs.Int("max-frames", 255, "truncate to this many frames")
	preview := fs.String("preview", "", "write a PNG preview of frame 0 and continue")
	fs.Parse(args)
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
	r, err := c.Upload(t, payload, !*yes, func(d, n int) { fmt.Fprintf(os.Stderr, "\r  uploading %3d%%", 100*d/n) })
	if r != nil {
		fmt.Printf("%d frame(s), %d bytes, %d packets of %d B to %s\n", len(frames), len(payload), r.Packets, r.Len, r.Node)
	}
	if err != nil {
		return err
	}
	if r.Dry {
		fmt.Println("dry run: first packet:", r.First)
		fmt.Println("(note: on firmware 1.16 uploads live in RAM and are lost on unplug / mode change)")
		return nil
	}
	fmt.Fprintln(os.Stderr)
	_ = history.Save(c.HistoryDir(), payload, fs.Arg(0))
	fmt.Println("done")
	return nil
}

func cmdBuiltin(args []string) error {
	fs := flag.NewFlagSet("builtin", flag.ExitOnError)
	yes := fs.Bool("yes", false, "send to the keyboard")
	node := fs.String("node", "", "data interface (default: detected from the layout)")
	plen := fs.Int("len", 0, "output report length")
	fs.Parse(args)
	var idx int
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ak820 builtin [flags] <index>")
	}
	if _, err := fmt.Sscanf(fs.Arg(0), "%d", &idx); err != nil {
		return err
	}
	r, err := c.Builtin(core.Target{Node: *node, Len: *plen}, idx, !*yes)
	if err != nil {
		return err
	}
	if r.Dry {
		fmt.Printf("dry run: would write %s: %s\n", r.Node, r.First)
	} else {
		fmt.Println("sent")
	}
	return nil
}

func cmdTime(args []string) error {
	fs := flag.NewFlagSet("time", flag.ExitOnError)
	yes := fs.Bool("yes", false, "send to the keyboard")
	node := fs.String("node", "", "command interface (default: detected legacy config interface)")
	plen := fs.Int("len", 0, "output report length")
	fs.Parse(args)
	r, err := c.SetTime(core.Target{Node: *node, Len: *plen}, time.Now(), !*yes)
	if err != nil {
		return err
	}
	if r.Dry {
		fmt.Printf("dry run: would write %s: %s\n", r.Node, r.First)
	} else {
		fmt.Println("clock set")
	}
	return nil
}

// ---- ui -----------------------------------------------------------------------

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:0", "listen address (loopback only)")
	noOpen := fs.Bool("no-open", false, "don't open the browser")
	fs.Parse(args)
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("--addr must be a loopback address (127.0.0.1 or localhost), got %q", host)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s := &web.Server{
		Core:  c,
		Token: web.NewToken(),
		AllowedHosts: []string{
			fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("localhost:%d", port),
		},
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/#token=%s", port, s.Token)
	fmt.Println("ak820 UI:", url)
	fmt.Println("nothing is written to the keyboard until you confirm in the browser; Ctrl-C to stop")
	if !*noOpen && os.Getenv("DISPLAY")+os.Getenv("WAYLAND_DISPLAY") != "" {
		_ = exec.Command("xdg-open", url).Start()
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && !strings.Contains(err.Error(), "closed") {
		return err
	}
	return nil
}
