package glibclocale

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	radixChar    = item(lcNumeric) << 16
	thousandsSep = radixChar + 1
	grouping     = radixChar + 2

	monDecimalPoint = item(lcMonetary)<<16 + 2
	monThousandsSep = item(lcMonetary)<<16 + 3
	monGrouping     = item(lcMonetary)<<16 + 4
	pSepBySpace     = item(lcMonetary)<<16 + 10
	crncyStr        = item(lcMonetary)<<16 + 15

	abDay1        = item(lcTime) << 16
	day1          = abDay1 + 7
	abMon1        = abDay1 + 14
	mon1          = abDay1 + 26
	amStr         = abDay1 + 38
	pmStr         = abDay1 + 39
	dTFmt         = abDay1 + 40
	dFmt          = abDay1 + 41
	tFmt          = abDay1 + 42
	tFmtAmPm      = abDay1 + 43
	eraStrings    = abDay1 + 44
	altDigits     = abDay1 + 47
	eraDTFmt      = abDay1 + 48
	eraNumEntries = abDay1 + 50

	charMax = 127
)

// Numeric is a locale's LC_NUMERIC: how it writes a number.
type Numeric struct {
	decimal, thousands string
	grouping           string
}

// Monetary is a locale's LC_MONETARY: how it writes an amount of money.
type Monetary struct {
	decimal, thousands string
	grouping           string
	symbol             string
	placement          placement
	spaced             bool
}

// Time is a locale's LC_TIME: its names of days and months and its
// date and time formats, which [Time.Format] writes.
type Time struct {
	abDays, days                     [7]string
	abMonths, months                 [12]string
	am, pm                           string
	dateTime, date, clock, clockAMPM string
	eras                             []era
	altDigits                        []string
}

type eraDate struct{ year, month, day int }

func (d eraDate) compare(o eraDate) int {
	return cmp.Or(cmp.Compare(d.year, o.year), cmp.Compare(d.month, o.month), cmp.Compare(d.day, o.day))
}

type era struct {
	direction   int
	offset      int
	start, stop eraDate
}

