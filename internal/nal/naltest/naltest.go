// Package naltest reads H.264 and H.265 headers back through ffmpeg's
// trace_headers, for tests of what writes them.
package naltest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// Trace runs ffmpeg's trace_headers over an Annex-B stream of
// codec ("h264" or "hevc") and returns each field's last value.
func Trace(t *testing.T, codec string, stream []byte) map[string]int {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to read the headers back")
	}
	path := filepath.Join(t.TempDir(), "headers")
	if err := os.WriteFile(path, stream, 0o600); err != nil {
		t.Fatal(err)
	}
	// A stream of parameter sets alone has no picture, which ffmpeg
	// reports as a failure after tracing the headers: what matters is
	// that every header parsed.
	out, _ := exec.Command(ffmpeg, //nolint:gosec // ffmpeg is the one PATH names; the rest are fixed arguments and a temp file
		"-hide_banner", "-f", codec, "-i", path, "-c", "copy", "-bsf:v", "trace_headers", "-f", "null", "-").CombinedOutput()
	if bad := regexp.MustCompile(`(?i)(failed to read|out of range|invalid data|error reading)`).Find(out); bad != nil {
		t.Fatalf("ffmpeg could not parse the headers (%s):\n%s", bad, out)
	}
	fields := map[string]int{}
	re := regexp.MustCompile(`\]\s+\d+\s+(\w+(?:\[\d+\])*)\s+[01]+ = (-?\d+)`)
	for _, m := range re.FindAllSubmatch(out, -1) {
		v, _ := strconv.Atoi(string(m[2]))
		fields[string(m[1])] = v
	}
	if len(fields) == 0 {
		t.Fatalf("ffmpeg traced no header fields:\n%s", out)
	}
	return fields
}

// Want checks traced fields against expected values.
func Want(t *testing.T, fields map[string]int, expect map[string]int) {
	t.Helper()
	for k, v := range expect {
		if got, ok := fields[k]; !ok || got != v {
			t.Errorf("%s = %d (present %v), want %d", k, got, ok, v)
		}
	}
}
