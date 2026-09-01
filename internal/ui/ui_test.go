package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/mnorrsken/pave/internal/config"
	"github.com/mnorrsken/pave/internal/run"
	"github.com/mnorrsken/pave/internal/sshcert"
)

func TestScanFillsTheTree(t *testing.T) {
	h := newHarness(t)

	var count int
	var names []string
	h.inspect(func() {
		count = h.app.tree.playbookCount()
		names = h.app.tree.sortedProjectNames()
	})
	if count != 3 {
		t.Errorf("playbooks = %d, want 3", count)
	}
	if strings.Join(names, ",") != "base,cluster" {
		t.Errorf("projects = %v", names)
	}
	if !strings.Contains(h.status(), "3 playbooks") {
		t.Errorf("status = %q", h.status())
	}
}

// The tree starts on the first playbook, so the detail pane and the command
// preview say something before anything is touched.
func TestDetailAndPreviewFollowTheSelection(t *testing.T) {
	h := newHarness(t)

	var detail, preview string
	h.inspect(func() {
		detail = h.app.detail.GetText(true)
		preview = h.app.preview.GetText(true)
	})
	if !strings.Contains(detail, "Onboard a host onto the SSH user CA.") {
		t.Errorf("detail = %q, want the playbook's own comment", detail)
	}
	if !strings.Contains(preview, "ansible-playbook playbooks/onboard.yml") {
		t.Errorf("preview = %q", preview)
	}

	h.key(tcell.KeyDown)
	h.waitFor("the selection to move", func() bool {
		return strings.Contains(h.app.detail.GetText(true), "Patch every host.")
	})
	h.inspect(func() { preview = h.app.preview.GetText(true) })
	if !strings.Contains(preview, "playbooks/patch.yml") {
		t.Errorf("preview = %q", preview)
	}
}

func TestRunUsesTheFormOptions(t *testing.T) {
	h := newHarness(t)

	// Enter moves from the tree into the form, where space ticks check mode.
	h.key(tcell.KeyEnter)
	h.waitFor("the form to take focus", func() bool { return h.app.form.HasFocus() })
	h.press(' ')
	h.waitFor("check mode to be on", func() bool { return h.app.form.checkbox(fieldCheck).IsChecked() })

	h.inspect(func() {
		h.app.form.checkbox(fieldDiff).SetChecked(true)
		h.app.form.setLimit("kubeworkers")
	})

	h.key(tcell.KeyF5)
	h.waitFor("the run to start", func() bool { return h.runner.count() == 1 })

	cmd := h.runner.lastCmd()
	if got, want := strings.Join(cmd.Args, " "), "playbooks/onboard.yml --check --diff --limit kubeworkers"; got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	if !strings.HasSuffix(cmd.Dir, filepath.Join("testdata", "tree", "base")) {
		t.Errorf("dir = %q, want the project directory", cmd.Dir)
	}
	if !hasEnvSuffix(cmd.Env, "ANSIBLE_CONFIG=", filepath.Join("base", "ansible.cfg")) {
		t.Errorf("env has no ANSIBLE_CONFIG for the project: %v", tail(cmd.Env, 3))
	}
	h.runner.session(t, 0).finish(nil)
}

func TestOutputStreamsAndTypingReachesAnsible(t *testing.T) {
	h := newHarness(t)
	h.key(tcell.KeyF5)
	h.waitFor("the run to start", func() bool { return h.runner.count() == 1 })
	sess := h.runner.session(t, 0)

	sess.emit("PLAY [all] ****\nBECOME password: ")
	h.waitFor("the output to arrive", func() bool {
		// The brackets must survive: a TextView with dynamic colours would
		// otherwise eat "[all]" as a colour tag.
		return strings.Contains(h.app.output.text(), "PLAY [all] ****")
	})

	// While a run is going the keyboard belongs to the process.
	h.press('y')
	h.key(tcell.KeyEnter)
	h.waitFor("the answer to reach the process", func() bool { return sess.typed() == "y\r" })

	sess.finish(nil)
	h.waitFor("the run to finish", func() bool { return !h.app.running })
	if h.status() != "done" {
		t.Errorf("status = %q, want done", h.status())
	}
	if !strings.Contains(h.app.output.text(), "ok") {
		t.Errorf("output = %q, want pave's own ok line", h.app.output.text())
	}
}

