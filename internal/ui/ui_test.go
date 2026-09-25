package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/mnorrsken/pave/internal/config"
	"github.com/mnorrsken/pave/internal/inv"
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

	// Enter opens the run options, on the run button, so most runs are one
	// more keystroke. Diff is on to start with, and space on the checkbox is
	// what would turn it off.
	h.key(tcell.KeyEnter)
	h.waitFor("the options dialog", func() bool { return h.app.optionsFront() && h.app.form.HasFocus() })
	if !h.app.form.checkbox(fieldDiff).IsChecked() {
		t.Error("diff should start on")
	}
	h.focusField(fieldDiff)
	h.press(' ')
	h.waitFor("diff to go off", func() bool { return !h.app.form.checkbox(fieldDiff).IsChecked() })
	h.press(' ')
	h.waitFor("diff to come back", func() bool { return h.app.form.checkbox(fieldDiff).IsChecked() })

	h.inspect(func() {
		h.app.form.checkbox(fieldVerbose).SetChecked(true)
		h.app.form.setLimit("kubeworkers")
	})

	// F5 asks the confirmation; its first button is a real run.
	h.key(tcell.KeyF5)
	h.waitFor("the confirmation", func() bool { return h.app.modalOpen() && !h.app.optionsFront() })
	h.key(tcell.KeyEnter)
	h.waitFor("the run", func() bool { return h.term.count() == 1 })

	cmd := h.term.lastCmd()
	if got, want := strings.Join(cmd.Args, " "), "playbooks/onboard.yml --diff -v --limit kubeworkers"; got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	if !strings.HasSuffix(cmd.Dir, filepath.Join("testdata", "tree", "base")) {
		t.Errorf("dir = %q, want the project directory", cmd.Dir)
	}
	if !hasEnvSuffix(cmd.Env, "ANSIBLE_CONFIG=", filepath.Join("base", "ansible.cfg")) {
		t.Errorf("env has no ANSIBLE_CONFIG for the project: %v", tail(cmd.Env, 3))
	}
}

// The options open on the run button, so a run that wants them as they are is
// one more keystroke and never a tab through the fields.
func TestOptionsOpenOnTheRunButton(t *testing.T) {
	h := newHarness(t)

	h.key(tcell.KeyF5)
	h.waitFor("the run button to have the keyboard", func() bool {
		return h.app.optionsFront() && h.app.form.runButton().HasFocus()
	})
	h.key(tcell.KeyEnter)
	h.waitFor("the confirmation", func() bool { return h.app.modalOpen() && !h.app.optionsFront() })
}

// Check mode is the confirmation's second button, so a dry run is one tab and
// an enter away and is never left ticked in the form afterwards.
func TestConfirmationChoosesCheckMode(t *testing.T) {
	h := newHarness(t)

	h.confirmRun()
	h.key(tcell.KeyTab)
	h.key(tcell.KeyEnter)
	h.waitFor("the run", func() bool { return h.term.count() == 1 })

	if got, want := strings.Join(h.term.lastCmd().Args, " "), "playbooks/onboard.yml --check --diff"; got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}

// Cancelling the confirmation runs nothing and leaves the options open.
func TestConfirmationCanBackOut(t *testing.T) {
	h := newHarness(t)

	h.confirmRun()
	h.key(tcell.KeyEscape)
	h.waitFor("the options to be back on top and have the keyboard", func() bool {
		return h.app.optionsFront() && h.app.form.HasFocus()
	})
	if h.term.count() != 0 {
		t.Errorf("%d runs started, want none", h.term.count())
	}
}

// A run has the terminal to itself: pave says what it runs and how it ended,
// and waits for a key before drawing the interface over the result.
func TestARunHappensOnTheTerminal(t *testing.T) {
	h := newHarness(t)
	h.runNow()
	h.waitFor("the run", func() bool { return h.term.pauseCount() == 1 })

	said := h.term.saidLines()
	if len(said) != 3 || !strings.HasPrefix(said[0], "ansible-playbook playbooks/onboard.yml") ||
		said[1] != "ok" || !strings.HasPrefix(said[2], "log: ") {
		t.Errorf("said %q, want the command, ok and the log", said)
	}
	if h.status() != "done" {
		t.Errorf("status = %q, want done", h.status())
	}
}