var (
	cNumeric  = Numeric{decimal: "."}
	cMonetary = Monetary{}
	cTime     = Time{
		abDays:    [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
		days:      [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
		abMonths:  [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
		months:    [12]string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
		am:        "AM",
		pm:        "PM",
		dateTime:  "%a %b %e %H:%M:%S %Y",
		date:      "%m/%d/%y",
		clock:     "%H:%M:%S",
		clockAMPM: "%I:%M:%S %p",
	}
)

func parseNumeric(data []byte) (Numeric, error) {
	d, err := parseCategory(lcNumeric, data, grouping)
	if err != nil {
		return Numeric{}, err
	}
	var n Numeric
	if n.decimal, err = d.str(radixChar); err != nil {
		return Numeric{}, err
	}
	if n.decimal == "" {
		return Numeric{}, fmt.Errorf("%s has an empty decimal point", lcNumeric)
	}
	if n.thousands, err = d.str(thousandsSep); err != nil {
		return Numeric{}, err
	}
	if n.grouping, err = d.str(grouping); err != nil {
		return Numeric{}, err
	}
	return n, nil
}

func parseMonetary(data []byte) (Monetary, error) {
	d, err := parseCategory(lcMonetary, data, crncyStr)
	if err != nil {
		return Monetary{}, err
	}
	var m Monetary
	if m.decimal, err = d.str(monDecimalPoint); err != nil {
		return Monetary{}, err
	}
	if m.thousands, err = d.str(monThousandsSep); err != nil {
		return Monetary{}, err
	}
	if m.grouping, err = d.str(monGrouping); err != nil {
		return Monetary{}, err
	}
	sep, err := d.char(pSepBySpace)
	if err != nil {
		return Monetary{}, err
	}
	m.spaced = sep == 1
	currency, err := d.str(crncyStr)
	if err != nil {
		return Monetary{}, err
	}
	if m.placement, m.symbol, err = parseCurrency(currency); err != nil {
		return Monetary{}, err
	}
	return m, nil
}

func parseTime(data []byte) (Time, error) {
	d, err := parseCategory(lcTime, data, eraNumEntries)
	if err != nil {
		return Time{}, err
	}
	var t Time
	for _, a := range []struct {
		first item
		out   []string
	}{
		{abDay1, t.abDays[:]}, {day1, t.days[:]}, {abMon1, t.abMonths[:]}, {mon1, t.months[:]},
	} {
		if err := d.strArray(a.first, a.out); err != nil {
			return Time{}, err
		}
	}
	for _, s := range []struct {
		it  item
		out *string
	}{
		{amStr, &t.am}, {pmStr, &t.pm}, {dTFmt, &t.dateTime}, {dFmt, &t.date}, {tFmt, &t.clock}, {tFmtAmPm, &t.clockAMPM},
	} {
		if *s.out, err = d.str(s.it); err != nil {
			return Time{}, err
		}
	}
	if t.altDigits, err = d.strsUpTo(altDigits, eraDTFmt, maxAltDigits); err != nil {
		return Time{}, err
	}
	n, err := d.word(eraNumEntries)
	if err != nil {
		return Time{}, err
	}
	if n == 0 {
		return t, nil
	}
	if int(n) > len(data) {
		return Time{}, fmt.Errorf("%s claims %d eras in %d bytes", lcTime, n, len(data))
	}
	specs, err := d.strs(eraStrings, int(n))
	if err != nil {
		return Time{}, err
	}
	t.eras = make([]era, 0, len(specs))
	for _, s := range specs {
		e, err := parseEra(s)
		if err != nil {
			return Time{}, fmt.Errorf("%s era %q: %w", lcTime, s, err)
		}
		t.eras = append(t.eras, e)
	}
	return t, nil
}

func parseEra(s string) (era, error) {
	fields := strings.SplitN(s, ":", 6)
	if len(fields) != 6 {
		return era{}, fmt.Errorf("%d fields, want 6", len(fields))
	}
	var e era
	switch fields[0] {
	case "+":
		e.direction = 1
	case "-":
		e.direction = -1
	default:
		return era{}, fmt.Errorf("direction %q is neither + nor -", fields[0])
	}
	var err error
	if e.offset, err = strconv.Atoi(fields[1]); err != nil {
		return era{}, fmt.Errorf("offset: %w", err)
	}
	if e.start, err = parseEraDate(fields[2]); err != nil {
		return era{}, fmt.Errorf("start date: %w", err)
	}
	switch fields[3] {
	case "+*":
		e.stop = eraDate{math.MaxInt, math.MaxInt, math.MaxInt}
	case "-*":
		e.stop = eraDate{math.MinInt, math.MinInt, math.MinInt}
	default:
		if e.stop, err = parseEraDate(fields[3]); err != nil {
			return era{}, fmt.Errorf("end date: %w", err)
		}
	}
	if e.start.compare(e.stop) > 0 {
		e.direction = -e.direction
	}
	return e, nil
}

func parseEraDate(s string) (eraDate, error) {
	y, rest, ok := strings.Cut(s, "/")
	m, day, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 {
		return eraDate{}, fmt.Errorf("%q is not year/month/day", s)
	}
	var d eraDate
	var err error
	if d.year, err = strconv.Atoi(y); err != nil {
		return eraDate{}, fmt.Errorf("year of %q: %w", s, err)
	}
	if d.year < 0 {
		d.year++
	}
	if d.month, err = strconv.Atoi(m); err != nil {
		return eraDate{}, fmt.Errorf("month of %q: %w", s, err)
	}
	if d.day, err = strconv.Atoi(day); err != nil {
		return eraDate{}, fmt.Errorf("day of %q: %w", s, err)
	}
	return d, nil
}

type placement uint8

const (
	beforeAmount placement = iota
	afterAmount
	atRadix
)

func parseCurrency(s string) (placement, string, error) {
	if s == "" {
		return 0, "", fmt.Errorf("%s has an empty currency string", lcMonetary)
	}
	switch s[0] {
	case '-':
		return beforeAmount, s[1:], nil
	case '+':
		return afterAmount, s[1:], nil
	case '.':
		return atRadix, s[1:], nil
	}
	return 0, "", fmt.Errorf("%s currency string %q starts with none of %q", lcMonetary, s, "-+.")
}
