package drivers_test

import (
	"fmt"
	"time"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard/drivers"
)

// A Plan can be inspected without touching the keyboard. Nothing is sent until Run.
func Example_plan() {
	control := []byte{0x06, 0x13, 0xff, 0x09, 0x01, 0xa1, 0x01, 0x75, 0x08, 0x95, 0x40, 0xb1, 0x02, 0xc0}
	data := []byte{0x06, 0x68, 0xff, 0x09, 0x61, 0xa1, 0x01, 0x75, 0x08, 0x96, 0x00, 0x10, 0x91, 0x02, 0xc0}
	ifaces := []hidraw.Info{
		{Node: "/dev/hidraw5", PID: 0x800A, Descriptor: hidraw.ParseDescriptor(data)},
		{Node: "/dev/hidraw6", PID: 0x800A, Descriptor: hidraw.ParseDescriptor(control)},
	}
	f := drivers.NewFinder(func() ([]hidraw.Info, error) { return ifaces, nil }, nil)

	drv, err := f.Driver(ifaces, keyboard.Override{})
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := drv.PlanClock(time.Now())
	if err != nil {
		fmt.Println(err)
		return
	}
	d := plan.Describe()
	fmt.Println(drv.Layout(), d.Node, d.Packets)
	// Output: sonix /dev/hidraw6 4
}