func TestFailedRunSaysSo(t *testing.T) {
	h := newHarness(t)
	h.term.onRun = func(run.Cmd, io.Writer) error { return exitError(t, 2) }
	h.runNow()
	h.waitFor("the failure to be reported", func() bool {
		return strings.Contains(h.app.status.text(), "ansible-playbook exited 2")
	})
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
	h := newHarness(t, func(o *Options) {
		o.Inventory = func(context.Context, inv.Source) (*inv.Inventory, error) {
			return nil, errors.New("no age key")
		}
	})

	// The detail pane reads it in the background and says so rather than
	// pretending the play targets nothing.
	h.waitFor("the pane to say why it cannot tell", func() bool {
		return strings.Contains(h.app.detail.GetText(true), "no inventory: no age key")
	})

	h.press('L')
	h.waitFor("the error", func() bool { return strings.Contains(h.app.status.text(), "no age key") })
}

// The passwords go to files that exist only while the run does.
func TestCredentialsBecomePasswordFiles(t *testing.T) {
	h := newHarness(t)
	h.inspect(func() {
		h.app.form.setCredentials(credentials{User: "pi", Password: "s3cret", Become: "sud0", Target: "10.0.0.9"})
	})

	// The files have to be there while the command runs.
	var during []string
	h.term.onRun = func(c run.Cmd, _ io.Writer) error {
		for _, flag := range []string{"--connection-password-file", "--become-password-file"} {
			b, _ := os.ReadFile(valueAfter(c.Args, flag))
			during = append(during, string(b))
		}
		return nil
	}
	h.runNow()
	h.waitFor("the run", func() bool { return h.term.pauseCount() == 1 })

	args := h.term.lastCmd().Args
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

	if want := []string{"s3cret\n", "sud0\n"}; !reflect.DeepEqual(during, want) {
		t.Errorf("password files held %q during the run, want %q", during, want)
	}

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
	h.waitFor("the signing to run", func() bool { return h.term.pauseCount() == 1 })

	cmd := h.term.cmds[0]
	if filepath.Base(cmd.Path) != "ssh-keygen" {
		t.Fatalf("ran %q", cmd.Path)
	}
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"-s " + ca, "-V +2h", "-n ansible-admin", key + ".pub"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	for _, line := range h.term.saidLines() {
		if strings.HasPrefix(line, "log: ") {
			t.Errorf("signing was logged: %q", line)
		}
	}
}