func TestFailedRunSaysSo(t *testing.T) {
	h := newHarness(t)
	h.key(tcell.KeyF5)
	h.waitFor("the run to start", func() bool { return h.runner.count() == 1 })

	h.runner.session(t, 0).finish(errors.New("boom"))
	h.waitFor("the failure to be reported", func() bool {
		return !h.app.running && strings.Contains(h.app.status.text(), "exited")
	})
}

func TestCtrlCInterruptsThenKills(t *testing.T) {
	h := newHarness(t)
	h.key(tcell.KeyF5)
	h.waitFor("the run to start", func() bool { return h.runner.count() == 1 })
	sess := h.runner.session(t, 0)

	h.key(tcell.KeyCtrlC)
	h.waitFor("the interrupt", func() bool { return sess.interruptCount() == 1 })
	if !strings.Contains(h.app.output.text(), "^C again to kill") {
		t.Errorf("output = %q", h.app.output.text())
	}

	h.key(tcell.KeyCtrlC)
	h.waitFor("the run to end", func() bool { return !h.app.running })
	if h.status() != "interrupted" {
		t.Errorf("status = %q", h.status())
	}
}

func TestHostPickerFillsTheLimit(t *testing.T) {
	h := newHarness(t)

	h.press('L')
	h.waitFor("the picker", func() bool { return h.app.modalOpen() })

	h.press(' ')          // kubemasters
	h.key(tcell.KeyDown)  //
	h.press(' ')          // kubeworkers
	h.key(tcell.KeyEnter) // accept
	h.waitFor("the limit to be filled in", func() bool {
		return h.app.form.limit() == "kubemasters"+limitSeparator+"kubeworkers"
	})
	if h.app.modalOpen() {
		t.Error("the picker should have closed")
	}

	// Reopening it starts from what is already in the limit.
	h.press('L')
	h.waitFor("the picker again", func() bool { return h.app.modalOpen() })
	h.press(' ') // clears kubemasters
	h.key(tcell.KeyEnter)
	h.waitFor("the limit to shrink", func() bool { return h.app.form.limit() == "kubeworkers" })
}

func TestInventoryFailureIsReported(t *testing.T) {
	h := newHarness(t)
	h.invErr = errors.New("no age key")

	h.press('L')
	h.waitFor("the error", func() bool { return strings.Contains(h.app.status.text(), "no age key") })
}

// The passwords go to files that exist only while the run does.
func TestCredentialsBecomePasswordFiles(t *testing.T) {
	h := newHarness(t)
	h.inspect(func() {
		h.app.form.setCredentials(credentials{User: "pi", Password: "s3cret", Become: "sud0", Target: "10.0.0.9"})
	})

	h.key(tcell.KeyF5)
	h.waitFor("the run to start", func() bool { return h.runner.count() == 1 })

	args := h.runner.lastCmd().Args
	connFile := valueAfter(args, "--connection-password-file")
	becomeFile := valueAfter(args, "--become-password-file")
	if connFile == "" || becomeFile == "" {
		t.Fatalf("args = %q, want both password files", args)
	}
	if !strings.Contains(strings.Join(args, " "), "--inventory 10.0.0.9,") {
		t.Errorf("args = %q, want the ad-hoc target as its own inventory", args)
	}
	if strings.Contains(strings.Join(args, " "), "s3cret") {
		t.Fatal("a password must never be on the command line")
	}

	b, err := os.ReadFile(connFile)
	if err != nil {
		t.Fatalf("read the password file: %v", err)
	}
	if string(b) != "s3cret\n" {
		t.Errorf("password file = %q", b)
	}
	if fi, err := os.Stat(connFile); err == nil && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}

	h.runner.session(t, 0).finish(nil)
	h.waitFor("the run to finish", func() bool { return !h.app.running })

	for _, f := range []string{connFile, becomeFile} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("%s is still there after the run", f)
		}
	}
}

func TestFilterNarrowsTheTree(t *testing.T) {
	h := newHarness(t)

	h.press('/')
	h.waitFor("the filter bar", func() bool { return h.app.filtering })
	h.typeText("patch")
	h.waitFor("the tree to narrow", func() bool { return h.app.tree.playbookCount() == 1 })

	h.key(tcell.KeyEscape)
	h.waitFor("the filter to clear", func() bool {
		return !h.app.filtering && h.app.tree.playbookCount() == 3
	})
}

