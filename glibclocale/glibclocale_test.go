package glibclocale

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/glibclocale/glibclocaletest"
)

var at = time.Date(2026, time.March, 4, 13, 5, 6, 0, time.UTC)

type sources struct{ archive, dirs Source }

func fixtures(t *testing.T) sources {
	t.Helper()
	return sources{archive: NewSource(glibclocaletest.Archive(t)), dirs: NewSource("", glibclocaletest.Dirs(t))}
}

func TestListNamesWhatLocaleAPrints(t *testing.T) {
	f := fixtures(t)
	want := append([]string{"C", "POSIX"}, glibclocaletest.Names...)
	slices.Sort(want)
	for name, src := range map[string]Source{"archive": f.archive, "dirs": f.dirs, "both": NewSource(f.archive.archive, f.dirs.dirs...)} {
		got, err := src.List()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s lists %q, want %q", name, got, want)
		}
	}
}

func TestListSkipsWhatIsNotALocale(t *testing.T) {
	dir := glibclocaletest.Dirs(t)
	if err := os.Mkdir(filepath.Join(dir, "translations-only"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "locale-archive"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := NewSource("", dir).List()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got, "translations-only") || slices.Contains(got, "locale-archive") {
		t.Fatalf("%q lists a directory without LC_IDENTIFICATION or a file", got)
	}
}

func TestASystemWithoutLocaleDataListsOnlyTheBuiltIns(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	src := NewSource(filepath.Join(missing, "locale-archive"), missing)
	got, err := src.List()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"C", "POSIX"}) {
		t.Fatalf("%q, want only C and POSIX", got)
	}
	_, err = src.Time("en_US.UTF-8")
	if err == nil || !strings.Contains(err.Error(), `no locale "en_US.UTF-8"`) || !strings.Contains(err.Error(), "locale-archive (absent)") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("a locale with no data: %v", err)
	}
}

