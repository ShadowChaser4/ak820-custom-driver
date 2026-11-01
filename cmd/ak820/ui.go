package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/web"
)

func (a *app) cmdUI(ctx context.Context, args []string) error {
	fs := a.flags("ui")
	addr := fs.String("addr", "127.0.0.1:0", "listen address (loopback only)")
	noOpen := fs.Bool("no-open", false, "don't open the browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return a.ui(ctx, *addr, *noOpen)
}

// serveUI runs the web server until ctx is cancelled, then shuts down gracefully.
func (a *app) serveUI(ctx context.Context, addr string, noOpen bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("--addr must be a loopback address (127.0.0.1 or localhost), got %q", host)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("unexpected listener address %v", ln.Addr())
	}
	s := &web.Server{
		Finder:     a.finder,
		HistoryDir: a.historyDir,
		Token:      web.NewToken(),
		Version:    version,
		AllowedHosts: []string{
			fmt.Sprintf("127.0.0.1:%d", tcp.Port), fmt.Sprintf("localhost:%d", tcp.Port),
		},
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/#token=%s", tcp.Port, s.Token)
	fmt.Fprintln(a.out, "ak820 is running. Open this address in your browser:\n  "+url)
	fmt.Fprintln(a.out, "Nothing is written to the keyboard until you confirm in the browser. Press Ctrl-C to stop.")
	if !noOpen && os.Getenv("DISPLAY")+os.Getenv("WAYLAND_DISPLAY") != "" {
		if err := exec.CommandContext(ctx, "xdg-open", url).Start(); err != nil { //nolint:gosec // fixed program; the URL is built above
			fmt.Fprintf(a.errw, "could not open the browser (%v); open the address above yourself\n", err)
		}
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	shutdownErr := make(chan error, 1)
	go func() { //nolint:gosec // shutdown starts after ctx ends, so its timeout cannot derive from ctx
		<-ctx.Done()
		// Requests share ctx, so a running upload has already been told to stop.
		grace, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr <- srv.Shutdown(grace)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-shutdownErr
}
