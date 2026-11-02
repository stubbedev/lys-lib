package xcursor

import (
	"os"
	"testing"
)

func benchTheme(b *testing.B) *Theme {
	b.Helper()
	path := os.Getenv("LYS_TEST_XCURSOR_PATH")
	if path == "" {
		b.Skip("LYS_TEST_XCURSOR_PATH is not set")
	}
	theme, err := Open("Vanilla-DMZ", []string{path})
	if err != nil {
		b.Fatal(err)
	}
	return theme
}

func BenchmarkThemeLoad(b *testing.B) {
	theme := benchTheme(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := theme.Load(24, "left_ptr"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkThemeFrames(b *testing.B) {
	theme := benchTheme(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := theme.Frames(24, "watch"); err != nil {
			b.Fatal(err)
		}
	}
}
