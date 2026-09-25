package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/mnorrsken/pave/internal/config"
	"github.com/mnorrsken/pave/internal/inv"
	"github.com/mnorrsken/pave/internal/invfile"
	"github.com/mnorrsken/pave/internal/run"
	"github.com/mnorrsken/pave/internal/sshcert"
)

// fakeTerminal stands in for the terminal pave gives back for a run. It
// records what it was asked to run and to say, and a command does whatever
// onRun says, which is how a test has ansible print something or fail.
type fakeTerminal struct {
	mu     sync.Mutex
	cmds   []run.Cmd
	said   []string
	pauses int
	onRun  func(c run.Cmd, log io.Writer) error
}

func (f *fakeTerminal) Run(c run.Cmd, log io.Writer) error {
	f.mu.Lock()
	f.cmds = append(f.cmds, c)
	fn := f.onRun
	f.mu.Unlock()
	if fn != nil {
		return fn(c, log)
	}
	return nil
}

func (f *fakeTerminal) Say(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.said = append(f.said, msg)
}

func (f *fakeTerminal) Pause() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauses++
}

func (f *fakeTerminal) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cmds)
}

func (f *fakeTerminal) lastCmd() run.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cmds) == 0 {
		return run.Cmd{}
	}
	return f.cmds[len(f.cmds)-1]
}

func (f *fakeTerminal) saidLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.said...)
}

func (f *fakeTerminal) pauseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pauses
}

// exitError is what a command that exits with code returns.
func exitError(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if run.ExitCode(err) != code {
		t.Fatalf("sh exited %v, want %d", err, code)
	}
	return err
}

// testInventory is what the fake inventory loader returns.
func testInventory() *inv.Inventory {
	return &inv.Inventory{
		Groups: []inv.Group{
			{Name: "kubemasters", Hosts: []string{"master1", "master2"}},
			{Name: "kubeworkers", Hosts: []string{"worker1"}},
		},
		Hosts: []string{"master1", "master2", "worker1"},
	}
}

// harness is an App running on a simulation screen, with everything that
// would touch the system replaced.
type harness struct {
	t       *testing.T
	app     *App
	screen  tcell.SimulationScreen
	term    *fakeTerminal
	cert    sshcert.Status
	stopped chan struct{}

	// edits records what the editor would have been asked to run, and editFn
	// is what happens instead of running it.
	editMu sync.Mutex
	edits  []run.Cmd
	editFn func(run.Cmd) error

	// logDir is where the run logs go: a temporary directory, never the
	// fixture tree.
	logDir string
}

// onEdit replaces what the fake editor does: write the file, fail, whatever
// the test is about.
func (h *harness) onEdit(f func(run.Cmd) error) {
	h.editMu.Lock()
	defer h.editMu.Unlock()
	h.editFn = f
}

func (h *harness) editCount() int {
	h.editMu.Lock()
	defer h.editMu.Unlock()
	return len(h.edits)
}

func (h *harness) lastEdit() run.Cmd {
	h.editMu.Lock()
	defer h.editMu.Unlock()
	if len(h.edits) == 0 {
		return run.Cmd{}
	}
	return h.edits[len(h.edits)-1]
}

func newHarness(t *testing.T, tweak ...func(*Options)) *harness {
	t.Helper()

	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(140, 44)

	h := &harness{t: t, screen: screen, term: &fakeTerminal{}, stopped: make(chan struct{}),
		logDir: filepath.Join(t.TempDir(), "logs")}
	h.cert = sshcert.Status{Present: true, Principals: []string{"ansible-admin"},
		ValidTo: time.Date(2026, 9, 2, 9, 0, 0, 0, time.Local)}

	cfg, err := config.Load(t.TempDir() + "/none.yaml")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Root:   "testdata/tree",
		Config: cfg,
		Screen: screen,
		Inventory: func(context.Context, inv.Source) (*inv.Inventory, error) {
			return testInventory(), nil
		},
		// The layout comes from a real look at the fixture tree; only the
		// question ansible would be asked is answered here.
		Layout: func(_ context.Context, src invfile.Source) (*invfile.Layout, error) {
			return invfile.Describe(src.Dir, []string{"../inventory/hosts.yml"}), nil
		},
		Edit: func(c run.Cmd) error {
			h.editMu.Lock()
			h.edits = append(h.edits, c)
			fn := h.editFn
			h.editMu.Unlock()
			if fn != nil {
				return fn(c)
			}
			return nil
		},
		OnTerminal: func(session func(Terminal)) error {
			session(h.term)
			return nil
		},
		LogDir: h.logDir,
		Cert:   func(string) (sshcert.Status, error) { return h.cert, nil },
		Now:    func() time.Time { return time.Date(2026, 9, 1, 21, 0, 0, 0, time.Local) },
	}
	for _, f := range tweak {
		f(&opts)
	}

	h.app = New(opts)
	go func() {
		defer close(h.stopped)
		if err := h.app.Run(); err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	t.Cleanup(h.stop)
	h.sync()
	return h
}

