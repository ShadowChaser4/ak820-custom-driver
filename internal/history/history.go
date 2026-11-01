// Package history keeps previously uploaded payloads on disk, in the same
// format as the Python tool (YYYYMMDD-HHMMSS_name.bin).
package history

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const Max = 50

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

// Save stores payload and prunes to the newest Max entries.
func Save(dir string, payload []byte, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f := fmt.Sprintf("%s_%s.bin", time.Now().Format("20060102-150405"), safe(name))
	if err := os.WriteFile(filepath.Join(dir, f), payload, 0o644); err != nil {
		return err
	}
	es, _ := List(dir)
	for i := Max; i < len(es); i++ {
		os.Remove(filepath.Join(dir, es[i].ID))
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
func Load(dir, id string) ([]byte, error) {
	es, err := List(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if e.ID == id {
			return os.ReadFile(filepath.Join(dir, e.ID))
		}
	}
	return nil, fmt.Errorf("no history entry %q", id)
}
