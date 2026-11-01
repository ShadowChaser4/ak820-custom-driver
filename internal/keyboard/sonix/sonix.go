// Package sonix speaks the protocol of AK820 Pro units whose firmware exposes a
// 64-byte feature-report control interface (usage page 0xFF13) and a 4096-byte
// data interface (usage page 0xFF68).
//
// Source: the firmware disassembly notes in github.com/ErvinUrban-Bratislava/ak820pro-linux
// (MIT, FINDINGS.md). Uploads here are committed to the keyboard's SPI flash, so
// they persist across unplugging.
package sonix

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/animation"
)

// Protocol limits and timings.
const (
	ChunkSize  = 4096
	MaxFrames  = 255
	cmdDelay   = 50 * time.Millisecond
	cfgDelay   = 350 * time.Millisecond
	ackTimeout = time.Second
)

var ackMagic = []byte{0x01, 0x5A, 0x02}

// Control is the 64-byte feature-report control pipe.
type Control interface {
	SetFeature(payload []byte) error
	GetFeature() ([]byte, error)
}

// Data is the 4096-byte data pipe. Read reports a timeout as os.ErrDeadlineExceeded.
type Data interface {
	Write(report []byte) error
	Read(ctx context.Context, timeout time.Duration) ([]byte, error)
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
func ClockFrames(now time.Time) [][]byte {
	d := make([]byte, 64)
	d[1], d[2] = 0x01, 0x5A
	d[3], d[4], d[5] = byte(now.Year()-2000), byte(now.Month()), byte(now.Day())
	d[6], d[7], d[8] = byte(now.Hour()), byte(now.Minute()), byte(now.Second())
	d[10] = 0x04
	d[62], d[63] = 0xAA, 0x55
	return [][]byte{Cmd(0x18, 0, true), Cmd(0x28, 0, true), d, Cmd(0x02, 0, false)}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Port drives both pipes.
type Port struct {
	Control Control
	Data    Data
	fails   int
}

// handshake waits for the firmware to process a command, then reads the feature
// report. One failed read is tolerated; two in a row mean the channel is dead.
func (p *Port) handshake(ctx context.Context, post time.Duration) error {
	if err := sleep(ctx, cmdDelay); err != nil {
		return err
	}
	if _, err := p.Control.GetFeature(); err != nil {
		p.fails++
		if p.fails >= 2 {
			return fmt.Errorf("the keyboard's control channel is not answering: %w", err)
		}
	} else {
		p.fails = 0
	}
	return sleep(ctx, post)
}

// SyncClock sends the clock frames from ClockFrames.
func (p *Port) SyncClock(ctx context.Context, frames [][]byte) error {
	if err := p.Control.SetFeature(frames[0]); err != nil { // START
		return err
	}
	if err := sleep(ctx, cmdDelay); err != nil {
		return err
	}
	if err := p.Control.SetFeature(frames[1]); err != nil { // TIME_CFG
		return err
	}
	if err := p.handshake(ctx, cmdDelay); err != nil {
		return err
	}
	if err := p.Control.SetFeature(frames[2]); err != nil { // TIME_DATA
		return err
	}
	if err := sleep(ctx, cmdDelay); err != nil {
		return err
	}
	if err := p.Control.SetFeature(frames[3]); err != nil { // SAVE
		return err
	}
	return p.handshake(ctx, cmdDelay)
}

// BuildPayload makes the flash payload: 256-byte header (frame count, then one
// delay byte per frame in 5 ms units, minimum 16) + RGB565-LE frames, zero-padded
// to whole 4096-byte chunks. It returns the payload and the chunk count.
func BuildPayload(frames []animation.Frame) ([]byte, int, error) {
	if len(frames) == 0 {
		return nil, 0, errors.New("no frames")
	}
	if len(frames) > MaxFrames {
		return nil, 0, fmt.Errorf("%d frames: the keyboard supports at most %d", len(frames), MaxFrames)
	}
	out := make([]byte, 256, 256+len(frames)*animation.FrameBytes)
	out[0] = byte(len(frames))
	for i, f := range frames {
		out[1+i] = byte(min(255, max(16, f.DelayMs/5)))
	}
	for _, f := range frames {
		out = append(out, animation.RGB565LE(f.Img)...)
	}
	chunks := (len(out) + ChunkSize - 1) / ChunkSize
	out = append(out, make([]byte, chunks*ChunkSize-len(out))...)
	return out, chunks, nil
}

// readAck reads one acknowledgement. ok is false if none arrived in time.
func (p *Port) readAck(ctx context.Context, t time.Duration) (ack []byte, ok bool, err error) {
	ack, err = p.Data.Read(ctx, t)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return ack, true, nil
}

// drain discards stale acknowledgements, stopping when the pipe is quiet. The
// number of reads is bounded in case a faulty pipe never goes quiet.
func (p *Port) drain(ctx context.Context) error {
	for range 64 {
		_, ok, err := p.readAck(ctx, 10*time.Millisecond)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	return nil
}

// sendChunk writes one chunk and reports whether it was acknowledged.
func (p *Port) sendChunk(ctx context.Context, chunk []byte) (bool, error) {
	if err := p.Data.Write(chunk); err != nil {
		return false, err
	}
	ack, ok, err := p.readAck(ctx, ackTimeout)
	if err != nil || !ok {
		return false, err
	}
	if len(ack) < 3 || ack[0] != ackMagic[0] || ack[1] != ackMagic[1] || ack[2] != ackMagic[2] {
		return false, fmt.Errorf("unexpected reply % x: stopping with the keyboard in an unknown state", ack[:min(4, len(ack))])
	}
	return true, nil
}

func imageCfg(chunks int) []byte {
	p := Cmd(0x72, 0x03, false)
	p[8], p[9] = byte(chunks), byte(chunks>>8)
	return p
}

// sendAndHandshake sends a control frame and does the handshake.
func (p *Port) sendAndHandshake(ctx context.Context, frame []byte, post time.Duration) error {
	if err := p.Control.SetFeature(frame); err != nil {
		return err
	}
	return p.handshake(ctx, post)
}

// Align locks the firmware's 64-packet ring counter to 0 (see FINDINGS.md). It
// writes dummy zero data and a SAVE, so it overwrites the current animation.
func (p *Port) Align(ctx context.Context) (bool, error) {
	if err := p.drain(ctx); err != nil {
		return false, err
	}
	if err := p.sendAndHandshake(ctx, Cmd(0x18, 0, false), cmdDelay); err != nil {
		return false, err
	}
	if err := p.sendAndHandshake(ctx, imageCfg(3), cfgDelay); err != nil {
		return false, err
	}
	if err := p.drain(ctx); err != nil {
		return false, err
	}
	pkt := make([]byte, 64)
	burst := false
	for b := 1; b <= 8 && !burst; b++ {
		for range 8 {
			if err := p.Data.Write(pkt); err != nil {
				return false, err
			}
		}
		_, ok, err := p.readAck(ctx, 220*time.Millisecond)
		if err != nil {
			return false, err
		}
		burst = ok
	}
	for range 56 {
		if err := p.Data.Write(pkt); err != nil {
			return false, err
		}
	}
	fine := false
	for q := 1; q <= 8 && !fine; q++ {
		if err := p.Data.Write(pkt); err != nil {
			return false, err
		}
		_, ok, err := p.readAck(ctx, 220*time.Millisecond)
		if err != nil {
			return false, err
		}
		fine = ok
	}
	last, err := p.sendChunk(ctx, make([]byte, ChunkSize))
	if err != nil {
		return false, err
	}
	if err := sleep(ctx, 100*time.Millisecond); err != nil {
		return false, err
	}
	if err := p.sendAndHandshake(ctx, Cmd(0x02, 0, false), 100*time.Millisecond); err != nil {
		return false, err
	}
	if err := p.drain(ctx); err != nil {
		return false, err
	}
	return burst && fine && last, nil
}

// Upload streams the payload. The final SAVE is sent only after every chunk was
// acknowledged, so a failed or cancelled transfer is never committed.
func (p *Port) Upload(ctx context.Context, payload []byte, chunks int, progress func(done, total int)) error {
	if len(payload) != chunks*ChunkSize {
		return fmt.Errorf("payload is %d bytes, expected %d chunks of %d", len(payload), chunks, ChunkSize)
	}
	ok, err := p.Align(ctx)
	if err == nil && !ok {
		ok, err = p.Align(ctx)
	}
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the keyboard's data channel would not line up after 2 tries; replug the cable and try again")
	}
	if err := p.sendAndHandshake(ctx, Cmd(0x18, 0, false), cmdDelay); err != nil {
		return err
	}
	if err := p.sendAndHandshake(ctx, imageCfg(chunks), cfgDelay); err != nil {
		return err
	}
	if err := p.drain(ctx); err != nil {
		return err
	}
	deadline := time.Now().Add(30*time.Second + time.Duration(chunks)*2*ackTimeout)
	acks := 0
	for i := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %d of %d parts; nothing was saved", acks, chunks)
		}
		acked, err := p.sendChunk(ctx, payload[i*ChunkSize:(i+1)*ChunkSize])
		if err != nil {
			return err
		}
		if acked {
			acks++
		}
		if progress != nil {
			progress(i+1, chunks)
		}
	}
	if acks != chunks {
		return fmt.Errorf("the keyboard confirmed only %d of %d parts; nothing was saved", acks, chunks)
	}
	if err := sleep(ctx, 100*time.Millisecond); err != nil {
		return err
	}
	if err := p.sendAndHandshake(ctx, Cmd(0x02, 0, false), 100*time.Millisecond); err != nil {
		return err
	}
	_ = p.drain(ctx) // the picture is saved; a failure here changes nothing for the caller
	return nil
}
