// Package hidraw enumerates Linux hidraw nodes and parses their report descriptors.
package hidraw

// Desc is the part of a HID report descriptor this tool cares about.
type Desc struct {
	Page      uint32 // first Usage Page
	InBits    int
	OutBits   int
	FeatBits  int
	ReportIDs bool
}

func (d Desc) InBytes() int   { return d.InBits / 8 }
func (d Desc) OutBytes() int  { return d.OutBits / 8 }
func (d Desc) FeatBytes() int { return d.FeatBits / 8 }

// Vendor reports whether the first usage page is vendor-defined (0xFF00-0xFFFF).
func (d Desc) Vendor() bool { return d.Page >= 0xFF00 }

const (
	maxField = 1 << 16 // largest report size / count we believe (real ones are tiny)
	maxBits  = 1 << 28 // 32 MiB: sums saturate here, so they can never overflow or go negative
)

func addBits(total, size, count int) int {
	n := total + size*count // both factors <= 1<<16, so this cannot overflow a 64-bit int
	if n > maxBits {
		return maxBits
	}
	return n
}

// ParseDescriptor walks the short items of a report descriptor and sums the
// Input / Output / Feature field sizes. Push/Pop are not handled (vendor
// descriptors on this keyboard don't use them).
func ParseDescriptor(b []byte) Desc {
	var d Desc
	var size, count int
	havePage := false
	for i := 0; i < len(b); {
		p := b[i]
		if p == 0xFE { // long item: skip
			if i+1 >= len(b) {
				break
			}
			i += 3 + int(b[i+1])
			continue
		}
		n := [4]int{0, 1, 2, 4}[p&3]
		if i+1+n > len(b) {
			break
		}
		var v int
		for k := 0; k < n; k++ {
			v |= int(b[i+1+k]) << (8 * k)
		}
		if v < 0 || v > maxField {
			v = maxField // an absurd size or count: clamp instead of overflowing
		}
		switch p & 0xFC {
		case 0x04: // Usage Page
			if !havePage {
				d.Page, havePage = uint32(v), true
			}
		case 0x74:
			size = v
		case 0x94:
			count = v
		case 0x84:
			d.ReportIDs = true
		case 0x80:
			d.InBits = addBits(d.InBits, size, count)
		case 0x90:
			d.OutBits = addBits(d.OutBits, size, count)
		case 0xB0:
			d.FeatBits = addBits(d.FeatBits, size, count)
		}
		i += 1 + n
	}
	return d
}
