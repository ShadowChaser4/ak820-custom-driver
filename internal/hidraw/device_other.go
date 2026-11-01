//go:build !linux

package hidraw

import (
	"errors"
	"fmt"
	"time"
)

// errNotLinux explains why device I/O fails on other systems: hidraw is Linux-only.
var errNotLinux = fmt.Errorf("hidraw devices are only available on Linux: %w", errors.ErrUnsupported)

// Accessible always reports false outside Linux.
func (Info) Accessible() bool { return false }

// Device is an open hidraw node. It cannot be opened outside Linux.
type Device struct{ Info Info }

// Open always fails outside Linux.
func Open(Info) (*Device, error) { return nil, errNotLinux }

// Close does nothing outside Linux.
func (*Device) Close() error { return nil }

// Write always fails outside Linux.
func (*Device) Write([]byte) error { return errNotLinux }

// ReadTimeout always fails outside Linux.
func (*Device) ReadTimeout(time.Duration) ([]byte, error) { return nil, errNotLinux }

// GetFeature always fails outside Linux.
func (*Device) GetFeature(byte, int) ([]byte, error) { return nil, errNotLinux }

// SetFeature always fails outside Linux.
func (*Device) SetFeature([]byte, int) error { return errNotLinux }
