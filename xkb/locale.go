package xkb

import (
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/stubbedev/lys-lib/glibclocale"
)

type localeRule uint8

const (
	installedLocales localeRule = iota
	everyLocale
)

const selfExe = "/proc/self/exe"

var processLibc struct {
	once sync.Once
	rule localeRule
	err  error
}

func processLocaleRule() (localeRule, error) {
	processLibc.once.Do(func() {
		var interp string
		interp, processLibc.err = programInterpreter(selfExe)
		processLibc.rule = localeRuleOf(interp)
	})
	return processLibc.rule, processLibc.err
}

func localeRuleOf(interpreter string) localeRule {
	if strings.HasPrefix(filepath.Base(interpreter), "ld-musl-") {
		return everyLocale
	}
	return installedLocales
}

func programInterpreter(path string) (interp string, err error) {
	f, err := elf.Open(path)
	if err != nil {
		return "", fmt.Errorf("xkb: read the program interpreter of %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		raw, err := io.ReadAll(p.Open())
		if err != nil {
			return "", fmt.Errorf("xkb: read the program interpreter of %s: %w", path, err)
		}
		return strings.TrimRight(string(raw), "\x00"), nil
	}
	return "", nil
}

func localeInstalled(rule localeRule, getenv func(string) string, locale string) (bool, error) {
	if rule == everyLocale {
		return true, nil
	}
	return glibclocale.System(getenv).Installed(locale)
}

func (c *Context) localeInstalled(locale string) (bool, error) {
	rule, err := processLocaleRule()
	if err != nil {
		return false, err
	}
	return localeInstalled(rule, c.getenv, locale)
}
