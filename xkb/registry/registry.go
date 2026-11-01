// Package registry lists the models, layouts, variants and options a ruleset
// offers, from xkeyboard-config's XML files, the way libxkbregistry does. It
// lives apart from package xkb so that compositors and clients do not link an
// XML parser for a configuration interface's needs.
package registry

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/stubbedev/lys-lib/xkb"
)

// Flags adjusts NewContext.
type Flags uint8

const (
	// NoDefaultIncludes starts with an empty include path.
	NoDefaultIncludes Flags = 1 << iota
	// LoadExoticRules also reads the ruleset's .extras.xml files.
	LoadExoticRules
	// NoSecureGetenv reads the environment even when the process runs setuid
	// or setgid.
	NoSecureGetenv
)

// Popularity is how common an entry is.
type Popularity uint8

const (
	// Standard is an entry of the ruleset's main file.
	Standard Popularity = iota + 1
	// Exotic is an entry of the ruleset's extras file, or one marked exotic.
	Exotic
)

// Model is a keyboard model.
type Model struct {
	Name, Description, Vendor string
	Popularity                Popularity
}

// Layout is a layout, or a variant of one when Variant is set.
type Layout struct {
	Name, Variant, Brief, Description string
	Popularity                        Popularity
	ISO639, ISO3166                   []string
}

// Option is one XKB option.
type Option struct {
	Name, Brief, Description string
	Popularity               Popularity
	LayoutSpecific           bool
}

// OptionGroup is a group of related options.
type OptionGroup struct {
	Name, Description string
	Popularity        Popularity
	AllowsMultiple    bool
	Options           []Option
	seen              map[string]bool
}

type layoutKey struct{ name, variant string }

// Context reads rulesets. Parse fills it once.
type Context struct {
	xkb    *xkb.Context
	exotic bool
	state  contextState

	models      []Model
	layouts     []*Layout
	groups      []*OptionGroup
	seenModel   map[string]bool
	seenLayout  map[layoutKey]*Layout
	seenGroup   map[string]*OptionGroup
	publicGroup []OptionGroup
}

type contextState uint8

const (
	stateNew contextState = iota
	stateParsed
	stateFailed
)

// ErrNoIncludes reports a context none of whose default include paths exists.
var ErrNoIncludes = errors.New("registry: no valid include path")

// NewContext makes a context. RXKB_LOG_LEVEL sets its log level.
func NewContext(getenv func(string) string, flags Flags) (*Context, error) {
	xf := xkb.ContextNoDefaultIncludes
	if flags&NoSecureGetenv != 0 {
		xf |= xkb.ContextNoSecureGetenv
	}
	c := &Context{
		xkb:        xkb.NewContextFlags(getenv, xf),
		exotic:     flags&LoadExoticRules != 0,
		seenModel:  map[string]bool{},
		seenLayout: map[layoutKey]*Layout{},
		seenGroup:  map[string]*OptionGroup{},
	}
	if env := getenv("RXKB_LOG_LEVEL"); env != "" {
		c.xkb.SetLogLevel(xkb.ParseLogLevel(env))
	}
	if flags&NoDefaultIncludes == 0 && !c.AppendDefaultIncludePaths() {
		return nil, ErrNoIncludes
	}
	return c, nil
}

// SetLogFunc routes log messages to fn; nil restores the default, stderr.
func (c *Context) SetLogFunc(fn xkb.LogFunc) { c.xkb.SetLogFunc(fn) }

// SetLogLevel sets the least severe level logged.
func (c *Context) SetLogLevel(level xkb.LogLevel) { c.xkb.SetLogLevel(level) }

// LogLevel is the least severe level logged.
func (c *Context) LogLevel() xkb.LogLevel { return c.xkb.LogLevel() }

// SetUserData keeps a value on the context.
func (c *Context) SetUserData(v any) { c.xkb.SetUserData(v) }

// UserData is the value SetUserData kept.
func (c *Context) UserData() any { return c.xkb.UserData() }

