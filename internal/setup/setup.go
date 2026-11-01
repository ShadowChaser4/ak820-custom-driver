// Package setup checks that a machine is ready to talk to the keyboard and
// explains, in plain words, what to do when it is not.
package setup

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Rule is the udev rule that lets the logged-in user open the keyboard's hidraw nodes.
//
//go:embed 70-ak820.rules
var Rule string

// RuleDirs are the places udev reads rules from.
var RuleDirs = []string{"/etc/udev/rules.d", "/usr/lib/udev/rules.d", "/lib/udev/rules.d", "/run/udev/rules.d"}

// InstallCommand is the one command that installs the rule, using prog as the
// name the user invoked (e.g. "ak820" or "./ak820"). The tool never runs sudo itself.
func InstallCommand(prog string) string {
	return prog + " setup --print-rule | sudo tee /etc/udev/rules.d/70-ak820.rules >/dev/null && sudo udevadm control --reload && sudo udevadm trigger"
}

type Item struct {
	OK    bool
	Title string
	Fix   string // what to do, when !OK
}

type Input struct {
	Found      int    // interfaces found for the chosen keyboard
	Name       string // e.g. "AK820 0c45:800a"
	FindErr    error  // why none was found (multiple keyboards, none, ...)
	Accessible int    // how many of them this user can open read-write
	Layout     string // "sonix", "legacy", "unknown"
	RuleDirs   []string
	Prog       string // how the user ran this tool; defaults to "ak820"
}

// RuleInstalled reports whether any udev rule in dirs already covers the AK820
// (any *.rules file mentioning both the vendor and a supported product id).
func RuleInstalled(dirs []string) (string, bool) {
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.rules"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			t := strings.ToLower(string(b))
			if strings.Contains(t, "0c45") && (strings.Contains(t, "800a") || strings.Contains(t, "8009")) {
				return f, true
			}
		}
	}
	return "", false
}

// Check turns the facts into a checklist.
func Check(in Input) []Item {
	if in.Prog == "" {
		in.Prog = "ak820"
	}
	var out []Item
	if in.FindErr != nil || in.Found == 0 {
		fix := "Plug the keyboard in with the USB-C cable and slide the switch on the back to USB. Bluetooth and the 2.4G dongle cannot control the screen."
		if in.FindErr != nil {
			fix = in.FindErr.Error()
		}
		out = append(out, Item{false, "Keyboard found", fix})
	} else {
		out = append(out, Item{true, fmt.Sprintf("Keyboard found (%s, %d interfaces)", in.Name, in.Found), ""})
	}
	path, installed := RuleInstalled(in.RuleDirs)
	if installed {
		out = append(out, Item{true, "Permission rule installed (" + path + ")", ""})
	} else {
		out = append(out, Item{false, "Permission rule installed",
			"Run this once, then unplug and replug the keyboard:\n    " + InstallCommand(in.Prog)})
	}
	if in.Found > 0 {
		if in.Accessible == in.Found {
			out = append(out, Item{true, "This user is allowed to use the keyboard", ""})
		} else {
			fix := "Unplug and replug the keyboard after installing the rule."
			if !installed {
				fix = "Install the permission rule above, then unplug and replug the keyboard."
			}
			out = append(out, Item{false, fmt.Sprintf("This user is allowed to use the keyboard (%d of %d interfaces)", in.Accessible, in.Found), fix})
		}
		if in.Layout == "unknown" {
			out = append(out, Item{false, "Keyboard layout recognised",
				"Run `" + in.Prog + " report --issue` and paste the result into a new issue so support can be added."})
		} else {
			out = append(out, Item{true, "Keyboard layout recognised (" + in.Layout + ")", ""})
		}
	}
	return out
}

// AllOK reports whether nothing needs fixing.
func AllOK(items []Item) bool {
	for _, i := range items {
		if !i.OK {
			return false
		}
	}
	return true
}

// Format renders the checklist.
func Format(items []Item, prog string) string {
	var b strings.Builder
	for _, i := range items {
		mark := "✓"
		if !i.OK {
			mark = "✗"
		}
		fmt.Fprintf(&b, " %s %s\n", mark, i.Title)
		if !i.OK && i.Fix != "" {
			fmt.Fprintf(&b, "     %s\n", strings.ReplaceAll(i.Fix, "\n", "\n     "))
		}
	}
	if AllOK(items) {
		fmt.Fprintf(&b, "\nAll good. Start the app with:  %s\n", prog)
	} else {
		fmt.Fprintf(&b, "\nFix the items marked ✗, then run `%s setup` again.\n", prog)
	}
	return b.String()
}
