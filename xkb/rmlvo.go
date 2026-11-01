package xkb

import (
	"errors"
	"fmt"
	"slices"
)

type builderLayout struct{ layout, variant string }

type builderOption struct {
	option string
	layout uint32
}

// RMLVOBuilder assembles layouts, variants and options incrementally, with
// options that apply to one layout only.
type RMLVOBuilder struct {
	ctx          *Context
	rules, model string
	layouts      []builderLayout
	options      []builderOption
}

// NewRMLVOBuilder starts a builder; empty rules and model take the context's
// defaults.
func (c *Context) NewRMLVOBuilder(rules, model string) *RMLVOBuilder {
	return &RMLVOBuilder{ctx: c, rules: rules, model: model}
}

// AppendLayout adds a layout with an optional variant and options specific
// to it, failing beyond 32 layouts.
func (b *RMLVOBuilder) AppendLayout(layout, variant string, options ...string) error {
	idx := uint32(len(b.layouts)) //nolint:gosec // at most maxGroups
	if idx >= maxGroups {
		b.ctx.log(LogError, 0, 237, "Maximum layout count reached: %d; cannot add layout \"%s(%s)\" to the RMLVO builder.\n", maxGroups, layout, variant)
		return fmt.Errorf("xkb: append layout %s(%s): at most %d layouts", layout, variant, maxGroups)
	}
	b.layouts = append(b.layouts, builderLayout{layout, variant})
	for _, o := range options {
		b.options = append(b.options, builderOption{o, idx})
	}
	return nil
}

// AppendOption adds an option for every layout, once.
func (b *RMLVOBuilder) AppendOption(option string) error {
	if option == "" {
		return errors.New("xkb: append option: an empty option")
	}
	if !slices.Contains(b.options, builderOption{option, allLayouts}) {
		b.options = append(b.options, builderOption{option, allLayouts})
	}
	return nil
}

func (b *RMLVOBuilder) matcher(s *session) (*matcher, string, Names) {
	names := Names{Rules: b.rules, Model: b.model}
	if len(b.layouts) > 0 {
		names.Layout, names.Variant = "x", "x"
	}
	if len(b.options) > 0 {
		names.Options = "x"
	}
	sane := b.ctx.sanitize(names)
	m := newMatcher(s, sane)
	if len(b.layouts) > 0 {
		m.layouts, m.vars = m.layouts[:0], m.vars[:0]
		for _, l := range b.layouts {
			m.layouts = append(m.layouts, matchedValue{value: l.layout, layout: allLayouts})
			m.vars = append(m.vars, matchedValue{value: l.variant, layout: allLayouts})
		}
	}
	if len(b.options) > 0 {
		m.options = m.options[:0]
		for _, o := range b.options {
			m.options = append(m.options, matchedValue{value: o.option, layout: o.layout})
		}
	}
	return m, sane.Rules, sane
}

// NewKeymap compiles the builder's keymap in a format.
func (b *RMLVOBuilder) NewKeymap(format KeymapFormat) (*Keymap, error) {
	if !format.valid() {
		return nil, fmt.Errorf("xkb: compile keymap: unsupported format %d", format)
	}
	s := b.ctx.newSession(format)
	if len(s.paths) == 0 {
		return nil, fmt.Errorf("compile keymap: %w", ErrNoData)
	}
	m, rules, _ := b.matcher(s)
	kccgst, layouts, err := s.resolveWith(m, rules)
	if err != nil {
		b.ctx.log(LogError, 0, keymapCompilationFailed, "Couldn't look up rules from the RMLVO builder\n")
		return nil, fmt.Errorf("xkb: resolve keymap: %w", err)
	}
	km, err := b.ctx.compileComponents(kccgst, layouts, s, format)
	if err != nil {
		return nil, fmt.Errorf("xkb: compile keymap: %w", err)
	}
	return km, nil
}
