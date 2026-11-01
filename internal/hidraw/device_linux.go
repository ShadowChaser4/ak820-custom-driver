package hidraw

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Accessible reports whether the current user can read and write the node.
func (i Info) Accessible() bool { return syscall.Access(i.Node, 6) == nil }

// Device is an open hidraw node.
type Device struct {
	f    *os.File
	Info Info
}

// Open opens the node for reading and writing. Without permission it returns an
// error wrapping ErrPermission.
func Open(i Info) (*Device, error) {
	fd, err := syscall.Open(i.Node, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.EACCES) {
			return nil, fmt.Errorf("%w on %s", ErrPermission, i.Node)
		}
		return nil, err
	}
	return &Device{f: os.NewFile(uintptr(fd), i.Node), Info: i}, nil
}

// Close closes the node.
func (d *Device) Close() error { return d.f.Close() }

// Write sends one output report. pkt must already be the full report length;
// report ID 0 is prepended (the kernel strips it for unnumbered reports).
func (d *Device) Write(pkt []byte) error {
	_, err := d.f.Write(append([]byte{0}, pkt...))
	return err
}

// ReadTimeout returns one input report, or (nil, nil) on timeout.
func (d *Device) ReadTimeout(t time.Duration) ([]byte, error) {
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
func (d *Device) GetFeature(id byte, n int) ([]byte, error) {
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
func (d *Device) SetFeature(payload []byte, n int) error {
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
