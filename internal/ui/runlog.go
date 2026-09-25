package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runLog is a playbook run written to a file as it arrives, as plain text:
// the colours are taken out and the pty's CRLFs made plain LFs, so the file
// reads the same in an editor, in grep and in a mail. It is written on the
// run's goroutine and never touches tview. A nil runLog writes nothing, which
// is what a run that could not open one gets.
type runLog struct {
	path string
	f    *os.File
	w    io.Writer
	// midLine says the last thing written did not end a line, so one of
	// pave's own lines has to start a new one.
	midLine bool
}

// openRunLog creates the log for a run of playbook in dir. The directory gets
// a .gitignore of its own the first time, since it usually sits in the tree
// of playbooks, which is usually a repository.
func openRunLog(dir, playbook string, when time.Time) (*runLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
		os.WriteFile(ignore, []byte("*\n"), 0o644)
	}

	name := logName(playbook, when)
	for i := 2; ; i++ {
		path := filepath.Join(dir, name+".log")
		// The output can show what a host is configured with, so the file is
		// the user's alone.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		switch {
		case err == nil:
			l := &runLog{path: path, f: f}
			l.w = &newlineWriter{w: &ansiStripper{w: writerFunc(l.write)}}
			return l, nil
		case !errors.Is(err, fs.ErrExist) || i > 9:
			return nil, err
		}
		// Two runs in the same second.
		name = fmt.Sprintf("%s-%d", logName(playbook, when), i)
	}
}

// logName is <playbook>-<date>-<time>, the playbook without its directory
// or extension.
func logName(playbook string, when time.Time) string {
	name := filepath.Base(playbook)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if name == "" || name == "." {
		name = "run"
	}
	return name + "-" + when.Format("20060102-150405")
}

// Write is the command's output.
func (l *runLog) Write(p []byte) (int, error) {
	if l == nil {
		return len(p), nil
	}
	return l.w.Write(p)
}

// line is one of pave's own, marked the way the output pane marks it.
func (l *runLog) line(msg string) {
	if l == nil {
		return
	}
	if l.midLine {
		l.write([]byte("\n"))
	}
	l.write([]byte(":: " + msg + "\n"))
}

func (l *runLog) write(p []byte) (int, error) {
	if len(p) > 0 {
		l.midLine = p[len(p)-1] != '\n'
	}
	return l.f.Write(p)
}

func (l *runLog) Close() error {
	if l == nil {
		return nil
	}
	return l.f.Close()
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// ansiStripper drops the terminal escape sequences from a stream: the colour
// codes ansible writes, and anything else a program sends a pty. It carries
// its state between writes because a read can end in the middle of one.
type ansiStripper struct {
	w     io.Writer
	state int
}

const (
	ansiText = iota
	ansiEsc  // after ESC
	ansiCSI  // after ESC [, up to a final byte
	ansiOSC  // after ESC ], up to BEL or ESC \
	ansiOSCEsc
)

func (s *ansiStripper) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch s.state {
		case ansiText:
			if b == 0x1b {
				s.state = ansiEsc
			} else {
				out = append(out, b)
			}
		case ansiEsc:
			switch b {
			case '[':
				s.state = ansiCSI
			case ']':
				s.state = ansiOSC
			default:
				s.state = ansiText // a two byte sequence
			}
		case ansiCSI:
			if b >= 0x40 && b <= 0x7e {
				s.state = ansiText
			}
		case ansiOSC:
			switch b {
			case 0x07:
				s.state = ansiText
			case 0x1b:
				s.state = ansiOSCEsc
			}
		case ansiOSCEsc:
			s.state = ansiText
		}
	}
	if len(out) > 0 {
		if _, err := s.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// newlineWriter turns the pty's CRLF line endings into plain LF, the way a
// text file has them. A lone \r, which a progress line rewrites itself with,
// becomes a line of its own. It carries the state between writes because a
// read can end between the \r and the \n.
type newlineWriter struct {
	w       io.Writer
	afterCR bool
}

func (n *newlineWriter) Write(p []byte) (int, error) {
	if !n.afterCR && !bytes.ContainsRune(p, '\r') {
		return n.w.Write(p)
	}
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch {
		case b == '\r':
			// The break itself; a \n right after it is the same one.
			out = append(out, '\n')
		case n.afterCR && b == '\n':
		default:
			out = append(out, b)
		}
		n.afterCR = b == '\r'
	}
	if _, err := n.w.Write(out); err != nil {
		return 0, err
	}
	// The caller wrote len(p) bytes; the CRLFs are our business.
	return len(p), nil
}