// Every playbook run goes to a file of its own, as plain text: no colour
// codes, no CRs, pave's own lines marked as in the pane.
func TestARunIsLogged(t *testing.T) {
	h := newHarness(t)
	h.term.onRun = func(_ run.Cmd, log io.Writer) error {
		io.WriteString(log, "\x1b[0;32mok: [master1]\x1b[0m\r\n")
		io.WriteString(log, "PLAY RECAP ****\r\n")
		return nil
	}
	h.runNow()
	h.waitFor("the run", func() bool { return h.term.pauseCount() == 1 })

	b, err := os.ReadFile(filepath.Join(h.logDir, "onboard-20260901-210000.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	for _, want := range []string{":: ansible-playbook ", "\nok: [master1]\nPLAY RECAP ****\n:: ok\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("log %q lacks %q", log, want)
		}
	}
	if strings.ContainsAny(log, "\x1b\r") {
		t.Errorf("log %q has escape codes or CRs", log)
	}
	if _, err := os.Stat(filepath.Join(h.logDir, ".gitignore")); err != nil {
		t.Errorf("no .gitignore in the log directory: %v", err)
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

// Integration: a real ansible-playbook, syntax-checking a real playbook with
// the project's own ansible.cfg.
func TestRealAnsibleSyntaxCheck(t *testing.T) {
	if os.Getenv("PAVE_IT") != "1" {
		t.Skip("set PAVE_IT=1 to run against a real ansible")
	}
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("no ansible-playbook on PATH")
	}

	var screen bytes.Buffer
	h := newHarness(t)
	h.term.onRun = func(c run.Cmd, log io.Writer) error { return run.Tee(c, &screen, log) }
	h.inspect(func() { h.app.form.input(fieldExtraArgs).SetText("--syntax-check") })

	h.runNow()
	// ansible is python: on a cold runner it takes seconds to get going.
	h.waitWithin(2*time.Minute, "the syntax check", func() bool { return h.term.pauseCount() == 1 })
	if h.status() != "done" {
		t.Errorf("status = %q, output:\n%s", h.status(), screen.String())
	}
}

// tcell gives backspace and ^H the same key code, so a binding on ^H would
// eat every backspace typed into the form.
func TestBackspaceEditsTheFormRatherThanOpeningAPicker(t *testing.T) {
	h := newHarness(t)
	h.key(tcell.KeyEnter)
	h.waitFor("the form", func() bool { return h.app.form.HasFocus() })

	h.focusField(fieldLimit)
	h.waitFor("the limit field", func() bool { return h.app.form.input(fieldLimit).HasFocus() })

	h.typeText("kubeworkers")
	h.waitFor("the text", func() bool { return h.app.form.limit() == "kubeworkers" })
	h.key(tcell.KeyBackspace2)
	h.waitFor("the last character to go", func() bool { return h.app.form.limit() == "kubeworker" })
	if !h.app.optionsFront() {
		t.Error("backspace opened something on top of the options")
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
	h.key(tcell.KeyF5)
	h.waitFor("the options dialog", func() bool { return h.app.optionsFront() })
	h.sync()
	screen := h.screenText()

	for _, want := range []string{"diff", "verbose (-v)", "extra args", "run", "hosts…", "credentials…", "cancel", markOn, markOff} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen does not show %q:\n%s", want, screen)
		}
	}

	// The confirmation on top of it, with all three of its answers.
	h.key(tcell.KeyF5)
	h.waitFor("the confirmation", func() bool { return h.app.modalOpen() && !h.app.optionsFront() })
	h.sync()
	for _, want := range []string{"playbooks/onboard.yml", "run", "check mode", "cancel"} {
		if screen := h.screenText(); !strings.Contains(screen, want) {
			t.Errorf("the confirmation does not show %q:\n%s", want, screen)
		}
	}
	h.key(tcell.KeyEscape)
	h.waitFor("the confirmation to close", func() bool { return h.app.optionsFront() })
	// The tree markers are drawn through tview's tag parser, so they must not
	// look like colour tags either. They are behind the dialog, on the left.
	h.key(tcell.KeyEscape)
	h.waitFor("the dialog to close", func() bool { return !h.app.modalOpen() })
	if screen := h.screenText(); !strings.Contains(screen, iconProject+" base") {
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

// The right pane is what the tool is for: which machines a play reaches, and
// what it does to them, before anything is run.
func TestDetailSaysWhatThePlayTouches(t *testing.T) {
	h := newHarness(t)

	// Past the other project's playbooks and its node to the one wanted.
	onSite := func() bool {
		pb := h.app.tree.currentPlaybook()
		return pb != nil && strings.Contains(pb.Rel, "site")
	}
	for i := 0; i < 6; i++ {
		var there bool
		h.inspect(func() { there = onSite() })
		if there {
			break
		}
		h.key(tcell.KeyDown)
		h.sync()
	}
	h.waitFor("the cluster playbook", onSite)
	// The inventory is read in the background, so the affects line arrives a
	// moment after the rest of the pane.
	h.waitFor("the inventory to be resolved", func() bool {
		return strings.Contains(h.app.detail.GetText(true), "affects")
	})

	detail := h.app.detail.GetText(true)
	for _, want := range []string{
		"Bring the cluster up to date.",
		"1 play · 2 hosts in total",
		"hosts    kubemasters",
		"affects  2 hosts: master1, master2",
		"roles    k3s",
		"tasks    1 task: reboot",
		"tags     reboot",
		"runs     serial 1",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the pane does not say %q:\n%s", want, detail)
		}
	}
}

// The options are asked for before a run rather than sitting in the layout,
// and cancelling one runs nothing.
func TestTheOptionsAreADialog(t *testing.T) {
	h := newHarness(t)
	h.sync()
	if strings.Contains(h.screenText(), "check mode") {
		t.Error("the options are drawn without being asked for")
	}

	h.key(tcell.KeyF5)
	h.waitFor("the dialog", func() bool { return h.app.optionsFront() })
	h.waitFor("the command it would run", func() bool {
		return strings.Contains(h.screenText(), "ansible-playbook playbooks/onboard.yml")
	})

	h.key(tcell.KeyEscape)
	h.waitFor("the dialog to close", func() bool { return !h.app.modalOpen() })
	if strings.Contains(h.screenText(), "check mode") {
		t.Error("the options are still drawn after cancelling")
	}
	if h.term.count() != 0 {
		t.Errorf("cancelling started %d runs", h.term.count())
	}
}
