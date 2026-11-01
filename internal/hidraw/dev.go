package hidraw

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Info describes one hidraw node (read from sysfs, no device I/O).
type Info struct {
	Node  string // /dev/hidrawN
	Name  string
	VID   uint16
	PID   uint16
	Iface int // USB interface number, -1 if unknown
	Raw   []byte
	D     Desc
}

// Enumerate lists hidraw nodes whose vendor id matches vid (0 = all).
func Enumerate(vid uint16) ([]Info, error) {
	nodes, err := filepath.Glob("/sys/class/hidraw/hidraw*")
	if err != nil {
		return nil, err
	}
	sort.Slice(nodes, func(i, j int) bool {
		if len(nodes[i]) != len(nodes[j]) {
			return len(nodes[i]) < len(nodes[j])
		}
		return nodes[i] < nodes[j]
	})
	var out []Info
	for _, n := range nodes {
		dev := filepath.Join(n, "device")
		ue, err := os.ReadFile(filepath.Join(dev, "uevent"))
		if err != nil {
			continue
		}
		inf := Info{Node: "/dev/" + filepath.Base(n), Iface: -1}
		for _, l := range strings.Split(string(ue), "\n") {
			switch {
			case strings.HasPrefix(l, "HID_ID="):
				f := strings.Split(strings.TrimPrefix(l, "HID_ID="), ":")
				if len(f) == 3 {
					v, _ := strconv.ParseUint(f[1], 16, 32)
					p, _ := strconv.ParseUint(f[2], 16, 32)
					inf.VID, inf.PID = uint16(v), uint16(p)
				}
			case strings.HasPrefix(l, "HID_NAME="):
				inf.Name = strings.TrimPrefix(l, "HID_NAME=")
			}
		}
		if vid != 0 && inf.VID != vid {
			continue
		}
		if real, err := filepath.EvalSymlinks(dev); err == nil {
			if b, err := os.ReadFile(filepath.Join(real, "..", "bInterfaceNumber")); err == nil {
				if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
					inf.Iface = v
				}
			}
		}
		raw, err := os.ReadFile(filepath.Join(dev, "report_descriptor"))
		if err != nil {
			continue
		}
		inf.Raw, inf.D = raw, ParseDescriptor(raw)
		out = append(out, inf)
	}
	return out, nil
}

// Accessible reports whether the current user can read and write the node.
func (i Info) Accessible() bool { return syscall.Access(i.Node, 6) == nil }

// Dev is an open hidraw node.
type Dev struct {
	f    *os.File
	Info Info
}

func Open(i Info) (*Dev, error) {
	fd, err := syscall.Open(i.Node, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.EACCES) {
			return nil, fmt.Errorf("no permission on %s — install udev/70-ak820.rules and replug", i.Node)
		}
		return nil, err
	}
	return &Dev{f: os.NewFile(uintptr(fd), i.Node), Info: i}, nil
}

func (d *Dev) Close() error { return d.f.Close() }

// Write sends one output report. pkt must already be the full report length;
// report ID 0 is prepended (the kernel strips it for unnumbered reports).
func (d *Dev) Write(pkt []byte) error {
	_, err := d.f.Write(append([]byte{0}, pkt...))
	return err
}

// ReadTimeout returns one input report, or (nil, nil) on timeout.
func (d *Dev) ReadTimeout(t time.Duration) ([]byte, error) {
	_ = d.f.SetReadDeadline(time.Now().Add(t))
	buf := make([]byte, 8192)
	n, err := d.f.Read(buf)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, nil
		}
		return nil, err
	}
	return buf[:n], nil
}

// GetFeature issues HIDIOCGFEATURE (a read-only GET_REPORT) for report id.
// The returned slice starts with the report id byte.
func (d *Dev) GetFeature(id byte, n int) ([]byte, error) {
	buf := make([]byte, n+1)
	buf[0] = id
	// _IOC(read|write, 'H', 0x07, len)
	req := uintptr(3<<30 | (n+1)<<16 | 'H'<<8 | 0x07)
	var errno syscall.Errno
	rc, err := d.f.SyscallConn()
	if err != nil {
		return nil, err
	}
	var got uintptr
	err = rc.Control(func(fd uintptr) {
		got, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0])))
	})
	if err != nil {
		return nil, err
	}
	if errno != 0 {
		return nil, errno
	}
	return buf[:got], nil
}

// SetFeature issues HIDIOCSFEATURE with report id 0 and the given payload
// (zero-padded to n bytes). This WRITES to the device.
func (d *Dev) SetFeature(payload []byte, n int) error {
	buf := make([]byte, n+1) // buf[0] = report id 0
	copy(buf[1:], payload)
	req := uintptr(3<<30 | (n+1)<<16 | 'H'<<8 | 0x06)
	rc, err := d.f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0])))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}
