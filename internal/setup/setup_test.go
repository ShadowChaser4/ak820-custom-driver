package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedRuleMatchesTheShippedFile(t *testing.T) {
	b, err := os.ReadFile("../../udev/70-ak820.rules")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != Rule {
		t.Fatal("internal/setup/70-ak820.rules and udev/70-ak820.rules have drifted apart; copy one over the other")
	}
	for _, want := range []string{`ATTRS{idVendor}=="0c45"`, `"800a"`, `"8009"`, `TAG+="uaccess"`} {
		if !strings.Contains(Rule, want) {
			t.Errorf("rule lacks %s", want)
		}
	}
}

func TestInstallCommandUsesThePrintedRuleAndNeverRunsItself(t *testing.T) {
	for _, prog := range []string{"ak820", "./ak820"} {
		c := InstallCommand(prog)
		if !strings.HasPrefix(c, prog+" setup --print-rule") || !strings.Contains(c, "udevadm control --reload") || !strings.Contains(c, "udevadm trigger") {
			t.Error(c)
		}
	}
}

func TestRuleInstalledDetection(t *testing.T) {
	dir := t.TempDir()
	if _, ok := RuleInstalled([]string{dir}); ok {
		t.Fatal("empty dir")
	}
	os.WriteFile(filepath.Join(dir, "10-other.rules"), []byte(`ATTRS{idVendor}=="046d", TAG+="uaccess"`), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(`0c45 800a`), 0o644)
	if _, ok := RuleInstalled([]string{dir, "/nonexistent"}); ok {
		t.Fatal("an unrelated rule and a non-.rules file must not count")
	}
	os.WriteFile(filepath.Join(dir, "70-ak820.rules"), []byte(Rule), 0o644)
	if p, ok := RuleInstalled([]string{dir}); !ok || !strings.HasSuffix(p, "70-ak820.rules") {
		t.Fatalf("%q %v", p, ok)
	}
	d2 := t.TempDir() // a hand-written rule from someone else is accepted too
	os.WriteFile(filepath.Join(d2, "99-me.rules"), []byte(`SUBSYSTEM=="hidraw", ATTRS{idVendor}=="0C45", ATTRS{idProduct}=="8009", MODE="0666"`), 0o644)
	if _, ok := RuleInstalled([]string{d2}); !ok {
		t.Fatal("case-insensitive match on a custom rule")
	}
}

func okAll(in Input) Input {
	in.Found, in.Accessible, in.Layout, in.Name = 4, 4, "sonix", "AK820 0c45:800a"
	return in
}

func TestCheckAllGood(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "70-ak820.rules"), []byte(Rule), 0o644)
	items := Check(okAll(Input{RuleDirs: []string{d}}))
	if !AllOK(items) || !strings.Contains(Format(items, "ak820"), "All good") || strings.Contains(Format(items, "ak820"), "✗") {
		t.Fatal(Format(items, "ak820"))
	}
}

func TestHintsUseTheNameTheUserTyped(t *testing.T) {
	in := okAll(Input{RuleDirs: []string{t.TempDir()}, Prog: "./ak820"})
	out := Format(Check(in), "./ak820")
	if !strings.Contains(out, "./ak820 setup --print-rule") || !strings.Contains(out, "`./ak820 setup` again") {
		t.Error(out)
	}
	if strings.Contains(strings.ReplaceAll(out, "./ak820", ""), "ak820 setup") {
		t.Errorf("a hint still says plain ak820 (which is not on the PATH):\n%s", out)
	}
}

func TestCheckExplainsEachProblemInPlainWords(t *testing.T) {
	empty := []string{t.TempDir()}
	cases := []struct {
		name string
		in   Input
		want []string
	}{
		{"no keyboard", Input{RuleDirs: empty}, []string{"✗ Keyboard found", "USB-C", "switch on the back", "Bluetooth"}},
		{"several keyboards", Input{FindErr: errors.New("2 AK820 keyboards are connected"), RuleDirs: empty}, []string{"✗ Keyboard found", "2 AK820 keyboards"}},
		{"no rule", okAll(Input{RuleDirs: empty}), []string{"✗ Permission rule installed", InstallCommand("ak820")}},
		{"not allowed", func() Input { i := okAll(Input{RuleDirs: empty}); i.Accessible = 0; return i }(), []string{"✗ This user is allowed", "0 of 4", "replug"}},
		{"unknown layout", func() Input { i := okAll(Input{RuleDirs: empty}); i.Layout = "unknown"; return i }(), []string{"✗ Keyboard layout recognised", "report --issue"}},
	}
	for _, c := range cases {
		items := Check(c.in)
		if AllOK(items) {
			t.Errorf("%s: should not be all ok", c.name)
		}
		out := Format(items, "ak820")
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q in\n%s", c.name, w, out)
			}
		}
		if !strings.Contains(out, "run `ak820 setup` again") {
			t.Errorf("%s: should tell the user to re-run setup", c.name)
		}
	}
}
