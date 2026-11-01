package fontscan

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/third_party/typesetting/font"
	ot "github.com/stubbedev/lys-lib/third_party/typesetting/font/opentype"
	"github.com/stubbedev/lys-lib/third_party/typesetting/language"
)

// lazyTestMap is a system font filed as a family the sans-serif generic
// substitutes to, then two fonts registered as an application's own.
func lazyTestMap(t *testing.T, system Footprint) *FontMap {
	t.Helper()
	fm := NewFontMap(discardLogger{})
	fm.appendFootprints(system)
	if err := fm.AddFont(ot.NewMapped(renderFont(t, "NotoSansHebrew-Regular.ttf")), "own:hebrew", ""); err != nil {
		t.Fatal(err)
	}
	if err := fm.AddFont(ot.NewMapped(renderFont(t, "Quicksand-VF.ttf")), "own:quicksand", "App Sans"); err != nil {
		t.Fatal(err)
	}
	return fm
}

func lazyTestSystemFont(t *testing.T) Footprint {
	t.Helper()
	system := scanOne(t, renderFont(t, "Cantarell-Regular.ttf"))
	system.Family = "dejavusans"
	return system
}

func TestARegisteredFontIsNotParsedWhileASystemFontAnswers(t *testing.T) {
	fm := lazyTestMap(t, lazyTestSystemFont(t))
	fm.SetQuery(Query{Families: []string{SansSerif}})
	if face := fm.ResolveFace('x'); face == nil || fm.FontLocation(face.Font).File == "" {
		t.Fatalf("resolved %v", face)
	}
	if len(fm.pending) != 2 {
		t.Errorf("%d fonts still pending, want both registered ones", len(fm.pending))
	}
}

func TestARegisteredFontIsParsedWhenAskedFor(t *testing.T) {
	fm := lazyTestMap(t, lazyTestSystemFont(t))
	fm.SetQuery(Query{Families: []string{"App Sans"}})
	face := fm.ResolveFace('x')
	if face == nil || fm.FontLocation(face.Font).File != "own:quicksand" {
		t.Fatalf("resolved %v", fm.FontLocation(face.Font))
	}
	if family, _ := fm.FontMetadata(face.Font); family != "appsans" {
		t.Errorf("metadata family %q", family)
	}
	if len(fm.pending) != 1 {
		t.Errorf("%d fonts still pending, want the one not asked for", len(fm.pending))
	}
}

// Every query answers alike whether the registered fonts are still pending
// or were parsed when they were added.
func TestRegisteredFontsResolveAsIfParsedWhenAdded(t *testing.T) {
	type query struct {
		families []string
		aspect   font.Aspect
		script   language.Script
		r        rune
	}
	bold := font.Aspect{Weight: font.WeightBold}
	italic := font.Aspect{Style: font.StyleItalic}
	var queries []query
	for _, families := range [][]string{
		{SansSerif}, {Serif}, {Monospace}, {"App Sans"}, {"apptypo"}, {"Noto Sans Hebrew"},
		{"No Such Family"}, {"No Such Family", "App Sans"}, {"Quicksand"},
	} {
		for _, aspect := range []font.Aspect{{}, bold, italic} {
			for _, script := range []language.Script{0, language.Latin, language.Hebrew, language.Han} {
				for _, r := range []rune{'x', 'א', '中', '€', 0x10FFFD} {
					queries = append(queries, query{families, aspect, script, r})
				}
			}
		}
	}
	answer := func(fm *FontMap, q query) string {
		fm.SetQuery(Query{Families: q.families, Aspect: q.aspect})
		fm.SetScript(q.script)
		face := fm.ResolveFace(q.r)
		if face == nil {
			return "nil"
		}
		return fmt.Sprint(fm.FontLocation(face.Font))
	}
	system := lazyTestSystemFont(t)
	parsed := lazyTestMap(t, system)
	parsed.realizeAll()
	for _, q := range queries {
		want := answer(parsed, q)
		if got := answer(lazyTestMap(t, system), q); got != want {
			t.Errorf("%+v on a fresh map: %s, parsed when added: %s", q, got, want)
		}
	}
	shared := lazyTestMap(t, system)
	for _, q := range queries {
		if got, want := answer(shared, q), answer(parsed, q); got != want {
			t.Errorf("%+v after earlier queries: %s, parsed when added: %s", q, got, want)
		}
	}
}

func TestTheArbitraryFaceIsTheFirstRegisteredWhateverLoadedBefore(t *testing.T) {
	fm := lazyTestMap(t, lazyTestSystemFont(t))
	fm.SetQuery(Query{Families: []string{SansSerif}})
	if face := fm.ResolveFace('x'); fm.FontLocation(face.Font).File == "own:hebrew" {
		t.Fatal("the system font did not answer first")
	}
	fm.SetQuery(Query{Families: []string{SansSerif}})
	face := fm.ResolveFace(0x10FFFD)
	if got := fm.FontLocation(face.Font).File; got != "own:hebrew" {
		t.Errorf("arbitrary face from %s, want the first one registered", got)
	}
}

func TestAFontThatDoesNotParseIsLoggedAndCoversNothing(t *testing.T) {
	logger := &recordingLogger{}
	fm := NewFontMap(logger)
	broken := renameTable(t, renderFont(t, "Cantarell-Regular.ttf"), "cmap", "xmap")
	if err := fm.AddFont(ot.NewMapped(broken), "own:broken", "Broken"); err != nil {
		t.Fatal(err)
	}
	if err := fm.AddFont(ot.NewMapped(renderFont(t, "Quicksand-VF.ttf")), "own:good", "Good"); err != nil {
		t.Fatal(err)
	}
	fm.SetQuery(Query{Families: []string{"Broken"}})
	face := fm.ResolveFace('x')
	if face == nil || fm.FontLocation(face.Font).File != "own:good" {
		t.Errorf("resolved %v, want the font that parsed", face)
	}
	if !strings.Contains(strings.Join(logger.lines, "\n"), "own:broken") {
		t.Errorf("log %q does not name the broken font", logger.lines)
	}
}

func TestAResourceThatIsNotAFontIsRefusedWhenAdded(t *testing.T) {
	if err := NewFontMap(discardLogger{}).AddFont(ot.NewMapped([]byte("not a font at all")), "own:bad", "Bad"); err == nil {
		t.Error("registered something that is not a font")
	}
}
