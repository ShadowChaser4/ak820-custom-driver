package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var timestamp = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d[-+Z][\d:]*`)

// normalise replaces what differs on every run, so the rest can be compared exactly.
func normalise(t *testing.T, raw []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %q", raw)
	}
	var walk func(any) any
	walk = func(x any) any {
		switch n := x.(type) {
		case map[string]any:
			for k, e := range n {
				switch k {
				case "id":
					n[k] = "<id>"
				case "url":
					n[k] = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(e.(string))))
				default:
					n[k] = walk(e)
				}
			}
		case []any:
			for i, e := range n {
				n[i] = walk(e)
			}
		case string:
			return timestamp.ReplaceAllString(n, "<time>")
		}
		return x
	}
	out, err := json.MarshalIndent(walk(v), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func compare(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/web -update)", err)
	}
	if !bytes.Equal(want, got) {
		t.Errorf("%s: the page depends on this response and it changed.\n--- want\n%s--- got\n%s", name, want, got)
	}
}

// The frontend is written against these responses. They must not drift.
func TestAPIResponsesAreStable(t *testing.T) {
	ts, _, _, _ := rig(t, false)
	body, ct := pngBody(t)
	json_ := map[string]string{"Content-Type": "application/json"}

	r := do(t, ts, "POST", "/api/preview", body, map[string]string{"Content-Type": ct})
	raw, _ := io.ReadAll(r.Body)
	compare(t, "preview", normalise(t, raw))
	var pv struct{ ID string }
	json.Unmarshal(raw, &pv)

	for _, c := range []struct {
		name, method, path, body string
		code                     int
	}{
		{"devices", "GET", "/api/devices", "", 200},
		{"gallery", "GET", "/api/gallery", "", 200},
		{"history-empty", "GET", "/api/history", "", 200},
		{"report", "GET", "/api/report", "", 200},
		{"probe-dry", "POST", "/api/probe", `{"dry":true,"feature":true}`, 200},
		{"clock-dry-shape", "POST", "/api/builtin", `{"dry":true,"index":1}`, 501},
		{"upload-dry", "POST", "/api/upload", `{"dry":true,"previewId":"` + pv.ID + `"}`, 200},
		{"upload-not-connected", "POST", "/api/upload", `{"previewId":"` + pv.ID + `"}`, 409},
		{"upload-unknown-preview", "POST", "/api/upload", `{"previewId":"nope"}`, 404},
		{"info-explicit-dry", "POST", "/api/info", `{"dry":true,"node":"hidraw8"}`, 200},
	} {
		r := do(t, ts, c.method, c.path, []byte(c.body), json_)
		raw, _ := io.ReadAll(r.Body)
		if r.StatusCode != c.code {
			t.Errorf("%s: status %d, want %d: %s", c.name, r.StatusCode, c.code, raw)
			continue
		}
		compare(t, c.name, normalise(t, raw))
	}
}

// Field names of the clock dry run; the frames themselves contain the current time.
func TestClockDryRunShape(t *testing.T) {
	ts, _, _, _ := rig(t, false)
	r := do(t, ts, "POST", "/api/time", []byte(`{"dry":true}`), map[string]string{"Content-Type": "application/json"})
	var d map[string]any
	json.NewDecoder(r.Body).Decode(&d)
	for _, k := range []string{"dry", "node", "len", "packets", "first", "steps", "flash"} {
		if _, ok := d[k]; !ok {
			t.Errorf("clock dry run lacks %q: %v", k, d)
		}
	}
}
