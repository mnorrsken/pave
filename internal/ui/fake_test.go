package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/mnorrsken/pave/internal/config"
	"github.com/mnorrsken/pave/internal/inv"
	"github.com/mnorrsken/pave/internal/run"
	"github.com/mnorrsken/pave/internal/sshcert"
)

// fakeRunner stands in for ansible. It records what it was asked to run and
// hands back a session the test drives by hand.
type fakeRunner struct {
	mu       sync.Mutex
	cmds     []run.Cmd
	sessions []*fakeSession
	started  chan struct{}
	// fail makes Start return an error instead of a session.
	fail error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{started: make(chan struct{}, 8)}
}

func (r *fakeRunner) Start(c run.Cmd) (run.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, c)
	if r.fail != nil {
		return nil, r.fail
	}
	s := newFakeSession()
	r.sessions = append(r.sessions, s)
	select {
	case r.started <- struct{}{}:
	default:
	}
	return s, nil
}

func (r *fakeRunner) lastCmd() run.Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cmds) == 0 {
		return run.Cmd{}
	}
	return r.cmds[len(r.cmds)-1]
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cmds)
}

// session waits for the nth session to be started and returns it.
func (r *fakeRunner) session(t *testing.T, n int) *fakeSession {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.sessions) > n {
			s := r.sessions[n]
			r.mu.Unlock()
			return s
		}
		r.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no session %d was started", n)
	return nil
}

type fakeSession struct {
	r *io.PipeReader
	w *io.PipeWriter

	mu         sync.Mutex
	in         []byte
	interrupts int
	killed     bool
	rows, cols int

	once sync.Once
	done chan struct{}
	err  error
}

func newFakeSession() *fakeSession {
	r, w := io.Pipe()
	return &fakeSession{r: r, w: w, done: make(chan struct{})}
}

func (s *fakeSession) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *fakeSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.in = append(s.in, p...)
	return len(p), nil
}

func (s *fakeSession) Resize(rows, cols int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows, s.cols = rows, cols
	return nil
}

func (s *fakeSession) Interrupt() error {
	s.mu.Lock()
	s.interrupts++
	s.mu.Unlock()
	return nil
}

func (s *fakeSession) Kill() error {
	s.mu.Lock()
	s.killed = true
	s.mu.Unlock()
	s.finish(errors.New("killed"))
	return nil
}

func (s *fakeSession) Wait() error {
	<-s.done
	return s.err
}

func (s *fakeSession) Close() error { return s.r.Close() }

// emit is output arriving from the command.
func (s *fakeSession) emit(text string) { s.w.Write([]byte(text)) }

// finish ends the command with an exit error, or nil for success.
func (s *fakeSession) finish(err error) {
	s.once.Do(func() {
		s.err = err
		s.w.Close()
		close(s.done)
	})
}

func (s *fakeSession) typed() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.in)
}

func (s *fakeSession) interruptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interrupts
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
	t      *testing.T
	app    *App
	screen tcell.SimulationScreen
	runner *fakeRunner
	cert   sshcert.Status
	// invErr makes the inventory loader fail.
	invErr  error
	stopped chan struct{}
}

func newHarness(t *testing.T, tweak ...func(*Options)) *harness {
	t.Helper()

	screen := tcell.NewSimulationScreen("UTF-8")
	screen.SetSize(140, 44)

	h := &harness{t: t, screen: screen, runner: newFakeRunner(), stopped: make(chan struct{})}
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
		Runner: h.runner,
		Inventory: func(context.Context, inv.Source) (*inv.Inventory, error) {
			if h.invErr != nil {
				return nil, h.invErr
			}
			return testInventory(), nil
		},
		Cert: func(string) (sshcert.Status, error) { return h.cert, nil },
		Now:  func() time.Time { return time.Date(2026, 9, 1, 21, 0, 0, 0, time.Local) },
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
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ok := false
		h.inspect(func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
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
