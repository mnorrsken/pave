package sshcert

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mnorrsken/pave/internal/run"
)

const sample = `/Users/martin/.ssh/id_ed25519-cert.pub:
        Type: ssh-ed25519-cert-v01@openssh.com user certificate
        Public key: ED25519-CERT SHA256:abc
        Signing CA: ED25519 SHA256:def (using ssh-ed25519)
        Key ID: "martin@laptop"
        Serial: 1756759000
        Valid: from 2026-09-01T21:00:00 to 2026-09-02T09:00:00
        Principals: 
                ansible-admin
                pi
                root
        Critical Options: (none)
        Extensions: 
                permit-pty
                permit-user-rc
`

func TestParseStatus(t *testing.T) {
	st := ParseStatus(sample)
	if st.KeyID != "martin@laptop" {
		t.Errorf("key id = %q", st.KeyID)
	}
	if got, want := strings.Join(st.Principals, ","), "ansible-admin,pi,root"; got != want {
		// The extension names below must not leak into the principal list.
		t.Errorf("principals = %q, want %q", got, want)
	}
	want := time.Date(2026, 9, 2, 9, 0, 0, 0, time.Local)
	if !st.ValidTo.Equal(want) {
		t.Errorf("valid to = %v, want %v", st.ValidTo, want)
	}
	if st.Forever {
		t.Error("should not be forever")
	}
}

func TestParseStatusForever(t *testing.T) {
	st := ParseStatus("        Valid: forever\n")
	if !st.Forever {
		t.Error("want forever")
	}
	st.Present = true
	if st.Expired(time.Now()) || st.Remaining(time.Now()) != 0 {
		t.Error("a forever certificate never expires and has no remaining time")
	}
}

func TestRemainingAndExpired(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.Local)
	st := Status{Present: true, ValidTo: now.Add(90 * time.Minute)}
	if got := st.Remaining(now); got != 90*time.Minute {
		t.Errorf("remaining = %v", got)
	}
	if st.Expired(now) {
		t.Error("not expired yet")
	}

	past := Status{Present: true, ValidTo: now.Add(-time.Minute)}
	if !past.Expired(now) || past.Remaining(now) != 0 {
		t.Error("want expired with nothing remaining")
	}

	// No certificate at all is not the same as an expired one.
	if (Status{}).Expired(now) {
		t.Error("a missing certificate is not expired")
	}
}

func TestStepsChecksInputs(t *testing.T) {
	dir := t.TempDir()
	if _, err := (Request{Key: filepath.Join(dir, "k")}).Steps(nil); err == nil {
		t.Error("want an error without a CA key")
	}
	ca := filepath.Join(dir, "ca")
	os.WriteFile(ca, []byte("x"), 0o600)
	if _, err := (Request{CAKey: ca, Key: filepath.Join(dir, "k")}).Steps(nil); err == nil {
		t.Error("want an error when the public key is missing")
	}
}

func TestStepsArgs(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca")
	key := filepath.Join(dir, "id")
	os.WriteFile(ca, []byte("x"), 0o600)
	os.WriteFile(key+".pub", []byte("ssh-ed25519 AAAA test"), 0o644)

	steps, err := Request{
		CAKey: ca, Key: key, Validity: "+8h", Identity: "martin@laptop",
		Principals: []string{"ansible-admin", "root"}, AddToAgent: true,
	}.Steps(nil)
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("got %d steps, want sign + two agent steps", len(steps))
	}
	args := strings.Join(steps[0].Cmd.Args, " ")
	for _, want := range []string{"-s " + ca, "-I martin@laptop", "-V +8h", "-n ansible-admin,root", key + ".pub"} {
		if !strings.Contains(args, want) {
			t.Errorf("sign args %q missing %q", args, want)
		}
	}
	if !strings.Contains(args, "-z ") {
		t.Errorf("sign args %q have no serial", args)
	}
	if !steps[1].AllowFail {
		t.Error("dropping a key that was never in the agent must not stop the run")
	}
}

// End to end with a throwaway CA: sign, then read back what was signed.
func TestSignAndRead(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca")
	key := filepath.Join(dir, "id_ed25519")
	keygen(t, ca)
	keygen(t, key)

	// Nothing signed yet.
	st, err := Read(key)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if st.Present {
		t.Fatal("there is no certificate yet")
	}

	req := Request{CAKey: ca, Key: key, Validity: "+12h", Identity: "test@pave",
		Principals: []string{"ansible-admin", "pi"}}
	steps, err := req.Steps(os.Environ())
	if err != nil {
		t.Fatalf("steps: %v", err)
	}
	for _, s := range steps {
		if out, err := run.Output(s.Cmd.Path, s.Cmd.Args...); err != nil {
			t.Fatalf("%s: %v (%s)", s.Label, err, out)
		}
	}

	st, err = Read(key)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !st.Present || st.KeyID != "test@pave" {
		t.Fatalf("status = %+v", st)
	}
	if got, want := strings.Join(st.Principals, ","), "ansible-admin,pi"; got != want {
		t.Errorf("principals = %q, want %q", got, want)
	}
	if r := st.Remaining(time.Now()); r < 11*time.Hour || r > 12*time.Hour+time.Minute {
		t.Errorf("remaining = %v, want about 12h", r)
	}

	// Signing again keeps the certificate it replaces.
	if _, err := req.Steps(os.Environ()); err != nil {
		t.Fatalf("second steps: %v", err)
	}
	backups, _ := filepath.Glob(CertPath(key) + ".bak.*")
	if len(backups) != 1 {
		t.Errorf("backups = %v, want exactly one", backups)
	}
}

func keygen(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "pave-test", "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v (%s)", err, out)
	}
}
