package glibclocale

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	maxFormatDepth = 3
	glibcFlags     = "_-0^#"
	unimplemented  = "DgGhjnstuUVwW"
)

type padding uint8

const (
	padZero padding = iota
	padSpace
	padNone
)

type moment struct {
	date                 eraDate
	weekday              time.Weekday
	hour, minute, second int
	zone                 string
	offset               int
}

// Format writes at as glibc's strftime writes format in this locale. It
// implements the conversions glibc's locales use in their own formats:
// %a %A %b %B %c %C %d %e %F %H %I %k %l %m %M %p %P %r %R %S %T %x %X
// %y %Y %z %Z and %%, the - flag, %Ey for the era's year and the O
// modifier for the locale's alternative digits. %P lowercases ASCII
// letters only, as glibc does in a UTF-8 locale. A conversion glibc does
// not know is copied as it stands, as glibc copies it; one glibc knows
// but Format does not implement, another flag or a field width is an
// error naming it.
func (t *Time) Format(format string, at time.Time) (string, error) {
	y, mo, d := at.Date()
	zone, offset := at.Zone()
	m := moment{
		date:    eraDate{year: y, month: int(mo), day: d},
		weekday: at.Weekday(),
		hour:    at.Hour(), minute: at.Minute(), second: at.Second(),
		zone: zone, offset: offset,
	}
	var b strings.Builder
	b.Grow(2 * len(format))
	if err := t.write(&b, format, m, 0); err != nil {
		return "", fmt.Errorf("glibclocale: strftime %q: %w", format, err)
	}
	return b.String(), nil
}

func (t *Time) write(b *strings.Builder, format string, m moment, depth int) error {
	if depth > maxFormatDepth {
		return fmt.Errorf("formats nest deeper than %d", maxFormatDepth)
	}
	for format != "" {
		i := strings.IndexByte(format, '%')
		if i < 0 {
			b.WriteString(format)
			return nil
		}
		b.WriteString(format[:i])
		format = format[i:]
		n := 1
		pad := padZero
		for n < len(format) && strings.IndexByte(glibcFlags, format[n]) >= 0 {
			if format[n] != '-' {
				return fmt.Errorf("unsupported flag %q in %q", format[n], format)
			}
			pad = padNone
			n++
		}
		if n < len(format) && '1' <= format[n] && format[n] <= '9' {
			return fmt.Errorf("unsupported field width in %q", format)
		}
		var modifier byte
		if n < len(format) && (format[n] == 'E' || format[n] == 'O') {
			modifier = format[n]
			n++
		}
		if n >= len(format) {
			b.WriteString(format)
			return nil
		}
		known, err := t.convert(b, format[n], modifier, pad, m, depth)
		if err != nil {
			return err
		}
		if !known {
			b.WriteString(format[:n+1])
		}
		format = format[n+1:]
	}
	return nil
}

func accepts(spec, modifier byte) (bool, error) {
	unsupported := unsupportedConversion(spec, modifier)
	switch {
	case modifier == 0:
		return true, nil
	case strings.IndexByte("%pPZrTRz", spec) >= 0:
		return true, nil
	case modifier == 'O' && strings.IndexByte("bB", spec) >= 0:
		return false, unsupported
	case modifier == 'E' && strings.IndexByte("cxXCY", spec) >= 0:
		return false, unsupported
	case modifier == 'E':
		return spec == 'y', nil
	}
	return strings.IndexByte("CdeHIklmMSy", spec) >= 0, nil
}

