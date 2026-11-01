// Package sonix implements the protocol of AK820 Pro units whose firmware
// exposes a 64-byte feature-report control interface (usage page 0xFF13) and a
// 4096-byte data interface (usage page 0xFF68).
//
// Source: the firmware disassembly notes in github.com/ErvinUrban-Bratislava/ak820pro-linux
// (MIT, FINDINGS.md). Not yet verified against hardware by this project.
//
// Unlike the legacy 0xAA/0x55 protocol, uploads here are committed to the
// keyboard's SPI flash, so they persist across unplugging.
package sonix

import (
	"errors"
	"fmt"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/img"
)

const (
	ChunkSize  = 4096
	MaxFrames  = 255
	cmdDelay   = 50 * time.Millisecond
	cfgDelay   = 350 * time.Millisecond
	ackTimeout = time.Second
)

// sleep is a variable so tests can skip the real device delays.
var sleep = time.Sleep

var ackMagic = []byte{0x01, 0x5A, 0x02}

// Ctrl is the 64-byte feature-report control pipe (hidraw node, page 0xFF13).
type Ctrl interface {
	SetFeature(payload []byte) error // report id 0, 64 bytes
	GetFeature() ([]byte, error)     // 64 bytes
}

// Data is the 4096-byte data pipe (hidraw node, page 0xFF68).
type Data interface {
	WriteData(chunk []byte) error // one raw write (the report-id byte is added by the caller's adapter)
	ReadAck(t time.Duration) ([]byte, error)
}

// Cmd builds a control packet: 04 <cmd> <sub> ..., byte 8 = 1 when enable.
func Cmd(cmd, sub byte, enable bool) []byte {
	p := make([]byte, 64)
	p[0], p[1], p[2] = 0x04, cmd, sub
	if enable {
		p[8] = 1
	}
	return p
}

// ClockFrames returns the control packets that set the screen clock, in order.
// Each is followed by a handshake read, except the second (TIME_DATA).
func ClockFrames(now time.Time) [][]byte {
	d := make([]byte, 64)
	d[1], d[2] = 0x01, 0x5A
	d[3], d[4], d[5] = byte(now.Year()-2000), byte(now.Month()), byte(now.Day())
	d[6], d[7], d[8] = byte(now.Hour()), byte(now.Minute()), byte(now.Second())
	d[10] = 0x04
	d[62], d[63] = 0xAA, 0x55
	return [][]byte{Cmd(0x18, 0, true), Cmd(0x28, 0, true), d, Cmd(0x02, 0, false)}
}

func (p *Port) handshake(post time.Duration) error {
	sleep(cmdDelay)
	_, err := p.C.GetFeature()
	if err != nil {
		p.fails++
		if p.fails >= 2 {
			return fmt.Errorf("the keyboard's control channel is not answering: %w", err)
		}
	} else {
		p.fails = 0
	}
	sleep(post)
	return nil
}

// Port drives both pipes.
type Port struct {
	C     Ctrl
	D     Data
	fails int
}

// SyncClock sets the keyboard's clock. This writes to the keyboard.
func (p *Port) SyncClock(now time.Time) error {
	f := ClockFrames(now)
	if err := p.C.SetFeature(f[0]); err != nil { // START
		return err
	}
	sleep(cmdDelay)
	if err := p.C.SetFeature(f[1]); err != nil { // TIME_CFG
		return err
	}
	if err := p.handshake(cmdDelay); err != nil {
		return err
	}
	if err := p.C.SetFeature(f[2]); err != nil { // TIME_DATA
		return err
	}
	sleep(cmdDelay)
	if err := p.C.SetFeature(f[3]); err != nil { // SAVE
		return err
	}
	return p.handshake(cmdDelay)
}

// ---- payload ----------------------------------------------------------------

// BuildPayload makes the flash payload: 256-byte header (frame count, then one
// delay byte per frame in 5 ms units, minimum 16) + RGB565-LE frames, zero-padded
// to whole 4096-byte chunks. Returns the payload and the chunk count.
func BuildPayload(frames []img.Frame) ([]byte, int, error) {
	if len(frames) == 0 {
		return nil, 0, errors.New("no frames")
	}
	if len(frames) > MaxFrames {
		return nil, 0, fmt.Errorf("%d frames: the keyboard supports at most %d", len(frames), MaxFrames)
	}
	hdr := make([]byte, 256)
	hdr[0] = byte(len(frames))
	for i, f := range frames {
		hdr[1+i] = byte(min(255, max(16, f.DelayMs/5)))
	}
	out := hdr
	for _, f := range frames {
		out = append(out, img.RGB565LE(f.Img)...)
	}
	chunks := (len(out) + ChunkSize - 1) / ChunkSize
	out = append(out, make([]byte, chunks*ChunkSize-len(out))...)
	return out, chunks, nil
}

// ---- upload -----------------------------------------------------------------

