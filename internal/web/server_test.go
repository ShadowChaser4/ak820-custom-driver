package web

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/core"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
)

const host = "127.0.0.1:9999"

type dev struct{ w, o *int }

func (d dev) Write([]byte) error                        { *d.w++; return nil }
func (d dev) ReadTimeout(time.Duration) ([]byte, error) { return nil, nil }
func (d dev) GetFeature(byte, int) ([]byte, error)      { return nil, nil }
func (d dev) SetFeature([]byte, int) error              { *d.w++; return nil }
func (d dev) Close() error                              { return nil }

func rig(t *testing.T, confirmed bool) (*httptest.Server, *Server, *int, *int) {
	t.Helper()
	var writes, opens int
	c := &core.Core{
		Enumerate: func() ([]hidraw.Info, error) {
			// the two vendor interfaces of a real 0c45:800a unit (captured descriptors)
			ff68 := []byte{0x06, 0x68, 0xff, 0x09, 0x61, 0xa1, 0x01, 0x09, 0x62, 0x15, 0x00, 0x26, 0xff, 0x00, 0x95, 0x40,
				0x75, 0x08, 0x81, 0x02, 0x09, 0x63, 0x15, 0x00, 0x26, 0xff, 0x00, 0x96, 0x00, 0x10, 0x75, 0x08, 0x91, 0x02, 0xc0}
			ff13 := []byte{0x06, 0x13, 0xff, 0x09, 0x01, 0xa1, 0x01, 0x15, 0x00, 0x26, 0xff, 0x00, 0x75, 0x08, 0x95, 0x40,
				0x09, 0x02, 0x81, 0x02, 0x09, 0x03, 0x91, 0x02, 0x0a, 0x00, 0xff, 0xb1, 0x02, 0xc0}
			mk := func(node string, iface int, raw []byte) hidraw.Info {
				return hidraw.Info{Node: node, VID: 0x0c45, PID: 0x800a, Iface: iface, Raw: raw, D: hidraw.ParseDescriptor(raw)}
			}
			return []hidraw.Info{mk("/dev/hidraw7", 2, ff68), mk("/dev/hidraw8", 3, ff13)}, nil
		},
		OpenDev: func(hidraw.Info) (core.DevIO, error) {
			opens++
			return dev{&writes, &opens}, nil
		},
		DataDir: t.TempDir(),
	}
	if confirmed {
		c.MarkConnected("/dev/hidraw8")
	}
	s := &Server{Core: c, Token: "tok", AllowedHosts: []string{host}}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, s, &writes, &opens
}