// AppendIncludePath adds a directory to the search path, false once parsed
// or when it is not a readable directory.
func (c *Context) AppendIncludePath(path string) bool {
	if c.state != stateNew {
		c.xkb.Logf(xkb.LogError, "include paths can only be appended to a new context\n")
		return false
	}
	return c.xkb.AppendIncludePath(path)
}

// AppendDefaultIncludePaths adds the default search path, false once parsed
// or when none of it exists.
func (c *Context) AppendDefaultIncludePaths() bool {
	if c.state != stateNew {
		c.xkb.Logf(xkb.LogError, "include paths can only be appended to a new context\n")
		return false
	}
	return c.xkb.AppendDefaultIncludePaths()
}

// ParseDefaultRuleset is Parse of the evdev ruleset.
func (c *Context) ParseDefaultRuleset() error { return c.Parse("evdev") }

// Parse reads a ruleset's files from every include path, earlier paths
// winning, and may be called once.
func (c *Context) Parse(ruleset string) error {
	if c.state != stateNew {
		c.xkb.Logf(xkb.LogError, "parse must only be called on a new context\n")
		return errors.New("registry: parse must only be called on a new context")
	}
	paths := c.xkb.IncludePaths()
	var errs []error
	success := false
	for _, path := range slices.Backward(paths) {
		for _, f := range []struct {
			name string
			pop  Popularity
			want bool
		}{{ruleset + ".xml", Standard, true}, {ruleset + ".extras.xml", Exotic, c.exotic}} {
			if !f.want {
				continue
			}
			err := c.parseFile(filepath.Join(path, "rules", f.name), f.pop)
			switch {
			case err == nil:
				success = true
			case !errors.Is(err, os.ErrNotExist):
				c.xkb.Logf(xkb.LogError, "%v\n", err)
				errs = append(errs, err)
			}
		}
	}
	if !success {
		c.state = stateFailed
		return fmt.Errorf("registry: parse ruleset %s: %w", ruleset, errors.Join(append(errs, errors.New("no readable file"))...))
	}
	c.state = stateParsed
	c.publicGroup = make([]OptionGroup, len(c.groups))
	for i, g := range c.groups {
		c.publicGroup[i] = *g
		c.publicGroup[i].seen = nil
	}
	return nil
}

// Models is every model, in the order first read.
func (c *Context) Models() []Model { return c.models }

// Layouts is every layout and variant, in the order first read.
func (c *Context) Layouts() []Layout {
	out := make([]Layout, len(c.layouts))
	for i, l := range c.layouts {
		out[i] = *l
	}
	return out
}

// OptionGroups is every option group, in the order first read.
func (c *Context) OptionGroups() []OptionGroup { return c.publicGroup }

type xmlConfigItem struct {
	Popularity     string   `xml:"popularity,attr"`
	LayoutSpecific string   `xml:"layout-specific,attr"`
	Name           string   `xml:"name"`
	Description    string   `xml:"description"`
	Brief          string   `xml:"shortDescription"`
	Vendor         string   `xml:"vendor"`
	Languages      *xmlList `xml:"languageList"`
	Countries      *xmlList `xml:"countryList"`
}

type xmlList struct {
	ISO639  []string `xml:"iso639Id"`
	ISO3166 []string `xml:"iso3166Id"`
}

type xmlRegistry struct {
	XMLName xml.Name `xml:"xkbConfigRegistry"`
	Models  []struct {
		Item xmlConfigItem `xml:"configItem"`
	} `xml:"modelList>model"`
	Layouts []struct {
		Item     xmlConfigItem `xml:"configItem"`
		Variants []struct {
			Item xmlConfigItem `xml:"configItem"`
		} `xml:"variantList>variant"`
	} `xml:"layoutList>layout"`
	Groups []struct {
		Multiple string        `xml:"allowMultipleSelection,attr"`
		Item     xmlConfigItem `xml:"configItem"`
		Options  []struct {
			Item xmlConfigItem `xml:"configItem"`
		} `xml:"option"`
	} `xml:"optionList>group"`
}