func TestCertificateStateInTheHeader(t *testing.T) {
	h := newHarness(t)
	var header string
	h.inspect(func() { header = h.app.header.GetText(true) })
	if !strings.Contains(header, "cert 12h00m") || !strings.Contains(header, "ansible-admin") {
		t.Errorf("header = %q", header)
	}

	h.inspect(func() {
		h.app.cert = sshcert.Status{Present: true, ValidTo: time.Date(2026, 9, 1, 20, 0, 0, 0, time.Local)}
		h.app.renderHeader()
		header = h.app.header.GetText(true)
	})
	if !strings.Contains(header, "expired") {
		t.Errorf("header = %q, want it to say the certificate expired", header)
	}

	h.inspect(func() {
		h.app.cert = sshcert.Status{}
		h.app.renderHeader()
		header = h.app.header.GetText(true)
	})
	if !strings.Contains(header, "no certificate") {
		t.Errorf("header = %q", header)
	}
}

// Signing goes through the same output pane as a run, so an encrypted CA key
// can ask for its passphrase.
func TestSigningRunsSshKeygen(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "user_ca")
	key := filepath.Join(dir, "id_ed25519")
	os.WriteFile(ca, []byte("ca"), 0o600)
	os.WriteFile(key+".pub", []byte("ssh-ed25519 AAAA test"), 0o644)

	h := newHarness(t, func(o *Options) {
		o.Config.SSHCert.CAKey = ca
		o.Config.SSHCert.Key = key
		o.Config.SSHCert.Principals = []string{"ansible-admin"}
		o.Config.SSHCert.Validity = "+2h"
	})

	h.press('c')
	h.waitFor("the signing form", func() bool { return h.app.modalOpen() })

	// Six fields, then the sign button.
	for i := 0; i < 6; i++ {
		h.key(tcell.KeyTab)
	}
	h.key(tcell.KeyEnter)
	h.waitFor("ssh-keygen to be started", func() bool { return h.runner.count() > 0 })

	cmd := h.runner.lastCmd()
	if filepath.Base(cmd.Path) != "ssh-keygen" {
		t.Fatalf("ran %q", cmd.Path)
	}
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"-s " + ca, "-V +2h", "-n ansible-admin", key + ".pub"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	h.runner.session(t, 0).finish(nil)
}

func TestQuitDuringARunAsksFirst(t *testing.T) {
	h := newHarness(t)
	h.key(tcell.KeyF5)
	h.waitFor("the run to start", func() bool { return h.runner.count() == 1 })

	// While the output pane has the keyboard everything typed goes to the
	// process, so leave it first. The run keeps going.
	h.key(tcell.KeyEscape)
	h.waitFor("the tree", func() bool { return h.app.focusedOn(h.app.tree) })
	h.press('q')
	h.waitFor("the confirmation", func() bool { return h.app.modalOpen() })

	h.key(tcell.KeyEscape)
	h.sync()
	h.runner.session(t, 0).finish(nil)
	h.waitFor("the run to finish", func() bool { return !h.app.running })
}

func TestSaveLogWritesTheOutput(t *testing.T) {
	h := newHarness(t)
	h.key(tcell.KeyF5)
	h.waitFor("the run to start", func() bool { return h.runner.count() == 1 })

	sess := h.runner.session(t, 0)
	sess.emit("PLAY RECAP ****\n")
	sess.finish(nil)
	h.waitFor("the run to finish", func() bool { return !h.app.running })

	path := filepath.Join(t.TempDir(), "run.log")
	h.press('s')
	h.waitFor("the save prompt", func() bool { return h.app.modalOpen() })
	h.key(tcell.KeyCtrlU) // clear the suggested file name
	h.typeText(path)
	h.key(tcell.KeyEnter)
	h.waitFor("the file to be written", func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "PLAY RECAP") {
		t.Errorf("log = %q", b)
	}
}

// Onboarding is the ordinary run form with the configured playbook selected
// and the credentials dialog open.
func TestOnboardSelectsTheConfiguredPlaybook(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.Config.OnboardPlaybook = "base/playbooks/onboard.yml"
	})

	// Move off it first, so selecting it again is visible.
	h.key(tcell.KeyDown)
	h.waitFor("another playbook", func() bool {
		return h.app.tree.currentPlaybook() != nil &&
			strings.Contains(h.app.tree.currentPlaybook().Rel, "patch")
	})

	h.press('o')
	h.waitFor("onboard to select its playbook", func() bool {
		pb := h.app.tree.currentPlaybook()
		return pb != nil && strings.Contains(pb.Rel, "onboard") && h.app.modalOpen()
	})
}

