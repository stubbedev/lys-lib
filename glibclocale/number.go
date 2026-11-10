package glibclocale

import (
	"cmp"
	"strconv"
	"strings"
)

// Format writes units scaled down by scale decimal places, so 123456789
// at scale 2 is 1234567.89, with the locale's decimal point, thousands
// separator and digit grouping.
func (n Numeric) Format(units uint64, scale uint8) string {
	return formatDecimal(units, scale, n.decimal, n.thousands, n.grouping)
}

// Format writes an amount of money as [Numeric.Format] writes a number,
// with the locale's monetary separators and grouping and its currency
// symbol before, after or in place of the decimal point. An empty
// monetary decimal point is numeric's, as glibc's strfmon takes it.
func (m Monetary) Format(units uint64, scale uint8, numeric Numeric) string {
	if m.placement == atRadix && m.symbol != "" && scale > 0 {
		return formatDecimal(units, scale, m.symbol, m.thousands, m.grouping)
	}
	amount := formatDecimal(units, scale, cmp.Or(m.decimal, numeric.decimal), m.thousands, m.grouping)
	if m.symbol == "" {
		return amount
	}
	space := ""
	if m.spaced {
		space = " "
	}
	if m.placement == beforeAmount {
		return m.symbol + space + amount
	}
	return amount + space + m.symbol
}

func formatDecimal(units uint64, scale uint8, decimal, thousands, grouping string) string {
	digits := strconv.FormatUint(units, 10)
	if len(digits) <= int(scale) {
		digits = strings.Repeat("0", int(scale)-len(digits)+1) + digits
	}
	whole, fraction := digits[:len(digits)-int(scale)], digits[len(digits)-int(scale):]
	var b strings.Builder
	b.Grow(2*len(digits) + len(decimal))
	group(&b, whole, thousands, grouping)
	if scale > 0 {
		b.WriteString(decimal)
		b.WriteString(fraction)
	}
	return b.String()
}

func group(b *strings.Builder, digits, sep, grouping string) {
	var cuts [20]int
	n, end, g := 0, len(digits), 0
	for sep != "" && g < len(grouping) {
		size := int(grouping[g])
		if size == 0 || size >= charMax || end <= size {
			break
		}
		end -= size
		cuts[n] = end
		n++
		if g+1 < len(grouping) {
			g++
		}
	}
	prev := 0
	for i := n - 1; i >= 0; i-- {
		b.WriteString(digits[prev:cuts[i]])
		b.WriteString(sep)
		prev = cuts[i]
	}
	b.WriteString(digits[prev:])
}
