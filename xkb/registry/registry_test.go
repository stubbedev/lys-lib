package registry

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ebitengine/purego"

	"github.com/stubbedev/lys-lib/clib"
)

type cRegistry struct {
	newCtx        func(flags int32) uintptr
	appendInclude func(ctx uintptr, path *byte) bool
	parse         func(ctx uintptr, ruleset *byte) bool
	modelFirst    func(ctx uintptr) uintptr
	modelNext     func(m uintptr) uintptr
	modelName     func(m uintptr) *byte
	modelDesc     func(m uintptr) *byte
	modelVendor   func(m uintptr) *byte
	modelPop      func(m uintptr) int32
	layoutFirst   func(ctx uintptr) uintptr
	layoutNext    func(l uintptr) uintptr
	layoutName    func(l uintptr) *byte
	layoutVariant func(l uintptr) *byte
	layoutBrief   func(l uintptr) *byte
	layoutDesc    func(l uintptr) *byte
	layoutPop     func(l uintptr) int32
	iso639First   func(l uintptr) uintptr
	iso639Next    func(c uintptr) uintptr
	iso639Code    func(c uintptr) *byte
	iso3166First  func(l uintptr) uintptr
	iso3166Next   func(c uintptr) uintptr
	iso3166Code   func(c uintptr) *byte
	groupFirst    func(ctx uintptr) uintptr
	groupNext     func(g uintptr) uintptr
	groupName     func(g uintptr) *byte
	groupDesc     func(g uintptr) *byte
	groupPop      func(g uintptr) int32
	groupMulti    func(g uintptr) bool
	optionFirst   func(g uintptr) uintptr
	optionNext    func(o uintptr) uintptr
	optionName    func(o uintptr) *byte
	optionBrief   func(o uintptr) *byte
	optionDesc    func(o uintptr) *byte
	optionPop     func(o uintptr) int32
	optionSpec    func(o uintptr) bool
}

func openRegistry(t *testing.T) *cRegistry {
	t.Helper()
	h, err := clib.Open(clib.SystemCandidates("libxkbregistry.so.0"))
	if err != nil {
		t.Fatalf("libxkbregistry: %v", err)
	}
	c := &cRegistry{}
	for _, f := range []struct {
		fn   any
		name string
	}{
		{&c.newCtx, "rxkb_context_new"},
		{&c.appendInclude, "rxkb_context_include_path_append"},
		{&c.parse, "rxkb_context_parse"},
		{&c.modelFirst, "rxkb_model_first"},
		{&c.modelNext, "rxkb_model_next"},
		{&c.modelName, "rxkb_model_get_name"},
		{&c.modelDesc, "rxkb_model_get_description"},
		{&c.modelVendor, "rxkb_model_get_vendor"},
		{&c.modelPop, "rxkb_model_get_popularity"},
		{&c.layoutFirst, "rxkb_layout_first"},
		{&c.layoutNext, "rxkb_layout_next"},
		{&c.layoutName, "rxkb_layout_get_name"},
		{&c.layoutVariant, "rxkb_layout_get_variant"},
		{&c.layoutBrief, "rxkb_layout_get_brief"},
		{&c.layoutDesc, "rxkb_layout_get_description"},
		{&c.layoutPop, "rxkb_layout_get_popularity"},
		{&c.iso639First, "rxkb_layout_get_iso639_first"},
		{&c.iso639Next, "rxkb_iso639_code_next"},
		{&c.iso639Code, "rxkb_iso639_code_get_code"},
		{&c.iso3166First, "rxkb_layout_get_iso3166_first"},
		{&c.iso3166Next, "rxkb_iso3166_code_next"},
		{&c.iso3166Code, "rxkb_iso3166_code_get_code"},
		{&c.groupFirst, "rxkb_option_group_first"},
		{&c.groupNext, "rxkb_option_group_next"},
		{&c.groupName, "rxkb_option_group_get_name"},
		{&c.groupDesc, "rxkb_option_group_get_description"},
		{&c.groupPop, "rxkb_option_group_get_popularity"},
		{&c.groupMulti, "rxkb_option_group_allows_multiple"},
		{&c.optionFirst, "rxkb_option_first"},
		{&c.optionNext, "rxkb_option_next"},
		{&c.optionName, "rxkb_option_get_name"},
		{&c.optionBrief, "rxkb_option_get_brief"},
		{&c.optionDesc, "rxkb_option_get_description"},
		{&c.optionPop, "rxkb_option_get_popularity"},
		{&c.optionSpec, "rxkb_option_is_layout_specific"},
	} {
		if _, err := purego.Dlsym(h, f.name); err != nil {
			t.Fatal(err)
		}
		purego.RegisterLibFunc(f.fn, h, f.name)
	}
	return c
}