// The default config must not make pave look somewhere odd for its key.
func TestDefaultKeyPathIsExpanded(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(config.Expand(cfg.SSHCert.Key)) {
		t.Errorf("key = %q, want an absolute path", config.Expand(cfg.SSHCert.Key))
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	if _, err := sshcert.Read(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Errorf("a missing certificate is not an error: %v", err)
	}
}

func valueAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasEnvSuffix(env []string, prefix, suffix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) && strings.HasSuffix(e, suffix) {
			return true
		}
	}
	return false
}

func tail(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// The real runner, end to end: a pty, ANSI colours and brackets, output in
// the pane. No ansible needed — a script that prints what ansible prints.
func TestRealRunnerStreamsColouredOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ansible-playbook")
	body := "#!/bin/sh\n" +
		"printf '\\033[0;32mPLAY [all] ***\\033[0m\\n'\n" +
		"printf 'ok: [worker1]\\n'\n" +
		"if [ -t 1 ]; then printf 'tty=yes\\n'; else printf 'tty=no\\n'; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, func(o *Options) {
		o.Runner = run.PTYRunner{}
		o.Config.AnsiblePlaybookBin = script
	})

	h.key(tcell.KeyF5)
	h.waitFor("the run to start", func() bool { return h.app.running })
	h.waitFor("the run to finish", func() bool { return !h.app.running })

	out := h.app.output.text()
	for _, want := range []string{"PLAY [all] ***", "ok: [worker1]", "tty=yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q is missing %q", out, want)
		}
	}
	if h.status() != "done" {
		t.Errorf("status = %q", h.status())
	}
}

// Integration: a real ansible-playbook, syntax-checking a real playbook with
// the project's own ansible.cfg.
func TestRealAnsibleSyntaxCheck(t *testing.T) {
	if os.Getenv("PAVE_IT") != "1" {
		t.Skip("set PAVE_IT=1 to run against a real ansible")
	}
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("no ansible-playbook on PATH")
	}

	h := newHarness(t, func(o *Options) { o.Runner = run.PTYRunner{} })
	h.inspect(func() { h.app.form.input(fieldExtraArgs).SetText("--syntax-check") })

	h.key(tcell.KeyF5)
	h.waitFor("the syntax check to start", func() bool { return h.app.running })
	// ansible is python: on a cold runner it takes seconds to get going.
	h.waitWithin(2*time.Minute, "the syntax check to finish", func() bool { return !h.app.running })
	if h.status() != "done" {
		t.Errorf("status = %q, output:\n%s", h.status(), h.app.output.text())
	}
}

// tcell gives backspace and ^H the same key code, so a binding on ^H would
// eat every backspace typed into the form.
func TestBackspaceEditsTheFormRatherThanOpeningAPicker(t *testing.T) {
	h := newHarness(t)
	h.key(tcell.KeyEnter)
	h.waitFor("the form", func() bool { return h.app.form.HasFocus() })

	for i := 0; i < fieldLimit; i++ {
		h.key(tcell.KeyTab)
	}
	h.waitFor("the limit field", func() bool { return h.app.form.input(fieldLimit).HasFocus() })

	h.typeText("kubeworkers")
	h.waitFor("the text", func() bool { return h.app.form.limit() == "kubeworkers" })
	h.key(tcell.KeyBackspace2)
	h.waitFor("the last character to go", func() bool { return h.app.form.limit() == "kubeworker" })
	if h.app.modalOpen() {
		t.Error("backspace opened a dialog")
	}

	// F2 is the one that opens the picker from inside the form.
	h.key(tcell.KeyF2)
	h.waitFor("the picker", func() bool { return h.app.modalOpen() })
}

// Everything the form offers has to be on the screen. tview draws a form's
// buttons below its items, outside the height a naive count would give it,
// and an unchecked checkbox is a single space unless it is told otherwise —
// both have already left the pane looking empty.
func TestTheWholeFormIsDrawn(t *testing.T) {
	h := newHarness(t)
	h.sync()
	screen := h.screenText()

	for _, want := range []string{"check mode", "extra args", "run", "hosts…", "credentials…", markOff} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen does not show %q:\n%s", want, screen)
		}
	}
	// The tree markers are drawn through tview's tag parser, so they must not
	// look like colour tags either.
	if !strings.Contains(screen, iconProject+" base") {
		t.Errorf("the project marker is missing:\n%s", screen)
	}
}

func TestPickerShowsWhatIsSelected(t *testing.T) {
	h := newHarness(t)
	h.press('L')
	h.waitFor("the picker", func() bool { return h.app.modalOpen() })
	h.press(' ')
	h.waitFor("the mark to be drawn", func() bool {
		return strings.Contains(h.screenText(), markOn+" kubemasters")
	})
}
