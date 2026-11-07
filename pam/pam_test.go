package pam

import (
	"os"
	"path/filepath"
	"testing"
)

func service(t *testing.T, rules string) string {
	t.Helper()
	l, err := loadLibpam()
	if err != nil {
		t.Skipf("no libpam: %v", err)
	}
	if l.startConfdir == nil {
		t.Skip("libpam lacks pam_start_confdir")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lys-lib-test"), []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAPasswordStackTalksThroughTheConversation(t *testing.T) {
	dir := service(t, "password optional pam_echo.so lys-lib-probe\npassword required pam_permit.so\n")
	var seen []Message
	txn, err := Start("lys-lib-test", "nobody", dir, func(ms []Message) ([]Reply, int32) {
		seen = append(seen, ms...)
		return make([]Reply, len(ms)), Success
	})
	if err != nil {
		t.Fatal(err)
	}
	rc := txn.Chauthtok(0)
	txn.End(rc)
	if rc != Success {
		t.Fatalf("pam_chauthtok: %d", rc)
	}
	if len(seen) == 0 || seen[0].Style != TextInfo || seen[0].Text != "lys-lib-probe" {
		t.Fatalf("messages %+v, want pam_echo's text as info", seen)
	}
}

func TestADeniedStackFails(t *testing.T) {
	dir := service(t, "password required pam_deny.so\n")
	txn, err := Start("lys-lib-test", "nobody", dir, func(ms []Message) ([]Reply, int32) { return make([]Reply, len(ms)), Success })
	if err != nil {
		t.Fatal(err)
	}
	rc := txn.Chauthtok(0)
	txn.End(rc)
	if rc == Success {
		t.Fatal("pam_deny changed the password")
	}
}