func cstr(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

func codes(first uintptr, next func(uintptr) uintptr, code func(uintptr) *byte) []string {
	var out []string
	for p := first; p != 0; p = next(p) {
		out = append(out, clib.GoString(code(p)))
	}
	return out
}

func compareRegistry(t *testing.T, flags Flags, cflags int32, dirs []string, ruleset string) {
	t.Helper()
	c := openRegistry(t)
	cc := c.newCtx(cflags | 1)
	g, err := NewContext(func(string) string { return "" }, flags|NoDefaultIncludes)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if a, b := g.AppendIncludePath(d), c.appendInclude(cc, cstr(d)); a != b {
			t.Fatalf("append %s: go %v, c %v", d, a, b)
		}
	}
	gerr := g.Parse(ruleset)
	ok := c.parse(cc, cstr(ruleset))
	if (gerr == nil) != ok {
		t.Fatalf("parse %s: go %v, c %v", ruleset, gerr, ok)
	}
	if !ok {
		return
	}
	i := 0
	models := g.Models()
	for m := c.modelFirst(cc); m != 0; m = c.modelNext(m) {
		if i >= len(models) {
			t.Fatalf("c has more than %d models", len(models))
		}
		x := models[i]
		if x.Name != clib.GoString(c.modelName(m)) || x.Description != clib.GoString(c.modelDesc(m)) || x.Vendor != clib.GoString(c.modelVendor(m)) || int32(x.Popularity) != c.modelPop(m) {
			t.Errorf("model %d: go %+v, c %q %q %q %d", i, x, clib.GoString(c.modelName(m)), clib.GoString(c.modelDesc(m)), clib.GoString(c.modelVendor(m)), c.modelPop(m))
		}
		i++
	}
	if i != len(models) {
		t.Errorf("models: go %d, c %d", len(models), i)
	}
	i = 0
	layouts := g.Layouts()
	for l := c.layoutFirst(cc); l != 0; l = c.layoutNext(l) {
		if i >= len(layouts) {
			t.Fatalf("c has more than %d layouts", len(layouts))
		}
		x := layouts[i]
		if x.Name != clib.GoString(c.layoutName(l)) || x.Variant != clib.GoString(c.layoutVariant(l)) || x.Brief != clib.GoString(c.layoutBrief(l)) || x.Description != clib.GoString(c.layoutDesc(l)) || int32(x.Popularity) != c.layoutPop(l) {
			t.Errorf("layout %d: go %+v, c %q %q %q %q %d", i, x, clib.GoString(c.layoutName(l)), clib.GoString(c.layoutVariant(l)), clib.GoString(c.layoutBrief(l)), clib.GoString(c.layoutDesc(l)), c.layoutPop(l))
		}
		if a, b := x.ISO639, codes(c.iso639First(l), c.iso639Next, c.iso639Code); !slices.Equal(a, b) {
			t.Errorf("layout %s(%s) iso639: go %v, c %v", x.Name, x.Variant, a, b)
		}
		if a, b := x.ISO3166, codes(c.iso3166First(l), c.iso3166Next, c.iso3166Code); !slices.Equal(a, b) {
			t.Errorf("layout %s(%s) iso3166: go %v, c %v", x.Name, x.Variant, a, b)
		}
		i++
	}
	if i != len(layouts) {
		t.Errorf("layouts: go %d, c %d", len(layouts), i)
	}
	i = 0
	groups := g.OptionGroups()
	for gr := c.groupFirst(cc); gr != 0; gr = c.groupNext(gr) {
		if i >= len(groups) {
			t.Fatalf("c has more than %d groups", len(groups))
		}
		x := groups[i]
		if x.Name != clib.GoString(c.groupName(gr)) || x.Description != clib.GoString(c.groupDesc(gr)) || int32(x.Popularity) != c.groupPop(gr) || x.AllowsMultiple != c.groupMulti(gr) {
			t.Errorf("group %d: go %+v, c %q", i, x, clib.GoString(c.groupName(gr)))
		}
		j := 0
		for o := c.optionFirst(gr); o != 0; o = c.optionNext(o) {
			if j >= len(x.Options) {
				t.Fatalf("group %s: c has more than %d options", x.Name, len(x.Options))
			}
			y := x.Options[j]
			if y.Name != clib.GoString(c.optionName(o)) || y.Brief != clib.GoString(c.optionBrief(o)) || y.Description != clib.GoString(c.optionDesc(o)) || int32(y.Popularity) != c.optionPop(o) || y.LayoutSpecific != c.optionSpec(o) {
				t.Errorf("option %s: go %+v, c %q %q %q %d %v", x.Name, y, clib.GoString(c.optionName(o)), clib.GoString(c.optionBrief(o)), clib.GoString(c.optionDesc(o)), c.optionPop(o), c.optionSpec(o))
			}
			j++
		}
		if j != len(x.Options) {
			t.Errorf("group %s options: go %d, c %d", x.Name, len(x.Options), j)
		}
		i++
	}
	if i != len(groups) {
		t.Errorf("groups: go %d, c %d", len(groups), i)
	}
}

func dataRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("XKB_CONFIG_ROOT")
	if root == "" {
		t.Fatal("XKB_CONFIG_ROOT is not set: the dev shell points it at xkeyboard-config")
	}
	return root
}

func TestRegistryMatchesLibxkbregistry(t *testing.T) {
	root := dataRoot(t)
	compareRegistry(t, 0, 0, []string{root}, "evdev")
	compareRegistry(t, LoadExoticRules, 2, []string{root}, "evdev")
	compareRegistry(t, LoadExoticRules, 2, []string{root}, "base")
}

func writeRules(t *testing.T, dir, name, text string) {
	t.Helper()
	rules := filepath.Join(dir, "rules")
	if err := os.MkdirAll(rules, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rules, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryEdgeCasesMatchLibxkbregistry(t *testing.T) {
	const good = `<?xml version="1.0"?>
<xkbConfigRegistry version="1.1">
<modelList><model><configItem popularity="exotic"><name>m</name><description>d</description><vendor>v</vendor></configItem></model></modelList>
<layoutList><layout><configItem><name>l</name><shortDescription>s</shortDescription><description>d &amp; e</description><countryList><iso3166Id>DK</iso3166Id></countryList><languageList><iso639Id>dan</iso639Id></languageList></configItem>
<variantList><variant><configItem><name>v1</name></configItem></variant><variant><configItem><name>v2</name><shortDescription>x</shortDescription><countryList><iso3166Id>SE</iso3166Id></countryList></configItem></variant></variantList></layout></layoutList>
<optionList><group allowMultipleSelection="true"><configItem><name>g</name></configItem><option><configItem layout-specific="true"><name>g:o</name><shortDescription>b</shortDescription><description>o</description></configItem></option></group></optionList>
</xkbConfigRegistry>`
	for name, doc := range map[string]string{
		"good":        good,
		"overlay":     strings.ReplaceAll(good, "<name>m</name>", "<name>m2</name>"),
		"empty":       `<?xml version="1.0"?><xkbConfigRegistry/>`,
		"wrong root":  `<?xml version="1.0"?><other/>`,
		"bad element": `<?xml version="1.0"?><xkbConfigRegistry><modelList><nope/></modelList></xkbConfigRegistry>`,
		"bad attr":    `<?xml version="1.0"?><xkbConfigRegistry><modelList><model><configItem popularity="x"><name>m</name></configItem></model></modelList></xkbConfigRegistry>`,
		"no name":     `<?xml version="1.0"?><xkbConfigRegistry><modelList><model><configItem><description>d</description></configItem></model></modelList></xkbConfigRegistry>`,
		"bad order":   `<?xml version="1.0"?><xkbConfigRegistry><layoutList/><modelList/></xkbConfigRegistry>`,
		"not xml":     `this is not xml`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeRules(t, dir, "rs.xml", doc)
			compareRegistry(t, 0, 0, []string{dir}, "rs")
			second := t.TempDir()
			writeRules(t, second, "rs.xml", good)
			compareRegistry(t, 0, 0, []string{second, dir}, "rs")
		})
	}
}

func TestParseOnlyOnce(t *testing.T) {
	root := dataRoot(t)
	c, err := NewContext(func(string) string { return "" }, NoDefaultIncludes)
	if err != nil {
		t.Fatal(err)
	}
	c.SetLogLevel(10)
	if !c.AppendIncludePath(root) {
		t.Fatal("append the data root")
	}
	if err := c.ParseDefaultRuleset(); err != nil {
		t.Fatal(err)
	}
	if c.Parse("evdev") == nil {
		t.Error("a second Parse succeeded")
	}
	if c.AppendIncludePath(root) {
		t.Error("appending after Parse succeeded")
	}
}
