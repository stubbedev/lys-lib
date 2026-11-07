package pwtest

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"testing"
)

// Serial is the object.serial the server's registry lists for global
// id, as pw-dump reads it.
func Serial(t testing.TB, id uint32) uint64 {
	t.Helper()
	out, err := exec.Command("pw-dump", strconv.FormatUint(uint64(id), 10)).Output() //nolint:gosec // pw-dump from PATH with a numeric id
	if err != nil {
		t.Fatalf("pw-dump %d: %v", id, err)
	}
	var objects []struct {
		ID   uint32 `json:"id"`
		Info struct {
			Props map[string]any `json:"props"`
		} `json:"info"`
	}
	if err := json.Unmarshal(out, &objects); err != nil {
		t.Fatalf("pw-dump %d: %v: %s", id, err, out)
	}
	for _, o := range objects {
		if o.ID != id {
			continue
		}
		serial, ok := o.Info.Props["object.serial"].(float64)
		if !ok {
			t.Fatalf("pw-dump %d: no numeric object.serial in %v", id, o.Info.Props)
		}
		return uint64(serial)
	}
	t.Fatalf("pw-dump %d lists no object %d: %s", id, id, out)
	return 0
}
