// Package proto implements the AK820 Pro host<->device packet framing
// (see PROTOCOL.md in fromagge/ak820-screen).
package proto

import (
	"fmt"
	"time"
)

const (
	CmdInfo    = 0x10
	CmdTemp    = 0x34
	CmdAnim    = 0x50
	CmdBuiltin = 0x51
	AckAnim    = 0x41
)

// Conn is the slice of hidraw.Dev the protocol needs (fakeable in tests).
type Conn interface {
	Write(pkt []byte) error
	ReadTimeout(t time.Duration) ([]byte, error)
}

// Chunks builds the standard chunked command packets (vendor kX/AX):
//
//	AA cmd len addr_lo addr_hi 00 last 00 <payload...>
//
// Each packet is exactly reportLen bytes; payload is reportLen-8 bytes per chunk.
func Chunks(reportLen int, cmd byte, data []byte, contentSize int) [][]byte {
	if contentSize < len(data) {
		contentSize = len(data)
	}
	per := reportLen - 8
	n := (contentSize + per - 1) / per
	if n < 1 {
		n = 1
	}
	var out [][]byte
	for i := 0; i < n; i++ {
		addr := i * per
		ln := min(per, contentSize-addr)
		pkt := make([]byte, reportLen)
		copy(pkt, []byte{0xAA, cmd, byte(ln), byte(addr), byte(addr >> 8), 0, 0, 0})
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

// AnimChunks builds the 0x50 upload stream, which uses a custom header:
//
//	AA 50 idx_lo idx_hi total_lo total_hi 50 06 <chunk>
func AnimChunks(reportLen int, payload []byte) [][]byte {
	per := reportLen - 8
	total := (len(payload) + per - 1) / per
	var out [][]byte
	for i := 0; i < total; i++ {
		pkt := make([]byte, reportLen)
		copy(pkt, []byte{0xAA, CmdAnim, byte(i), byte(i >> 8), byte(total), byte(total >> 8), 0x50, 0x06})
		copy(pkt[8:], payload[i*per:min((i+1)*per, len(payload))])
		out = append(out, pkt)
	}
	return out
}

// Port sends packets over a Conn and waits for 55 <cmd> replies.
type Port struct {
	C       Conn
	Len     int
	Timeout time.Duration
	Retries int
}

func (p *Port) drain() {
	for {
		r, err := p.C.ReadTimeout(time.Millisecond)
		if err != nil || r == nil {
			return
		}
	}
}

func (p *Port) wait(cmd byte, t time.Duration) ([]byte, error) {
	end := time.Now().Add(t)
	for {
		left := time.Until(end)
		if left <= 0 {
			return nil, nil
		}
		r, err := p.C.ReadTimeout(left)
		if err != nil {
			return nil, err
		}
		if len(r) > 1 && r[0] == 0x55 && r[1] == cmd {
			return r, nil
		}
		if r == nil {
			return nil, nil
		}
	}
}

// Transact writes pkt and waits for a reply to cmd, retrying on timeout.
func (p *Port) Transact(pkt []byte, cmd byte, t time.Duration, retries int) ([]byte, error) {
	for a := 0; a <= retries; a++ {
		p.drain()
		if err := p.C.Write(pkt); err != nil {
			return nil, err
		}
		r, err := p.wait(cmd, t)
		if err != nil {
			return nil, err
		}
		if r != nil {
			return r, nil
		}
	}
	return nil, fmt.Errorf("cmd 0x%02x: no reply (%d tries)", cmd, retries+1)
}

// Command sends a chunked command and collects the reply to every chunk.
func (p *Port) Command(cmd byte, data []byte, contentSize int) ([][]byte, error) {
	var replies [][]byte
	for _, pkt := range Chunks(p.Len, cmd, data, contentSize) {
		r, err := p.Transact(pkt, cmd, p.Timeout, p.Retries)
		if err != nil {
			return replies, err
		}
		replies = append(replies, r)
	}
	return replies, nil
}

// Send writes a chunked command without waiting for replies.
func (p *Port) Send(cmd byte, data []byte, contentSize int) error {
	for _, pkt := range Chunks(p.Len, cmd, data, contentSize) {
		if err := p.C.Write(pkt); err != nil {
			return err
		}
	}
	return nil
}

// Upload sends an animation payload, waiting for a 55 41 ack per chunk.
func (p *Port) Upload(payload []byte, progress func(done, total int)) error {
	pkts := AnimChunks(p.Len, payload)
	for i, pkt := range pkts {
		if _, err := p.Transact(pkt, AckAnim, 2*time.Second, p.Retries); err != nil {
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
	fw := (int(b[8]&15) + int(b[8]>>4)*10 + int(b[9])*100)
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
