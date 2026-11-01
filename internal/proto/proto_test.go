package proto

import (
	"bytes"
	"testing"
	"time"
)

func TestInfoChunks32(t *testing.T) {
	c := Chunks(32, CmdInfo, nil, 48)
	if len(c) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(c))
	}
	if !bytes.Equal(c[0][:8], []byte{0xAA, 0x10, 24, 0, 0, 0, 0, 0}) {
		t.Errorf("chunk0 % x", c[0][:8])
	}
	if !bytes.Equal(c[1][:8], []byte{0xAA, 0x10, 24, 24, 0, 0, 1, 0}) {
		t.Errorf("chunk1 % x", c[1][:8])
	}
}

func TestInfoChunk64And4096(t *testing.T) {
	for _, n := range []int{64, 4096} {
		c := Chunks(n, CmdInfo, nil, 48)
		if len(c) != 1 || len(c[0]) != n || !bytes.Equal(c[0][:8], []byte{0xAA, 0x10, 48, 0, 0, 0, 1, 0}) {
			t.Errorf("len %d: %d chunks, % x", n, len(c), c[0][:8])
		}
	}
}

func TestAnimChunks(t *testing.T) {
	payload := make([]byte, 4096+10)
	c := AnimChunks(4104, payload)
	if len(c) != 2 {
		t.Fatalf("got %d", len(c))
	}
	if !bytes.Equal(c[1][:8], []byte{0xAA, 0x50, 1, 0, 2, 0, 0x50, 0x06}) {
		t.Errorf("% x", c[1][:8])
	}
}

type fake struct {
	replies [][]byte
	writes  [][]byte
}

func (f *fake) Write(p []byte) error { f.writes = append(f.writes, p); return nil }
func (f *fake) ReadTimeout(time.Duration) ([]byte, error) {
	if len(f.replies) == 0 || len(f.writes) == 0 {
		return nil, nil
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r, nil
}

func TestTransactSkipsOtherReplies(t *testing.T) {
	f := &fake{replies: [][]byte{{0x55, 0x99, 0}, {0x55, 0x10, 1}}}
	p := &Port{C: f, Len: 64, Retries: 1}
	r, err := p.Transact(make([]byte, 64), CmdInfo, 50*time.Millisecond, 1)
	if err != nil || r[1] != 0x10 {
		t.Fatalf("%v % x", err, r)
	}
}

func TestTransactTimeout(t *testing.T) {
	f := &fake{}
	p := &Port{C: f, Len: 64}
	if _, err := p.Transact(make([]byte, 64), CmdInfo, 10*time.Millisecond, 2); err == nil {
		t.Fatal("expected timeout")
	}
	if len(f.writes) != 3 {
		t.Errorf("want 3 attempts, got %d", len(f.writes))
	}
}
