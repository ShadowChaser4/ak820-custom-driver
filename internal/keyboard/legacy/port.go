// Package legacy speaks the older AK820 protocol: AA/55 framed commands on a config
// interface and an animation stream on a bulk interface (usage page 0xFF67).
//
// Source: PROTOCOL.md in github.com/fromagge/ak820-screen.
package legacy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
)

// Command bytes.
const (
	CmdInfo    = 0x10
	CmdClock   = 0x34
	CmdAnim    = 0x50
	CmdBuiltin = 0x51
	ackAnim    = 0x41
)

// Conn is the part of a device the protocol needs.
type Conn interface {
	Write(report []byte) error
	Read(ctx context.Context, timeout time.Duration) ([]byte, error)
}

// Chunks builds the standard chunked command packets:
//
//	AA cmd len addr_lo addr_hi 00 last 00 <payload...>
//
// Each packet is exactly reportLen bytes.
func Chunks(reportLen int, cmd byte, data []byte, contentSize int) [][]byte {
	contentSize = max(contentSize, len(data))
	per := reportLen - 8
	n := max(1, (contentSize+per-1)/per)
	var out [][]byte
	for i := range n {
		addr := i * per
		pkt := make([]byte, reportLen)
		copy(pkt, []byte{0xAA, cmd, byte(min(per, contentSize-addr)), byte(addr), byte(addr >> 8), 0, 0, 0})
		if i == n-1 {
			pkt[6] = 1
		}
		if addr < len(data) {
			copy(pkt[8:], data[addr:min(addr+per, len(data))])
		}
		out = append(out, pkt)
	}
	return out
}

// AnimChunks builds the 0x50 upload stream, which has its own header:
//
//	AA 50 idx_lo idx_hi total_lo total_hi 50 06 <chunk>
func AnimChunks(reportLen int, payload []byte) [][]byte {
	per := reportLen - 8
	total := (len(payload) + per - 1) / per
	var out [][]byte
	for i := range total {
		pkt := make([]byte, reportLen)
		copy(pkt, []byte{0xAA, CmdAnim, byte(i), byte(i >> 8), byte(total), byte(total >> 8), 0x50, 0x06})
		copy(pkt[8:], payload[i*per:min((i+1)*per, len(payload))])
		out = append(out, pkt)
	}
	return out
}

// Port sends packets and waits for 55 <cmd> replies.
type Port struct {
	Conn    Conn
	Timeout time.Duration
	Retries int
}

func (p *Port) drain(ctx context.Context) error {
	for {
		_, err := p.Conn.Read(ctx, time.Millisecond)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// wait returns the reply to cmd, or os.ErrDeadlineExceeded.
func (p *Port) wait(ctx context.Context, cmd byte, t time.Duration) ([]byte, error) {
	end := time.Now().Add(t)
	for {
		left := time.Until(end)
		if left <= 0 {
			return nil, os.ErrDeadlineExceeded
		}
		r, err := p.Conn.Read(ctx, left)
		if err != nil {
			return nil, err
		}
		if len(r) > 1 && r[0] == 0x55 && r[1] == cmd {
			return r, nil
		}
	}
}

// Transact writes pkt and waits for a reply to cmd, retrying on timeout.
func (p *Port) Transact(ctx context.Context, pkt []byte, cmd byte, t time.Duration, retries int) ([]byte, error) {
	for range retries + 1 {
		if err := p.drain(ctx); err != nil {
			return nil, err
		}
		if err := p.Conn.Write(pkt); err != nil {
			return nil, err
		}
		r, err := p.wait(ctx, cmd, t)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("cmd 0x%02x: %w (%d tries)", cmd, keyboard.ErrNoReply, retries+1)
}

// Exchange sends packets and collects the reply to each.
func (p *Port) Exchange(ctx context.Context, pkts [][]byte, cmd byte) ([][]byte, error) {
	var replies [][]byte
	for _, pkt := range pkts {
		r, err := p.Transact(ctx, pkt, cmd, p.Timeout, p.Retries)
		if err != nil {
			return replies, err
		}
		replies = append(replies, r)
	}
	return replies, nil
}

// Upload sends the animation stream, waiting for a 55 41 acknowledgement per packet.
func (p *Port) Upload(ctx context.Context, pkts [][]byte, progress func(done, total int)) error {
	for i, pkt := range pkts {
		if _, err := p.Transact(ctx, pkt, ackAnim, 2*time.Second, p.Retries); err != nil {
			return fmt.Errorf("chunk %d/%d: %w", i+1, len(pkts), err)
		}
		if progress != nil {
			progress(i+1, len(pkts))
		}
	}
	return nil
}

// Info is the decoded GET_DEVICE_INFO reply.
type Info struct {
	VID, PID     uint16
	Firmware     string
	WorkMode     byte
	Battery      byte
	Charging     byte
	Profile      byte
	TFTMaxFrames int
	TFTDirection byte
	FrameVersion byte
}

// ParseInfo decodes the GET_DEVICE_INFO replies.
func ParseInfo(replies [][]byte) (Info, error) {
	var b []byte
	for _, r := range replies {
		if len(r) > 8 {
			b = append(b, r[8:]...)
		}
	}
	if len(b) < 31 {
		return Info{}, fmt.Errorf("info reply too short (%d bytes)", len(b))
	}
	fw := int(b[8]&15) + int(b[8]>>4)*10 + int(b[9])*100
	return Info{
		VID:          uint16(b[4]) | uint16(b[5])<<8,
		PID:          uint16(b[6]) | uint16(b[7])<<8,
		Firmware:     fmt.Sprintf("%d.%02d", fw/100, fw%100),
		WorkMode:     b[16],
		Battery:      b[17],
		Charging:     b[18],
		Profile:      b[19],
		TFTMaxFrames: int(b[22]) | int(b[23])<<8,
		TFTDirection: b[28],
		FrameVersion: b[30],
	}, nil
}
