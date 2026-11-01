// Package history keeps previously uploaded animations on disk, in the same format
// as the Python tool (YYYYMMDD-HHMMSS_name.bin). That format caps frame delays at
// 510 ms, which only affects animations restored from history.
package history

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/legacy"
)

// Max is how many entries are kept.
const Max = 50

// DefaultDir is where history lives for this user.
func DefaultDir() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "ak820", "history")
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "ak820", "history")
}

// Entry is one saved animation.
type Entry struct {
	ID     string    `json:"id"` // file name; only ever matched against a directory listing
	Name   string    `json:"name"`
	When   time.Time `json:"when"`
	Frames int       `json:"frames"`
}

func safe(name string) string {
	var b strings.Builder
	for _, r := range filepath.Base(name) {
		if r < 128 && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '_' || r == '-') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// Save stores frames and prunes to the newest Max entries.
func Save(dir string, frames []animation.Frame, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f := fmt.Sprintf("%s_%s.bin", time.Now().Format("20060102-150405"), safe(name))
	if err := os.WriteFile(filepath.Join(dir, f), legacy.Encode(frames), 0o644); err != nil {
		return err
	}
	es, err := List(dir)
	if err != nil {
		return err
	}
	for i := Max; i < len(es); i++ {
		if err := os.Remove(filepath.Join(dir, es[i].ID)); err != nil {
			return err
		}
	}
	return nil
}

// List returns entries, newest first.
func List(dir string) ([]Entry, error) {
	des, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, d := range des {
		n := d.Name()
		if d.IsDir() || !strings.HasSuffix(n, ".bin") || len(n) < 17 {
			continue
		}
		when, err := time.ParseInLocation("20060102-150405", n[:15], time.Local)
		if err != nil {
			continue
		}
		e := Entry{ID: n, Name: strings.TrimSuffix(n[16:], ".bin"), When: when}
		if f, err := os.Open(filepath.Join(dir, n)); err == nil {
			var b [1]byte
			if _, err := f.Read(b[:]); err == nil {
				e.Frames = int(b[0])
			}
			f.Close()
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// Load reads an entry. id is matched against the directory listing, so a
// client-supplied value can never name a path outside dir.
func Load(dir, id string) ([]animation.Frame, error) {
	es, err := List(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if e.ID == id {
			b, err := os.ReadFile(filepath.Join(dir, e.ID))
			if err != nil {
				return nil, err
			}
			return legacy.Decode(b)
		}
	}
	return nil, fmt.Errorf("no history entry %q", id)
}