func TestSystemLooksWhereGlibcLooks(t *testing.T) {
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	for _, c := range []struct {
		name string
		vars map[string]string
		want Source
	}{
		{"defaults", nil, Source{archive: defaultArchive, dirs: []string{defaultDir}}},
		{"NixOS", map[string]string{"LOCALE_ARCHIVE": "/run/a", "LOCALE_ARCHIVE_2_27": "/nix/b"}, Source{archive: "/nix/b", dirs: []string{defaultDir}}},
		{"LOCALE_ARCHIVE", map[string]string{"LOCALE_ARCHIVE": "/run/a"}, Source{archive: "/run/a", dirs: []string{defaultDir}}},
		{"LOCPATH replaces the archive", map[string]string{"LOCPATH": "/a::/b", "LOCALE_ARCHIVE": "/run/a"}, Source{dirs: []string{"/a", "/b"}}},
	} {
		if got := System(env(c.vars)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestTheArchiveAndTheDirectoriesReadAlike(t *testing.T) {
	f := fixtures(t)
	for _, name := range glibclocaletest.Names {
		for _, read := range []func(Source) (any, error){
			func(s Source) (any, error) { return s.Numeric(name) },
			func(s Source) (any, error) { return s.Monetary(name) },
			func(s Source) (any, error) { return s.Time(name) },
		} {
			a, err := read(f.archive)
			if err != nil {
				t.Fatalf("%s from the archive: %v", name, err)
			}
			d, err := read(f.dirs)
			if err != nil {
				t.Fatalf("%s from the directories: %v", name, err)
			}
			if !reflect.DeepEqual(a, d) {
				t.Fatalf("%s: the archive reads %+v, the directories %+v", name, a, d)
			}
		}
	}
}

func TestTheFormatsAreGlibcs(t *testing.T) {
	f := fixtures(t)
	for _, c := range []struct {
		name, dateTime, date, number, price string
	}{
		{"C", "Wed Mar  4 13:05:06 2026", "03/04/26", "1234567.89", "1234.50"},
		{"POSIX", "Wed Mar  4 13:05:06 2026", "03/04/26", "1234567.89", "1234.50"},
		{"de_DE.UTF-8", "Mi 04 Mär 2026 13:05:06 UTC", "04.03.2026", "1.234.567,89", "1.234,50 €"},
		{"en_GB.utf8", "Wed 04 Mar 2026 13:05:06 UTC", "04/03/26", "1,234,567.89", "£1,234.50"},
		{"en_US.UTF-8", "Wed 04 Mar 2026 01:05:06 PM UTC", "03/04/2026", "1,234,567.89", "$1,234.50"},
		{"fa_IR.UTF-8", "\u202bچهارشنبه ۰۴ مارس ۲۶، ۱۳:۰۵:۰۶\u202c", "۲۶/۰۳/۰۴", "1,234,567.89", "1٬234٫50 ریال"},
		{"th_TH.UTF-8", "พ.  4 มี.ค. 2569, 13:05:06", "04/03/2569", "1,234,567.89", "฿1,234.50"},
	} {
		for kind, src := range map[string]Source{"archive": f.archive, "dirs": f.dirs} {
			tm, err := src.Time(c.name)
			if err != nil {
				t.Fatalf("%s from the %s: %v", c.name, kind, err)
			}
			n, err := src.Numeric(c.name)
			if err != nil {
				t.Fatalf("%s from the %s: %v", c.name, kind, err)
			}
			m, err := src.Monetary(c.name)
			if err != nil {
				t.Fatalf("%s from the %s: %v", c.name, kind, err)
			}
			for format, want := range map[string]string{"%c": c.dateTime, "%x": c.date} {
				if got, err := tm.Format(format, at); err != nil || got != want {
					t.Errorf("%s from the %s: %s is %q (%v), want %q", c.name, kind, format, got, err, want)
				}
			}
			if got := n.Format(123456789, 2); got != c.number {
				t.Errorf("%s from the %s: the number is %q, want %q", c.name, kind, got, c.number)
			}
			if got := m.Format(123450, 2, n); got != c.price {
				t.Errorf("%s from the %s: the price is %q, want %q", c.name, kind, got, c.price)
			}
		}
	}
}

func TestNumbersGroupAsTheLocaleSays(t *testing.T) {
	for _, c := range []struct {
		grouping string
		units    uint64
		want     string
	}{
		{"\x03", 1234567, "1,234,567"},
		{"\x03\x02", 123456789, "12,34,56,789"},
		{"\x03\x7f", 123456789, "123456,789"},
		{"", 123456789, "123456789"},
		{"\x03", 999, "999"},
		{"\x03", 0, "0"},
	} {
		if got := (Numeric{decimal: ".", thousands: ",", grouping: c.grouping}).Format(c.units, 0); got != c.want {
			t.Errorf("%d grouped by %q: %q, want %q", c.units, c.grouping, got, c.want)
		}
	}
	if got := cNumeric.Format(5, 3); got != "0.005" {
		t.Errorf("5 at scale 3: %q", got)
	}
}

func TestMoneyPlacesItsSymbol(t *testing.T) {
	n := Numeric{decimal: ","}
	for _, c := range []struct {
		m    Monetary
		want string
	}{
		{Monetary{decimal: ".", symbol: "$", placement: beforeAmount}, "$12.50"},
		{Monetary{decimal: ".", symbol: "€", placement: beforeAmount, spaced: true}, "€ 12.50"},
		{Monetary{decimal: ".", symbol: "kr", placement: afterAmount}, "12.50kr"},
		{Monetary{symbol: "$", placement: atRadix}, "12$50"},
		{Monetary{}, "12,50"},
	} {
		if got := c.m.Format(1250, 2, n); got != c.want {
			t.Errorf("%+v: %q, want %q", c.m, got, c.want)
		}
	}
}

func TestStrftimeOutsideTheLocalesFormats(t *testing.T) {
	plus := time.Date(2026, time.March, 4, 9, 5, 6, 0, time.FixedZone("NPT", 5*3600+45*60))
	minus := time.Date(2026, time.March, 4, 0, 5, 6, 0, time.FixedZone("NST", -(3*3600+30*60)))
	tm := cTime
	for _, c := range []struct {
		format string
		at     time.Time
		want   string
	}{
		{"%-d.%-m. %e|%k|%l|%P|%I", plus, "4.3.  4| 9| 9|am|09"},
		{"%z %Z %-z", plus, "+0545 NPT +545"},
		{"%z %Z", minus, "-0330 NST"},
		{"%T %R %F %r", minus, "00:05:06 00:05 2026-03-04 12:05:06 AM"},
		{"%C %y %Y", time.Date(999, 1, 1, 0, 0, 0, 0, time.UTC), "9 99 999"},
		{"%:z %q %Oa %Ed %OY %Ox 100%", plus, "%:z %q %Oa %Ed %OY %Ox 100%"},
		{"%Op %EZ %O%", plus, "AM NPT %"},
	} {
		if got, err := tm.Format(c.format, c.at); err != nil || got != c.want {
			t.Errorf("%q: %q (%v), want %q", c.format, got, err, c.want)
		}
	}
	for _, format := range []string{"%j", "%_d", "%^a", "%5d", "%Ob", "%EC", "%Ec", "%OD"} {
		if _, err := tm.Format(format, plus); err == nil || !strings.Contains(err.Error(), format) {
			t.Errorf("%q: %v, want an error naming it", format, err)
		}
	}
	loop := cTime
	loop.dateTime = "%c"
	if _, err := loop.Format("%c", plus); err == nil || !strings.Contains(err.Error(), "nest") {
		t.Errorf("a format that holds itself: %v", err)
	}
}

func TestErasCountAsGlibcCounts(t *testing.T) {
	tm := cTime
	for _, s := range []string{"+:1:1912/01/01:+*:民國:%EC%Ey年", "+:1:1911/12/31:-*:民前:%EC%Ey年"} {
		e, err := parseEra(s)
		if err != nil {
			t.Fatal(err)
		}
		tm.eras = append(tm.eras, e)
	}
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "115"},
		{time.Date(1912, 1, 1, 0, 0, 0, 0, time.UTC), "01"},
		{time.Date(1911, 12, 31, 0, 0, 0, 0, time.UTC), "01"},
		{time.Date(1900, 6, 15, 0, 0, 0, 0, time.UTC), "12"},
	} {
		if got, err := tm.Format("%Ey", c.at); err != nil || got != c.want {
			t.Errorf("%s: %q (%v), want %q", c.at, got, err, c.want)
		}
	}
	bc, err := parseEra("+:1:-543/01/01:+*:พ.ศ.:%EC %Ey")
	if err != nil {
		t.Fatal(err)
	}
	tm.eras = []era{bc}
	if got, _ := tm.Format("%Ey", at); got != "2569" {
		t.Errorf("the Buddhist era in 2026: %q, want 2569", got)
	}
	for _, bad := range []string{"+:1:2000/01/01:+*:x", "*:1:2000/01/01:+*:x:y", "+:one:2000/01/01:+*:x:y", "+:1:2000-01-01:+*:x:y"} {
		if _, err := parseEra(bad); err == nil {
			t.Errorf("era %q parsed", bad)
		}
	}
}

func TestMissingDataIsAnErrorNamingIt(t *testing.T) {
	f := fixtures(t)
	if _, err := f.archive.Time("xx_NOPE.UTF-8"); err == nil || !strings.Contains(err.Error(), "xx_NOPE.UTF-8") || !strings.Contains(err.Error(), f.archive.archive) {
		t.Fatalf("an unknown locale in the archive: %v", err)
	}
	dir := f.dirs.dirs[0]
	if err := os.Remove(filepath.Join(dir, "en_US.utf8", "LC_TIME")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dirs.Time("en_US.UTF-8"); err == nil || !strings.Contains(err.Error(), "has no LC_TIME") {
		t.Fatalf("a locale without LC_TIME: %v", err)
	}
	if _, err := f.dirs.Numeric("en_US.UTF-8"); err != nil {
		t.Fatalf("its other categories: %v", err)
	}
	for _, name := range []string{"", ".", "..", "../en_US.utf8", "en/US"} {
		if _, err := f.dirs.Numeric(name); err == nil || !strings.Contains(err.Error(), "not a locale name") {
			t.Errorf("%q: %v", name, err)
		}
	}
	if _, err := NewSource("").Numeric("en_US.UTF-8"); err == nil || !strings.Contains(err.Error(), "no archive or directories") {
		t.Errorf("an empty source: %v", err)
	}
}

func TestACorruptArchiveIsAnError(t *testing.T) {
	path := glibclocaletest.Archive(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		data      []byte
		listFails bool
	}{
		"empty":     {nil, true},
		"short":     {raw[:20], true},
		"bad magic": {append([]byte("nope"), raw[4:]...), true},
		"truncated": {raw[:len(raw)/2], false},
	} {
		bad := filepath.Join(t.TempDir(), "locale-archive")
		if err := os.WriteFile(bad, c.data, 0o644); err != nil {
			t.Fatal(err)
		}
		src := NewSource(bad, glibclocaletest.Dirs(t))
		_, listErr := src.List()
		_, loadErr := src.Time("en_US.UTF-8")
		if (listErr != nil) != c.listFails || loadErr == nil || !strings.Contains(loadErr.Error(), bad) {
			t.Errorf("%s: list %v, load %v", name, listErr, loadErr)
		}
	}
}

func TestNormalizeIsGlibcs(t *testing.T) {
	for in, want := range map[string]string{
		"en_US.UTF-8":          "en_US.utf8",
		"de_DE.ISO-8859-15":    "de_DE.iso885915",
		"ja_JP.eucJP":          "ja_JP.eucjp",
		"sr_RS.UTF-8@latin":    "sr_RS.utf8@latin",
		"xx.8859":              "xx.iso8859",
		"en_US":                "en_US",
		"en_US.":               "en_US.",
		"nb_NO.@euro":          "nb_NO.@euro",
		"ca_ES.UTF-8@valencia": "ca_ES.utf8@valencia",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("%q normalizes to %q, want %q", in, got, want)
		}
	}
}

func FuzzParseArchive(f *testing.F) {
	raw, err := os.ReadFile(glibclocaletest.Archive(f))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add(raw[:archiveHeader])
	f.Fuzz(func(t *testing.T, data []byte) {
		a, err := parseArchive(data)
		if err != nil {
			return
		}
		names, err := a.localeNames()
		if err != nil {
			return
		}
		for _, name := range names {
			record, ok, err := a.find(name)
			if err != nil || !ok {
				continue
			}
			for _, c := range []category{lcNumeric, lcMonetary, lcTime} {
				if d, err := a.category(record, c); err == nil {
					parseAll(d)
				}
			}
		}
	})
}

func FuzzParseCategory(f *testing.F) {
	dir := glibclocaletest.Dirs(f)
	for _, name := range glibclocaletest.Names {
		for _, c := range []category{lcNumeric, lcMonetary, lcTime} {
			raw, err := os.ReadFile(filepath.Join(dir, name, c.String()))
			if err != nil {
				f.Fatal(err)
			}
			f.Add(raw)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) { parseAll(data) })
}

func parseAll(data []byte) {
	_, _ = parseNumeric(data)
	_, _ = parseMonetary(data)
	if tm, err := parseTime(data); err == nil {
		_, _ = tm.Format("%c %x %X %r %Ey %OH %Oy", at)
	}
}

func TestInstalledIsWhatNewlocaleWouldFind(t *testing.T) {
	f := fixtures(t)
	dir := f.dirs.dirs[0]
	if err := os.WriteFile(filepath.Join(dir, "en_US.utf8", "LC_CTYPE"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	for _, c := range []struct {
		src  Source
		name string
		want bool
	}{
		{f.dirs, "en_US.UTF-8", true},
		{f.dirs, "en_US.utf8", true},
		{f.dirs, "C", true},
		{f.dirs, "POSIX", true},
		{f.dirs, "zz_ZZ.UTF-8", false},
		{f.archive, "de_DE.UTF-8", true},
		{f.archive, "zz_ZZ.UTF-8", false},
		{NewSource(filepath.Join(missing, "locale-archive"), missing), "en_US.UTF-8", false},
	} {
		got, err := c.src.Installed(c.name)
		if err != nil || got != c.want {
			t.Errorf("Installed(%q) in %+v = %t, %v; want %t", c.name, c.src, got, err, c.want)
		}
	}
	if _, err := f.dirs.Installed("de_DE.UTF-8"); err == nil || !strings.Contains(err.Error(), "has no LC_CTYPE") {
		t.Errorf("a locale directory without LC_CTYPE: %v", err)
	}
	if _, err := f.dirs.Installed("../en_US.utf8"); err == nil || !strings.Contains(err.Error(), "not a locale name") {
		t.Errorf("a path as a locale name: %v", err)
	}
}
