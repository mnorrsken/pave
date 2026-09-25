package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A read can end anywhere, in the middle of a colour code or between the \r
// and the \n of a line break.
func TestRunLogStripsAcrossReads(t *testing.T) {
	dir := t.TempDir()
	when := time.Date(2026, 9, 25, 14, 30, 12, 0, time.Local)
	l, err := openRunLog(dir, "playbooks/base.yml", when)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"\x1b[0;3", "2mchanged\x1b[0m\r", "\nhalf", "", "\x1b]0;title\x07 way"} {
		l.Write([]byte(s))
	}
	l.line("ok")
	l.Close()

	b, err := os.ReadFile(filepath.Join(dir, "base-20260925-143012.log"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "changed\nhalf way\n:: ok\n"; string(b) != want {
		t.Errorf("log = %q, want %q", b, want)
	}

	// The same playbook again in the same second gets a name of its own.
	l2, err := openRunLog(dir, "playbooks/base.yml", when)
	if err != nil {
		t.Fatal(err)
	}
	l2.Close()
	if got := filepath.Base(l2.path); got != "base-20260925-143012-2.log" {
		t.Errorf("second log = %s", got)
	}
}