func (h *harness) stop() {
	h.app.QueueUpdate(func() { h.app.Stop() })
	select {
	case <-h.stopped:
	case <-time.After(3 * time.Second):
		h.t.Error("the app did not stop")
	}
}

// inspect runs f on the event loop, which is the only place the App's fields
// may be touched.
func (h *harness) inspect(f func()) {
	h.t.Helper()
	done := make(chan struct{})
	h.app.QueueUpdateDraw(func() {
		f()
		close(done)
	})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		h.t.Fatal("the event loop is stuck")
	}
}

func (h *harness) sync() { h.inspect(func() {}) }

// waitFor polls a condition on the event loop. Key events and queued updates
// arrive on two different channels, so a test cannot assume a key has been
// handled by the time the next update runs.
func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	h.waitWithin(10*time.Second, what, cond)
}

// waitWithin is waitFor with room for something slow. A real ansible starting
// on a cold CI runner takes seconds, not milliseconds, and the default here is
// sized for a fake.
func (h *harness) waitWithin(limit time.Duration, what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		ok := false
		h.inspect(func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	h.t.Fatalf("timed out after %s waiting for %s", limit, what)
}

// focusField tabs round the run form until item i has the keyboard. The
// options open on the run button, so a field is always a few tabs away.
func (h *harness) focusField(i int) {
	h.t.Helper()
	item := h.app.form.GetFormItem(i)
	for n := 0; n <= fieldCount+len(runFormButtons); n++ {
		done := false
		h.inspect(func() { done = item.HasFocus() })
		if done {
			return
		}
		h.key(tcell.KeyTab)
		h.sync()
	}
	h.t.Fatalf("field %d never took the keyboard", i)
}

// treeCopy copies the fixture tree into a temporary directory, for a test
// that creates a file: the repository is not the place for one.
func treeCopy(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "tree")
	if err := os.CopyFS(dst, os.DirFS("testdata/tree")); err != nil {
		t.Fatalf("copy the fixture tree: %v", err)
	}
	return dst
}

// openInventory gets the browser onto the screen the way a user does.
func (h *harness) openInventory() {
	h.t.Helper()
	h.press('i')
	h.waitFor("the inventory browser", func() bool { return h.app.invOpen })
}

// runNow starts a run the way a user does: F5 opens the options, F5 asks the
// confirmation, and enter takes its first button, which is a real run.
func (h *harness) runNow() {
	h.t.Helper()
	h.confirmRun()
	h.key(tcell.KeyEnter)
}

// confirmRun gets as far as the confirmation and leaves it open, so a test
// can pick check mode or back out.
func (h *harness) confirmRun() {
	h.t.Helper()
	h.key(tcell.KeyF5)
	h.waitFor("the run options", func() bool { return h.app.optionsFront() })
	h.key(tcell.KeyF5)
	h.waitFor("the confirmation", func() bool { return h.app.modalOpen() && !h.app.optionsFront() })
}

func (h *harness) press(r rune) {
	h.t.Helper()
	h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
}

func (h *harness) key(k tcell.Key) {
	h.t.Helper()
	h.screen.InjectKey(k, 0, tcell.ModNone)
}

func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		h.press(r)
	}
}

// status is the current status line.
func (h *harness) status() string {
	var s string
	h.inspect(func() { s = h.app.status.text() })
	return s
}

// screenText is everything currently drawn, one line per row. Style is
// dropped: what these tests care about is whether something was drawn at all.
func (h *harness) screenText() string {
	cells, w, ht := h.screen.GetContents()
	var b strings.Builder
	for y := 0; y < ht; y++ {
		for x := 0; x < w; x++ {
			r := cells[y*w+x].Runes
			if len(r) == 0 || r[0] == 0 {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(r[0])
		}
		b.WriteRune('\n')
	}
	return b.String()
}
