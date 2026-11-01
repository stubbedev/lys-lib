package registry

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

type childRule struct {
	name     string
	min, max int
}

type elementRule struct {
	children []childRule
	text     bool
	attrs    map[string][]string
}

const unbounded = int(^uint(0) >> 1)

var boolValues = []string{"true", "false"}

var dtd = map[string]elementRule{
	"xkbConfigRegistry": {children: []childRule{{"modelList", 0, 1}, {"layoutList", 0, 1}, {"optionList", 0, 1}}, attrs: map[string][]string{"version": nil}},
	"modelList":         {children: []childRule{{"model", 0, unbounded}}},
	"model":             {children: []childRule{{"configItem", 1, 1}}},
	"layoutList":        {children: []childRule{{"layout", 0, unbounded}}},
	"layout":            {children: []childRule{{"configItem", 1, 1}, {"variantList", 0, 1}}},
	"optionList":        {children: []childRule{{"group", 0, unbounded}}},
	"variantList":       {children: []childRule{{"variant", 0, unbounded}}},
	"variant":           {children: []childRule{{"configItem", 1, 1}}},
	"group":             {children: []childRule{{"configItem", 1, 1}, {"option", 0, unbounded}}, attrs: map[string][]string{"allowMultipleSelection": boolValues}},
	"option":            {children: []childRule{{"configItem", 1, 1}}},
	"configItem": {children: []childRule{
		{"name", 1, 1}, {"shortDescription", 0, 1}, {"description", 0, 1}, {"vendor", 0, 1}, {"countryList", 0, 1}, {"languageList", 0, 1}, {"hwList", 0, 1},
	}, attrs: map[string][]string{"layout-specific": boolValues, "popularity": {"standard", "exotic"}}},
	"name":             {text: true},
	"shortDescription": {text: true},
	"description":      {text: true},
	"vendor":           {text: true},
	"countryList":      {children: []childRule{{"iso3166Id", 1, unbounded}}},
	"iso3166Id":        {text: true},
	"languageList":     {children: []childRule{{"iso639Id", 1, unbounded}}},
	"iso639Id":         {text: true},
	"hwList":           {children: []childRule{{"hwId", 1, unbounded}}},
	"hwId":             {text: true},
}

type openElement struct {
	name  string
	rule  elementRule
	index int
	count int
}

func (o *openElement) child(name string) error {
	for o.index < len(o.rule.children) {
		want := o.rule.children[o.index]
		if want.name == name {
			if o.count >= want.max {
				return fmt.Errorf("<%s> holds too many <%s>", o.name, name)
			}
			o.count++
			return nil
		}
		if o.count < want.min {
			return fmt.Errorf("<%s> needs <%s> before <%s>", o.name, want.name, name)
		}
		o.index++
		o.count = 0
	}
	return fmt.Errorf("<%s> cannot hold <%s>", o.name, name)
}

func (o *openElement) close() error {
	for i := o.index; i < len(o.rule.children); i++ {
		want := o.rule.children[i]
		have := 0
		if i == o.index {
			have = o.count
		}
		if have < want.min {
			return fmt.Errorf("<%s> needs <%s>", o.name, want.name)
		}
	}
	return nil
}

func checkAttrs(rule elementRule, name string, attrs []xml.Attr) error {
	for _, a := range attrs {
		if a.Name.Space != "" {
			continue
		}
		allowed, ok := rule.attrs[a.Name.Local]
		if !ok {
			return fmt.Errorf("<%s> has no attribute %s", name, a.Name.Local)
		}
		if allowed != nil && !containsString(allowed, a.Value) {
			return fmt.Errorf("<%s %s> cannot be %q", name, a.Name.Local, a.Value)
		}
	}
	return nil
}

func containsString(list []string, s string) bool {
	return slices.Contains(list, s)
}

// validate holds a document to the DTD libxkbregistry validates against.
func validate(r io.Reader) error {
	dec := xml.NewDecoder(r)
	var stack []openElement
	sawRoot := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			rule, ok := dtd[name]
			if !ok {
				return fmt.Errorf("unknown element <%s>", name)
			}
			if len(stack) == 0 {
				if sawRoot || name != "xkbConfigRegistry" {
					return fmt.Errorf("the root element is <%s>, not <xkbConfigRegistry>", name)
				}
				sawRoot = true
			} else if err := stack[len(stack)-1].child(name); err != nil {
				return err
			}
			if err := checkAttrs(rule, name, t.Attr); err != nil {
				return err
			}
			stack = append(stack, openElement{name: name, rule: rule})
		case xml.EndElement:
			top := &stack[len(stack)-1]
			if err := top.close(); err != nil {
				return err
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 && !stack[len(stack)-1].rule.text && strings.TrimSpace(string(t)) != "" {
				return fmt.Errorf("<%s> cannot hold text", stack[len(stack)-1].name)
			}
		}
	}
	if !sawRoot {
		return errors.New("no root element")
	}
	return nil
}
