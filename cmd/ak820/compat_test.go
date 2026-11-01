package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var (
	tempPath = regexp.MustCompile(`/tmp/\S*?/(hidraw\d|pic\.png)`)
	stamp    = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d[-+Z][\d:]*`)
)

func compareText(t *testing.T, name, got string) {
	t.Helper()
	got = stamp.ReplaceAllString(tempPath.ReplaceAllString(got, "$1"), "<time>")
	path := filepath.Join("testdata", name+".txt")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./cmd/ak820 -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s changed.\n--- want\n%s--- got\n%s", name, want, got)
	}
}

// What people see in the terminal must not drift during refactors.
func TestCLIOutputIsStable(t *testing.T) {
	pic := testPNG(t)
	unknown := []hidraw.Info{{Node: "/dev/x", VID: 0x0c45, PID: 0x800a, Raw: vdesc(0xFF00, 8, 8, 0), Descriptor: hidraw.ParseDescriptor(vdesc(0xFF00, 8, 8, 0))}}
	for _, c := range []struct {
		name string
		ifs  []hidraw.Info
		args []string
		code int
	}{
		{"time-dry", sonixUnit(t, "1-1", 0x800A), []string{"time"}, 0},
		{"upload-dry", sonixUnit(t, "1-1", 0x800A), []string{"upload", pic}, 0},
		{"probe-dry", sonixUnit(t, "1-1", 0x800A), []string{"probe"}, 0},
		{"list", sonixUnit(t, "1-1", 0x800A), []string{"list"}, 0},
		{"report", sonixUnit(t, "1-1", 0x800A), []string{"report"}, 0},
		{"info-sonix", sonixUnit(t, "1-1", 0x800A), []string{"info", "--yes"}, 1},
		{"builtin-sonix", sonixUnit(t, "1-1", 0x800A), []string{"builtin", "1"}, 1},
		{"no-keyboard", nil, []string{"list"}, 1},
		{"unknown-layout", unknown, []string{"time", "--yes"}, 1},
		{"setup-no-rule", sonixUnit(t, "1-1", 0x800A), []string{"setup"}, 1},
		{"setup-no-keyboard", nil, []string{"setup"}, 1},
		{"two-keyboards", append(sonixUnit(t, "1-1", 0x800A), sonixUnit(t, "1-2", 0x800A)...), []string{"list"}, 1},
		{"bad-fit", sonixUnit(t, "1-1", 0x800A), []string{"upload", "--fit", "zoom", pic}, 1},
		{"help", nil, []string{"help"}, 0},
	} {
		r := newRig(t, c.ifs)
		if code := r.a.run(context.Background(), c.args); code != c.code {
			t.Errorf("%s: exit %d, want %d: %s %s", c.name, code, c.code, r.out, r.err)
		}
		compareText(t, c.name, "stdout:\n"+r.out.String()+"stderr:\n"+strings.ReplaceAll(r.err.String(), "\r", ""))
	}
}
