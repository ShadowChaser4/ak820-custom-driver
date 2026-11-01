// Command ak820 changes the picture on the AJAZZ / Epomaker AK820 Pro keyboard screen.
//
// Every command that writes to the keyboard needs --yes. Without it the tool prints
// the target and the exact bytes, then stops.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/history"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/drivers"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/setup"
)

// version is stamped at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

// app holds everything a command needs, so tests can swap the pieces.
type app struct {
	finder     *keyboard.Finder
	historyDir string
	out, errw  io.Writer
	ui         func(ctx context.Context, addr string, noOpen bool) error
	ruleDirs   []string
	prog       string // how the user invoked us ("ak820" via the PATH, else e.g. "./ak820"); used in hints
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	a := &app{finder: drivers.System(), historyDir: history.DefaultDir(), out: os.Stdout, errw: os.Stderr, ruleDirs: setup.RuleDirs, prog: os.Args[0]}
	a.ui = a.serveUI
	code := a.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
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

func (a *app) name() string {
	if a.prog == "" {
		return "ak820"
	}
	return a.prog
}

// flags returns a flag set that reports problems instead of exiting the process.
func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.errw)
	return fs
}

func (a *app) run(ctx context.Context, args []string) int {
	if v := os.Getenv("AK820_DEVICE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			a.finder = a.finder.WithDevice(n)
		}
	}
	for len(args) >= 2 && (args[0] == "--device" || args[0] == "-device") {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			fmt.Fprintln(a.errw, "error: --device needs a number like 1 or 2")
			return 2
		}
		a.finder = a.finder.WithDevice(n)
		args = args[2:]
	}
	cmd, rest := "ui", []string(nil)
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "ui":
		err = a.cmdUI(ctx, rest)
	case "setup":
		err = a.cmdSetup(rest)
	case "list":
		err = a.cmdList()
	case "report":
		err = a.cmdReport(rest)
	case "probe":
		err = a.cmdProbe(ctx, rest)
	case "info":
		err = a.cmdInfo(ctx, rest)
	case "upload":
		err = a.cmdUpload(ctx, rest)
	case "builtin":
		err = a.cmdBuiltin(ctx, rest)
	case "time":
		err = a.cmdTime(ctx, rest)
	case "version", "--version", "-v":
		fmt.Fprintf(a.out, "ak820 %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	case "help", "--help", "-h":
		fmt.Fprint(a.out, usageText)
	default:
		fmt.Fprintf(a.errw, "unknown command %q\n\n%s", cmd, usageText)
		return 2
	}
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp): // the flag package already printed the usage
		return 0
	case errors.Is(err, errExit):
		return 1
	}
	fmt.Fprintln(a.errw, advice(err, a.name()))
	return 1
}
