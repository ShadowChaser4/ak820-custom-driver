package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"

	"github.com/ShadowChaser4/ak820-custom-driver/internal/hidraw"
	"github.com/ShadowChaser4/ak820-custom-driver/internal/keyboard"
)

// errExit makes a command exit non-zero without printing another message.
var errExit = errors.New("exit")

// advice turns an error into what to do next, in plain words. This is the only place
// in the command that decides wording. prog is the name the user typed.
func advice(err error, prog string) string {
	return strings.ReplaceAll(adviceText(err), "`ak820 ", "`"+prog+" ")
}

func adviceText(err error) string {
	var multi *keyboard.MultipleError
	switch {
	case errors.Is(err, context.Canceled):
		return "Stopped before finishing. Send it again when you are ready."
	case errors.Is(err, keyboard.ErrNoDevice):
		return "No AK820 keyboard found.\n  Plug it in with the USB-C cable and slide the switch on the back to USB.\n  (Bluetooth and the 2.4G dongle cannot control the screen.)\n  Run `ak820 setup` to check this computer."
	case errors.Is(err, keyboard.ErrNotConfigured):
		return "The keyboard is plugged in, but it could not finish connecting to this computer.\n  This can happen when a USB hub runs out of bandwidth. Plug the keyboard into a USB port on the computer itself, or unplug other devices from the hub, then replug the keyboard."
	case errors.Is(err, syscall.ENODEV):
		return "The keyboard disappeared while talking to it.\n  Check the cable and that the switch on the back is on USB, then try again."
	case errors.Is(err, hidraw.ErrPermission):
		return "This computer is not letting ak820 use the keyboard yet.\n  Run `ak820 setup` and follow the steps, then unplug and replug the keyboard."
	case errors.Is(err, keyboard.ErrUnknownLayout):
		return "Your keyboard was found, but it talks in a way ak820 does not know yet.\n  Run `ak820 report --issue` and paste the result into a new issue so support can be added."
	case errors.As(err, &multi):
		return fmt.Sprintf("%d AK820 keyboards are connected. Unplug all but one, or choose one with --device 1 ... --device %d.", multi.N, multi.N)
	case errors.Is(err, keyboard.ErrNoDeviceInfo):
		return err.Error() + ". Run `ak820 time --yes` to connect and set the clock instead"
	case errors.Is(err, keyboard.ErrUnsupported):
		return err.Error()
	case errors.Is(err, keyboard.ErrNoReply):
		return "error: " + err.Error() + "\n  The keyboard did not answer. Check the cable and that the switch on the back is on USB, then try again."
	}
	return "error: " + err.Error()
}

// fnDelHint tells people how to get their picture back after the keyboard restarts.
const fnDelHint = "after a restart the keyboard shows the clock, so press Fn+Del to open your picture"
