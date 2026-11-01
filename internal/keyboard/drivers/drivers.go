// Package drivers lists the protocols this tool speaks. To support a new keyboard
// protocol, add its detector here.
package drivers

import (
	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/legacy"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/sonix"
)

// NewFinder returns a Finder using the given enumeration and opener.
func NewFinder(enumerate func() ([]hidraw.Info, error), open keyboard.Opener) *keyboard.Finder {
	return keyboard.NewFinder(keyboard.Config{
		Enumerate: enumerate,
		Open:      open,
		Detectors: []keyboard.Detector{sonix.Detect, legacy.Detect},
		Explicit:  legacy.ForNode,
	})
}

// System returns a Finder for the real devices on this machine.
func System() *keyboard.Finder {
	return NewFinder(
		func() ([]hidraw.Info, error) { return hidraw.Enumerate(keyboard.VendorID) },
		func(i hidraw.Info) (keyboard.Device, error) {
			d, err := hidraw.Open(i)
			if err != nil {
				return nil, err // not d: a nil *Device must not become a non-nil interface
			}
			return d, nil
		},
	)
}