// drain discards stale acknowledgements, stopping early when the pipe is quiet.
// The number of reads is bounded in case a faulty pipe never goes quiet.
func (p *Port) drain() error {
	for i := 0; i < 64; i++ {
		r, err := p.D.ReadAck(10 * time.Millisecond)
		if err != nil {
			return err
		}
		if r == nil {
			return nil
		}
	}
	return nil
}

func (p *Port) sendChunk(chunk []byte) ([]byte, error) {
	if err := p.D.WriteData(chunk); err != nil {
		return nil, err
	}
	ack, err := p.D.ReadAck(ackTimeout)
	if err != nil {
		return nil, err
	}
	if ack != nil && (len(ack) < 3 || ack[0] != ackMagic[0] || ack[1] != ackMagic[1] || ack[2] != ackMagic[2]) {
		return nil, fmt.Errorf("unexpected reply % x: stopping with the keyboard in an unknown state", ack[:min(4, len(ack))])
	}
	return ack, nil
}

func imageCfg(chunks int) []byte {
	p := Cmd(0x72, 0x03, false)
	p[8], p[9] = byte(chunks), byte(chunks>>8)
	return p
}

// Align locks the firmware's 64-packet ring counter to 0 (see FINDINGS.md §2).
// It writes dummy zero data and a SAVE, so it overwrites the current animation.
func (p *Port) Align() (bool, error) {
	if err := p.drain(); err != nil {
		return false, err
	}
	if err := p.C.SetFeature(Cmd(0x18, 0, false)); err != nil {
		return false, err
	}
	if err := p.handshake(cmdDelay); err != nil {
		return false, err
	}
	if err := p.C.SetFeature(imageCfg(3)); err != nil {
		return false, err
	}
	if err := p.handshake(cfgDelay); err != nil {
		return false, err
	}
	if err := p.drain(); err != nil {
		return false, err
	}
	pkt := make([]byte, 64)
	burst := 0
	for b := 1; b <= 8 && burst == 0; b++ {
		for i := 0; i < 8; i++ {
			if err := p.D.WriteData(pkt); err != nil {
				return false, err
			}
		}
		r, err := p.D.ReadAck(220 * time.Millisecond)
		if err != nil {
			return false, err
		}
		if r != nil {
			burst = b
		}
	}
	for i := 0; i < 56; i++ {
		if err := p.D.WriteData(pkt); err != nil {
			return false, err
		}
	}
	fine := 0
	for q := 1; q <= 8 && fine == 0; q++ {
		if err := p.D.WriteData(pkt); err != nil {
			return false, err
		}
		r, err := p.D.ReadAck(220 * time.Millisecond)
		if err != nil {
			return false, err
		}
		if r != nil {
			fine = q
		}
	}
	ack3, err := p.sendChunk(make([]byte, ChunkSize))
	if err != nil {
		return false, err
	}
	sleep(100 * time.Millisecond)
	if err := p.C.SetFeature(Cmd(0x02, 0, false)); err != nil {
		return false, err
	}
	if err := p.handshake(100 * time.Millisecond); err != nil {
		return false, err
	}
	if err := p.drain(); err != nil {
		return false, err
	}
	return burst > 0 && fine > 0 && ack3 != nil, nil
}

// Upload streams the payload. It only sends the final SAVE after every chunk
// was acknowledged, so a partial transfer is never committed.
func (p *Port) Upload(payload []byte, chunks int, progress func(done, total int)) error {
	if len(payload) != chunks*ChunkSize {
		return fmt.Errorf("payload is %d bytes, expected %d chunks of %d", len(payload), chunks, ChunkSize)
	}
	ok, err := p.Align()
	if err != nil {
		return err
	}
	if !ok {
		if ok, err = p.Align(); err != nil {
			return err
		}
		if !ok {
			return errors.New("the keyboard's data channel would not line up after 2 tries; replug the cable and try again")
		}
	}
	if err := p.C.SetFeature(Cmd(0x18, 0, false)); err != nil {
		return err
	}
	if err := p.handshake(cmdDelay); err != nil {
		return err
	}
	if err := p.C.SetFeature(imageCfg(chunks)); err != nil {
		return err
	}
	if err := p.handshake(cfgDelay); err != nil {
		return err
	}
	if err := p.drain(); err != nil {
		return err
	}
	deadline := time.Now().Add(30*time.Second + time.Duration(chunks)*2*ackTimeout)
	acks := 0
	for i := 0; i < chunks; i++ {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %d of %d parts; nothing was saved", acks, chunks)
		}
		ack, err := p.sendChunk(payload[i*ChunkSize : (i+1)*ChunkSize])
		if err != nil {
			return err
		}
		if ack != nil {
			acks++
		}
		if progress != nil {
			progress(i+1, chunks)
		}
	}
	if acks != chunks {
		return fmt.Errorf("the keyboard confirmed only %d of %d parts; nothing was saved", acks, chunks)
	}
	sleep(100 * time.Millisecond)
	if err := p.C.SetFeature(Cmd(0x02, 0, false)); err != nil {
		return err
	}
	if err := p.handshake(100 * time.Millisecond); err != nil {
		return err
	}
	_ = p.drain() // the picture is saved; a failure here changes nothing for the caller
	return nil
}
