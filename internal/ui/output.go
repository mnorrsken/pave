package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// flushEvery throttles how often a run's output reaches the screen. ansible
// writes a lot of short lines and one redraw per line is wasted work.
const flushEvery = 40 * time.Millisecond

// flushAt is the size that forces a flush before the timer says so, so a
// burst of output does not sit in the buffer.
const flushAt = 64 << 10

// outputPane shows a run as it happens. It is also the keyboard for the
// process: what the user types goes to the pty, so ansible's own prompts
// work, while the scrolling keys stay here.
//
// The bytes arrive on the run's goroutine and are parked in pending; only the
// main loop ever writes them into the TextView, because tview's own state is
// not safe to read while another goroutine writes it.
type outputPane struct {
	*tview.TextView

	// w escapes and translates on the way in. It carries the ANSI parser's
	// state between writes, so there is one per pane and only the main loop
	// touches it.
	w io.Writer

	mu      sync.Mutex
	pending []byte
	// following keeps the view pinned to the last line until the user scrolls
	// up, which is what every log viewer does.
	following bool
}

func newOutputPane() *outputPane {
	o := &outputPane{TextView: tview.NewTextView(), following: true}
	o.SetDynamicColors(true).SetScrollable(true).SetWrap(true)
	o.SetBackgroundColor(colorBackground)
	o.SetBorder(true).SetBorderColor(colorBorder).SetTitleColor(colorTitle).SetTitle(" output ")
	// The ANSI translation has to happen after the brackets are escaped: the
	// output is full of them — PLAY [all], TASK [role : task], ok: [host] —
	// and a TextView with dynamic colours would eat every one as a tag.
	o.w = escapeWriter{w: tview.ANSIWriter(o.TextView)}
	return o
}

func (o *outputPane) reset(title string) {
	o.mu.Lock()
	o.pending = nil
	o.following = true
	o.mu.Unlock()

	o.Clear()
	o.setTitle(title)
	o.ScrollToEnd()
}

func (o *outputPane) setTitle(title string) { o.SetTitle(" " + title + " ") }

// push parks output from the running command. It reports true when the buffer
// has grown enough to be drawn straight away; otherwise the run's ticker gets
// to it within flushEvery.
func (o *outputPane) push(b []byte) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pending = append(o.pending, b...)
	return len(o.pending) >= flushAt
}

// empty reports whether there is nothing waiting to be drawn.
func (o *outputPane) empty() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending) == 0
}

// flush writes what has arrived into the view. Main loop only.
func (o *outputPane) flush() {
	o.mu.Lock()
	buf := o.pending
	o.pending = nil
	o.mu.Unlock()

	if len(buf) > 0 {
		o.w.Write(buf)
	}
	if o.follow() {
		o.ScrollToEnd()
	}
}

// banner writes a line of pave's own, told apart from the command's output by
// its colour and its leading marker. Main loop only.
func (o *outputPane) banner(color tcell.Color, format string, args ...any) {
	o.flush() // keep pave's lines in the right place in the output
	fmt.Fprintf(o.TextView, "[%s]:: %s[-]\n", tag(color), tview.Escape(fmt.Sprintf(format, args...)))
	if o.follow() {
		o.ScrollToEnd()
	}
}

func (o *outputPane) follow() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.following
}

func (o *outputPane) setFollow(on bool) {
	o.mu.Lock()
	o.following = on
	o.mu.Unlock()
	if on {
		o.ScrollToEnd()
	}
}

// text is the output with the colour tags stripped, for saving and for tests.
// Main loop only.
func (o *outputPane) text() string { return o.GetText(true) }

// scrollKey handles the keys that stay with the pane instead of going to the
// process. It reports whether the key is the process's.
func (o *outputPane) scrollKey(ev *tcell.EventKey) bool {
	switch ev.Key() {
	case tcell.KeyPgUp, tcell.KeyUp, tcell.KeyHome:
		o.setFollow(false)
		return false // let the TextView scroll
	case tcell.KeyPgDn, tcell.KeyDown:
		return false
	case tcell.KeyEnd:
		o.setFollow(true)
		return false
	}
	return true
}

// savePath is the default name offered when saving a log.
func savePath(playbook string, when time.Time) string {
	name := strings.ReplaceAll(playbook, "/", "-")
	name = strings.TrimSuffix(name, ".yml")
	name = strings.TrimSuffix(name, ".yaml")
	if name == "" {
		name = "run"
	}
	return fmt.Sprintf("pave-%s-%s.log", name, when.Format("20060102-150405"))
}

// escapeWriter rewrites the square brackets in a program's output so tview
// shows them instead of taking them for colour tags.
type escapeWriter struct{ w io.Writer }

func (e escapeWriter) Write(p []byte) (int, error) {
	if !bytes.ContainsRune(p, '[') {
		return e.w.Write(p)
	}
	if _, err := e.w.Write([]byte(tview.Escape(string(p)))); err != nil {
		return 0, err
	}
	// The caller wrote len(p) bytes; the expansion is our business.
	return len(p), nil
}
