package hidraw

import (
	"encoding/hex"
	"strings"
	"testing"
)

func h(s string) []byte {
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		panic(err)
	}
	return b
}

// Descriptors captured from a real AK820 Pro (0c45:800a), hidraw5-8.
func TestParseDescriptorRealUnit(t *testing.T) {
	cases := []struct {
		name          string
		raw           []byte
		page          uint32
		in, out, feat int
		ids           bool
	}{
		{"kbd", h("0501 0906 a101 0507 19e0 29e7 1500 2501 7501 9508 8102 9501 7508 8101 9505 7501 0508 1901 2905 9102 9501 7503 9101 9506 7508 1500 26ff 0005 0719 002a 6500 8100 c0"), 0x01, 8, 1, 0, false},
		{"ff68", h("0668ff0961a1010962150026ff00954075088102 096315 0026ff00 960010 7508 9102 c0"), 0xFF68, 64, 4096, 0, false},
		{"ff13", h("0613ff0901a1011500 26ff00 7508 9540 0902 8102 0903 9102 0a00ff b102 c0"), 0xFF13, 64, 64, 64, false},
	}
	for _, c := range cases {
		d := ParseDescriptor(c.raw)
		if d.Page != c.page || d.InBytes() != c.in || d.OutBytes() != c.out || d.FeatBytes() != c.feat || d.ReportIDs != c.ids {
			t.Errorf("%s: got page %#x in %d out %d feat %d ids %v", c.name, d.Page, d.InBytes(), d.OutBytes(), d.FeatBytes(), d.ReportIDs)
		}
	}
}

func TestParseConsumerHasReportIDs(t *testing.T) {
	raw := h("050c 0901 a101 8503 1900 2a3c03 1500 263c03 9501 7510 8100 c0")
	d := ParseDescriptor(raw)
	if !d.ReportIDs || d.Page != 0x0C || d.InBytes() != 2 {
		t.Errorf("got %+v", d)
	}
}