func do(t *testing.T, ts *httptest.Server, method, path string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
	req.Host = host
	req.Header.Set("X-Token", "tok")
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestAuthRules(t *testing.T) {
	ts, _, _, _ := rig(t, false)
	if r := do(t, ts, "GET", "/api/devices", nil, map[string]string{"X-Token": ""}); r.StatusCode != 401 {
		t.Errorf("no token: %d", r.StatusCode)
	}
	if r := do(t, ts, "GET", "/api/devices", nil, map[string]string{"X-Token": "wrong"}); r.StatusCode != 401 {
		t.Errorf("wrong token: %d", r.StatusCode)
	}
	if r := do(t, ts, "GET", "/api/devices", nil, map[string]string{"Origin": "http://evil.example"}); r.StatusCode != 403 {
		t.Errorf("foreign origin: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/devices", nil)
	req.Host = "evil.example:9999" // DNS rebinding
	req.Header.Set("X-Token", "tok")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 403 {
		t.Errorf("foreign host: %d", r.StatusCode)
	}
	if r := do(t, ts, "GET", "/api/devices", nil, nil); r.StatusCode != 200 {
		t.Errorf("good request: %d", r.StatusCode)
	}
	if r := do(t, ts, "GET", "/", nil, map[string]string{"X-Token": ""}); r.StatusCode != 200 {
		t.Errorf("index must load without a token: %d", r.StatusCode)
	}
}

func pngBody(t *testing.T) ([]byte, string) {
	im := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for i := 0; i < len(im.Pix); i += 4 {
		im.Pix[i], im.Pix[i+3] = 255, 255
	}
	var pb bytes.Buffer
	_ = png.Encode(&pb, im)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "red.png")
	fw.Write(pb.Bytes())
	mw.WriteField("fit", "cover")
	mw.Close()
	return b.Bytes(), mw.FormDataContentType()
}

func TestDryEndpointsNeverWrite(t *testing.T) {
	ts, _, writes, opens := rig(t, true)
	body, ct := pngBody(t)
	r := do(t, ts, "POST", "/api/preview", body, map[string]string{"Content-Type": ct})
	var pv struct {
		ID     string
		Frames []struct{ URL string }
	}
	json.NewDecoder(r.Body).Decode(&pv)
	if r.StatusCode != 200 || pv.ID == "" || len(pv.Frames) != 1 || !strings.HasPrefix(pv.Frames[0].URL, "data:image/png") {
		t.Fatalf("preview: %d %+v", r.StatusCode, pv)
	}
	calls := map[string]string{
		"/api/probe":   `{"dry":true,"feature":true}`,
		"/api/info":    `{"dry":true,"node":"hidraw8"}`,
		"/api/upload":  `{"dry":true,"previewId":"` + pv.ID + `"}`,
		"/api/builtin": `{"dry":true,"index":2}`,
		"/api/time":    `{"dry":true}`,
	}
	for p, b := range calls {
		want := 200
		if p == "/api/builtin" {
			want = http.StatusNotImplemented // sonix has no built-in animations
		}
		if r := do(t, ts, "POST", p, []byte(b), map[string]string{"Content-Type": "application/json"}); r.StatusCode != want {
			t.Errorf("%s dry: %d, want %d", p, r.StatusCode, want)
		}
	}
	if *writes != 0 || *opens != 0 {
		t.Fatalf("dry runs opened %d / wrote %d", *opens, *writes)
	}
}

// Everything that changes the screen is refused until the keyboard is connected...
func TestRealWritesRefusedUntilConnected(t *testing.T) {
	ts, _, writes, opens := rig(t, false)
	for _, c := range []struct{ p, b string }{
		{"/api/builtin", `{"index":1}`}, {"/api/history/restore", `{"id":"x"}`}, {"/api/upload", `{"previewId":"x"}`},
	} {
		r := do(t, ts, "POST", c.p, []byte(c.b), nil)
		if r.StatusCode == 200 {
			t.Errorf("%s accepted a real write before connecting", c.p)
		}
	}
	if *writes != 0 || *opens != 0 {
		t.Fatalf("opened %d wrote %d", *opens, *writes)
	}
}

// ...but the Connect button (which sets the clock) must itself be allowed. Regression: it was
// gated by "connected", so it could never succeed ("the keyboard is not connected yet").
func TestConnectButtonWorksFromAFreshStart(t *testing.T) {
	ts, _, writes, _ := rig(t, false)
	devices := func() bool {
		r := do(t, ts, "GET", "/api/devices", nil, nil)
		var d struct{ Ready bool }
		json.NewDecoder(r.Body).Decode(&d)
		return d.Ready
	}
	if devices() {
		t.Fatal("a fresh start must not be connected")
	}
	// what the page does: dry run, then the real call
	if r := do(t, ts, "POST", "/api/time", []byte(`{"dry":true}`), map[string]string{"Content-Type": "application/json"}); r.StatusCode != 200 {
		t.Fatalf("dry: %d", r.StatusCode)
	}
	if *writes != 0 {
		t.Fatal("dry run wrote to the keyboard")
	}
	r := do(t, ts, "POST", "/api/time", []byte(`{"dry":false}`), map[string]string{"Content-Type": "application/json"})
	if r.StatusCode != 200 {
		t.Fatalf("the Connect button was refused with %d", r.StatusCode)
	}
	if *writes != 4 {
		t.Errorf("expected the 4 clock frames, got %d writes", *writes)
	}
	if !devices() {
		t.Fatal("still not ready after the Connect button succeeded")
	}
	// now the gate is open: a write that used to be refused with 409 gets as far as its own validation
	r = do(t, ts, "POST", "/api/history/restore", []byte(`{"id":"nope"}`), map[string]string{"Content-Type": "application/json"})
	if r.StatusCode == http.StatusConflict {
		t.Error("still refused as 'not connected' after connecting")
	}
}

func TestHistoryRejectsPathIDs(t *testing.T) {
	ts, _, _, _ := rig(t, true)
	for _, id := range []string{"../../etc/passwd", "%2e%2e%2fx"} {
		if r := do(t, ts, "GET", "/api/history/thumb?id="+id, nil, nil); r.StatusCode != 404 {
			t.Errorf("id %q: %d", id, r.StatusCode)
		}
	}
}

func TestOversizeUploadRejected(t *testing.T) {
	ts, _, _, _ := rig(t, false)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "big.png")
	fw.Write(make([]byte, maxUpload+1024))
	mw.Close()
	r := do(t, ts, "POST", "/api/preview", b.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()})
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("got %d", r.StatusCode)
	}
}

func TestGalleryEndpoints(t *testing.T) {
	ts, _, writes, opens := rig(t, true)
	r := do(t, ts, "GET", "/api/gallery", nil, nil)
	var items []struct {
		ID     string
		Frames int
	}
	json.NewDecoder(r.Body).Decode(&items)
	if r.StatusCode != 200 || len(items) != 4 {
		t.Fatalf("gallery: %d %+v", r.StatusCode, items)
	}
	if r := do(t, ts, "GET", "/api/gallery/thumb?id="+items[0].ID, nil, nil); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" {
		t.Errorf("thumb: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	for _, id := range []string{"../../etc/passwd", "nope", ""} {
		if r := do(t, ts, "GET", "/api/gallery/thumb?id="+id, nil, nil); r.StatusCode != 404 {
			t.Errorf("thumb %q: %d", id, r.StatusCode)
		}
	}
	r = do(t, ts, "POST", "/api/gallery/preview", []byte(`{"id":"`+items[0].ID+`","fit":"cover","bg":"000000"}`), map[string]string{"Content-Type": "application/json"})
	var pv struct {
		ID     string
		Frames []struct{ URL string }
	}
	json.NewDecoder(r.Body).Decode(&pv)
	if r.StatusCode != 200 || pv.ID == "" || len(pv.Frames) != items[0].Frames {
		t.Fatalf("preview: %d id=%q frames=%d want %d", r.StatusCode, pv.ID, len(pv.Frames), items[0].Frames)
	}
	if *writes != 0 || *opens != 0 {
		t.Fatal("gallery endpoints must not touch the device")
	}
}

func TestDevicesHasFriendlyFields(t *testing.T) {
	ts, _, _, _ := rig(t, false)
	r := do(t, ts, "GET", "/api/devices", nil, nil)
	var d struct {
		Friendly struct{ Title, Message, Level string }
		Ready    bool
		Profile  string
		Features map[string]bool
	}
	json.NewDecoder(r.Body).Decode(&d)
	if d.Friendly.Title == "" || d.Friendly.Level == "" || d.Ready || d.Profile == "" || d.Features == nil {
		t.Fatalf("%+v", d)
	}
}