func (t *Time) convert(b *strings.Builder, spec, modifier byte, pad padding, m moment, depth int) (bool, error) {
	if strings.IndexByte(unimplemented, spec) >= 0 {
		return false, unsupportedConversion(spec, modifier)
	}
	if ok, err := accepts(spec, modifier); !ok || err != nil {
		return false, err
	}
	hour12 := (m.hour+11)%12 + 1
	switch spec {
	case '%':
		b.WriteByte('%')
	case 'a':
		b.WriteString(t.abDays[m.weekday])
	case 'A':
		b.WriteString(t.days[m.weekday])
	case 'b':
		b.WriteString(t.abMonths[m.date.month-1])
	case 'B':
		b.WriteString(t.months[m.date.month-1])
	case 'p':
		b.WriteString(t.meridiem(m.hour))
	case 'P':
		writeLowerASCII(b, t.meridiem(m.hour))
	case 'c':
		return true, t.write(b, t.dateTime, m, depth+1)
	case 'x':
		return true, t.write(b, t.date, m, depth+1)
	case 'X':
		return true, t.write(b, t.clock, m, depth+1)
	case 'r':
		if t.clockAMPM == "" {
			return true, t.write(b, cTime.clockAMPM, m, depth+1)
		}
		return true, t.write(b, t.clockAMPM, m, depth+1)
	case 'T':
		return true, t.write(b, "%H:%M:%S", m, depth+1)
	case 'R':
		return true, t.write(b, "%H:%M", m, depth+1)
	case 'F':
		return true, t.write(b, "%Y-%m-%d", m, depth+1)
	case 'C':
		t.number(b, floorDiv(m.date.year, 100), 1, pad, modifier)
	case 'd':
		t.number(b, m.date.day, 2, pad, modifier)
	case 'e':
		t.number(b, m.date.day, 2, spaced(pad), modifier)
	case 'H':
		t.number(b, m.hour, 2, pad, modifier)
	case 'I':
		t.number(b, hour12, 2, pad, modifier)
	case 'k':
		t.number(b, m.hour, 2, spaced(pad), modifier)
	case 'l':
		t.number(b, hour12, 2, spaced(pad), modifier)
	case 'm':
		t.number(b, m.date.month, 2, pad, modifier)
	case 'M':
		t.number(b, m.minute, 2, pad, modifier)
	case 'S':
		t.number(b, m.second, 2, pad, modifier)
	case 'y':
		if modifier == 'E' {
			if e, ok := t.era(m.date); ok {
				t.number(b, e.offset+e.direction*(m.date.year-e.start.year), 2, pad, 0)
				return true, nil
			}
		}
		t.number(b, m.date.year-100*floorDiv(m.date.year, 100), 2, pad, modifier)
	case 'Y':
		t.number(b, m.date.year, 1, pad, 0)
	case 'z':
		t.offset(b, m.offset, pad, modifier)
	case 'Z':
		b.WriteString(m.zone)
	default:
		return false, nil
	}
	return true, nil
}

func (t *Time) meridiem(hour int) string {
	if hour < 12 {
		return t.am
	}
	return t.pm
}

func (t *Time) era(d eraDate) (era, bool) {
	for _, e := range t.eras {
		lo, hi := e.start, e.stop
		if lo.compare(hi) > 0 {
			lo, hi = hi, lo
		}
		if lo.compare(d) <= 0 && d.compare(hi) <= 0 {
			return e, true
		}
	}
	return era{}, false
}

func (t *Time) number(b *strings.Builder, n, width int, pad padding, modifier byte) {
	if modifier == 'O' && n >= 0 && n < len(t.altDigits) && t.altDigits[n] != "" {
		b.WriteString(t.altDigits[n])
		return
	}
	if n < 0 {
		b.WriteByte('-')
		n = -n
		width--
	}
	var buf [20]byte
	digits := strconv.AppendInt(buf[:0], int64(n), 10)
	if pad != padNone {
		fill := byte('0')
		if pad == padSpace {
			fill = ' '
		}
		for range width - len(digits) {
			b.WriteByte(fill)
		}
	}
	b.Write(digits)
}

func spaced(pad padding) padding {
	if pad == padNone {
		return padNone
	}
	return padSpace
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

func (t *Time) offset(b *strings.Builder, seconds int, pad padding, modifier byte) {
	sign := byte('+')
	if seconds < 0 {
		sign, seconds = '-', -seconds
	}
	b.WriteByte(sign)
	minutes := seconds / 60
	t.number(b, minutes/60*100+minutes%60, 4, pad, modifier)
}

func writeLowerASCII(b *strings.Builder, s string) {
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
}

func unsupportedConversion(spec, modifier byte) error {
	if modifier == 0 {
		return fmt.Errorf("unsupported conversion %%%c", spec)
	}
	return fmt.Errorf("unsupported conversion %%%c%c", modifier, spec)
}