func (c *Context) popularity(it *xmlConfigItem, def Popularity) Popularity {
	switch it.Popularity {
	case "":
		return def
	case "standard":
		return Standard
	case "exotic":
		return Exotic
	}
	c.xkb.Logf(xkb.LogError, "xml: invalid popularity attribute: expected 'standard' or 'exotic', got: '%s'\n", it.Popularity)
	return def
}

func (c *Context) parseFile(path string, pop Popularity) error {
	f, err := os.Open(path) //nolint:gosec // a ruleset file on the include path
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	defer func() { _ = f.Close() }()
	src, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("registry: read %s: %w", path, err)
	}
	if err := validate(bytes.NewReader(src)); err != nil {
		return fmt.Errorf("registry: XML error: failed to validate document at %s: %w", path, err)
	}
	return c.parseReader(path, bytes.NewReader(src), pop)
}

func (c *Context) parseReader(path string, r io.Reader, pop Popularity) error {
	var reg xmlRegistry
	if err := xml.NewDecoder(r).Decode(&reg); err != nil {
		return fmt.Errorf("registry: parse %s: %w", path, err)
	}
	for i := range reg.Models {
		it := &reg.Models[i].Item
		if !c.named(it) || c.seenModel[it.Name] {
			continue
		}
		c.seenModel[it.Name] = true
		c.models = append(c.models, Model{it.Name, it.Description, it.Vendor, c.popularity(it, pop)})
	}
	for i := range reg.Layouts {
		x := &reg.Layouts[i]
		if !c.named(&x.Item) {
			continue
		}
		l := c.layout(&x.Item, pop)
		for v := range x.Variants {
			c.variant(l, &x.Variants[v].Item, pop)
		}
	}
	for i := range reg.Groups {
		x := &reg.Groups[i]
		if !c.named(&x.Item) {
			continue
		}
		g, ok := c.seenGroup[x.Item.Name]
		if !ok {
			g = &OptionGroup{Name: x.Item.Name, Description: x.Item.Description, Popularity: c.popularity(&x.Item, pop), AllowsMultiple: x.Multiple == "true", seen: map[string]bool{}}
			c.seenGroup[x.Item.Name] = g
			c.groups = append(c.groups, g)
		}
		for o := range x.Options {
			it := &x.Options[o].Item
			if !c.named(it) || g.seen[it.Name] {
				continue
			}
			g.seen[it.Name] = true
			g.Options = append(g.Options, Option{Name: it.Name, Description: it.Description, Popularity: c.popularity(it, pop), LayoutSpecific: it.LayoutSpecific == "true"})
		}
	}
	return nil
}

func (c *Context) named(it *xmlConfigItem) bool {
	if it.Name != "" {
		return true
	}
	c.xkb.Logf(xkb.LogError, "xml: missing required element 'name'\n")
	return false
}

func (c *Context) layout(it *xmlConfigItem, pop Popularity) *Layout {
	key := layoutKey{name: it.Name}
	if l, ok := c.seenLayout[key]; ok {
		return l
	}
	l := &Layout{Name: it.Name, Description: it.Description, Brief: it.Brief, Popularity: c.popularity(it, pop)}
	if it.Languages != nil {
		l.ISO639 = it.Languages.ISO639
	}
	if it.Countries != nil {
		l.ISO3166 = it.Countries.ISO3166
	}
	c.seenLayout[key] = l
	c.layouts = append(c.layouts, l)
	return l
}

func (c *Context) variant(l *Layout, it *xmlConfigItem, pop Popularity) {
	key := layoutKey{l.Name, it.Name}
	if _, ok := c.seenLayout[key]; ok {
		return
	}
	v := &Layout{Name: l.Name, Variant: it.Name, Description: it.Description, Brief: it.Brief, Popularity: c.popularity(it, pop), ISO639: l.ISO639, ISO3166: l.ISO3166}
	if v.Brief == "" {
		v.Brief = l.Brief
	}
	if it.Languages != nil {
		v.ISO639 = it.Languages.ISO639
	}
	if it.Countries != nil {
		v.ISO3166 = it.Countries.ISO3166
	}
	c.seenLayout[key] = v
	c.layouts = append(c.layouts, v)
}
