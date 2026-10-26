package render

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
)

// Variation is one setting of a variable font's design axis: an
// OpenType axis tag ("wght", "wdth", "slnt", "ital", "opsz", or a
// font's own) and a value in that axis's design units (wght 100-900).
type Variation struct {
	Tag   string
	Value float32
}

// instances is a face's variation bookkeeping, shared by the default
// face and every instance made from it: the instances by settings (so
// one setting is one Typeface, and the shaping and glyph caches keep
// hitting) and the axes probed so far.
type instances struct {
	mu    sync.Mutex
	byKey map[string]*Typeface
	axes  map[string]bool
}

// family returns t's shared bookkeeping, creating it on the default
// face.
func (t *Typeface) family() *instances {
	if t.inst == nil {
		t.inst = &instances{byKey: map[string]*Typeface{}, axes: map[string]bool{}}
	}
	return t.inst
}

// HasAxis reports whether the face is variable along tag: setting the
// axis to its two extremes yields different coordinates.
func (t *Typeface) HasAxis(tag string) bool {
	if len(tag) != 4 {
		return false
	}
	inst := t.family()
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if has, ok := inst.axes[tag]; ok {
		return has
	}
	probe := func(v float32) []font.VarCoord {
		f := font.NewFace(t.face.Font)
		f.SetVariations([]font.Variation{{Tag: ot.MustNewTag(tag), Value: v}})
		return f.Coords()
	}
	has := !slices.Equal(probe(-math.MaxFloat32), probe(math.MaxFloat32))
	inst.axes[tag] = has
	return has
}

// Variations reports the settings this face was instantiated at; nil
// for a face at its default instance.
func (t *Typeface) Variations() []Variation { return slices.Clone(t.vars) }

// WithVariations returns the face instantiated at vs (CSS
// font-variation-settings, a named instance's coordinates): axes the
// font lacks are ignored, values clamp to each axis's range, and
// unnamed axes keep their defaults - settings apply over the default
// instance, not over t's own. A face with none of the axes, or empty
// vs, returns the default face. Instances are memoized per setting.
func (t *Typeface) WithVariations(vs ...Variation) *Typeface {
	base := t
	if t.base != nil {
		base = t.base
	}
	var kept []Variation
	for _, v := range vs {
		if base.HasAxis(v.Tag) {
			kept = append(kept, v)
		}
	}
	if len(kept) == 0 {
		return base
	}
	slices.SortStableFunc(kept, func(a, b Variation) int { return strings.Compare(a.Tag, b.Tag) })
	var kb strings.Builder
	kb.WriteString(variationKey(kept))
	for _, f := range t.features { // a tabular twin's instances are its own
		fmt.Fprintf(&kb, "+%s=%d", f.Tag, f.Value)
	}
	key := kb.String()
	inst := base.family()
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if v, ok := inst.byKey[key]; ok {
		return v
	}
	f := font.NewFace(base.face.Font)
	axes := make([]font.Variation, len(kept))
	for i, v := range kept {
		axes[i] = font.Variation{Tag: ot.MustNewTag(v.Tag), Value: v.Value}
	}
	f.SetVariations(axes)
	v := &Typeface{face: f, upem: base.upem, features: t.features, base: base, vars: kept, inst: inst}
	inst.byKey[key] = v
	return v
}

// Weighted is the face at CSS weight w along its wght axis - a
// variable family's real weight rather than its default instance; a
// face without the axis returns itself.
func (t *Typeface) Weighted(w int) *Typeface {
	if !t.HasAxis("wght") {
		return t
	}
	return t.WithVariations(Variation{Tag: "wght", Value: float32(w)})
}

// variationKey names a sorted setting.
func variationKey(vs []Variation) string {
	var b strings.Builder
	for _, v := range vs {
		fmt.Fprintf(&b, "%s=%g;", v.Tag, v.Value)
	}
	return b.String()
}

// WithVariations returns the chain shaping with every face instantiated
// at vs (each face ignores axes it lacks); faces the store resolves for
// uncovered runes shape at their defaults.
func (c *Chain) WithVariations(vs ...Variation) *Chain {
	if len(vs) == 0 {
		return c
	}
	sorted := slices.Clone(vs)
	slices.SortStableFunc(sorted, func(a, b Variation) int { return strings.Compare(a.Tag, b.Tag) })
	return c.variant("var:"+variationKey(sorted), func(n *Chain) {
		n.primary = c.primary.WithVariations(vs...)
		n.extra = make([]*Typeface, len(c.extra))
		for i, e := range c.extra {
			n.extra[i] = e.WithVariations(vs...)
		}
	})
}

// Weighted is the chain at CSS weight w (see Typeface.Weighted).
func (c *Chain) Weighted(w int) *Chain {
	return c.WithVariations(Variation{Tag: "wght", Value: float32(w)})
}

// ParseVariations parses a CSS font-variation-settings value - "normal",
// or comma-separated quoted four-letter tags and numbers ("wght" 650,
// "wdth" 80).
func ParseVariations(s string) ([]Variation, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "normal" {
		return nil, nil
	}
	var out []Variation
	for part := range strings.SplitSeq(s, ",") {
		fields := strings.Fields(part)
		if len(fields) != 2 {
			return nil, fmt.Errorf("render: font-variation-settings %q: want \"tag\" value", part)
		}
		tag := strings.Trim(fields[0], `"'`)
		if len(tag) != 4 {
			return nil, fmt.Errorf("render: font-variation-settings: tag %q is not four letters", tag)
		}
		var v float32
		if _, err := fmt.Sscanf(fields[1], "%g", &v); err != nil {
			return nil, fmt.Errorf("render: font-variation-settings: value %q: %w", fields[1], err)
		}
		out = append(out, Variation{Tag: tag, Value: v})
	}
	return out, nil
}
