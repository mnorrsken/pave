package run

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/creack/pty"
)

// Cmd is a command to run on a pty.
type Cmd struct {
	Path string
	Args []string
	Dir  string
	Env  []string
	Rows int
	Cols int
}

// Session is a running command. The UI reads its output, writes the keys the
// user types into it so ansible's own prompts work, and waits for it to end.
type Session interface {
	io.Reader
	io.Writer
	// Resize tells the child how big the output pane is, so ansible's line
	// wrapping matches what is on screen.
	Resize(rows, cols int) error
	// Interrupt is ^C: it goes to the whole process group, the way a terminal
	// would deliver it, so ansible's own handler runs.
	Interrupt() error
	// Kill ends the run without asking.
	Kill() error
	// Wait returns when the command has exited; the error carries the exit
	// status.
	Wait() error
	Close() error
}

// Runner starts commands. The UI holds one so tests can supply a fake instead
// of really running ansible.
type Runner interface {
	Start(Cmd) (Session, error)
}

// PTYRunner runs commands for real, each on its own pty.
type PTYRunner struct{}

func (PTYRunner) Start(c Cmd) (Session, error) {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env

	rows, cols := c.Rows, c.Cols
	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 100
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", c.Path, err)
	}
	return &ptySession{cmd: cmd, pty: f}, nil
}

type ptySession struct {
	cmd *exec.Cmd
	pty *os.File
}

// Read turns the pty's end-of-child error into a plain EOF. A pty master
// reports EIO once the last slave is gone, which is a normal exit here.
func (s *ptySession) Read(p []byte) (int, error) {
	n, err := s.pty.Read(p)
	if err != nil && errors.Is(err, syscall.EIO) {
		return n, io.EOF
	}
	return n, err
}

func (s *ptySession) Write(p []byte) (int, error) { return s.pty.Write(p) }

func (s *ptySession) Resize(rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		return nil
	}
	return pty.Setsize(s.pty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (s *ptySession) Interrupt() error { return s.signal(syscall.SIGINT) }

func (s *ptySession) Kill() error { return s.signal(syscall.SIGKILL) }

// signal delivers to the process group. pty.Start puts the child in its own
// session, so its pid is also its group id, and ansible's forks get the
// signal too.
func (s *ptySession) signal(sig syscall.Signal) error {
	if s.cmd.Process == nil {
		return errors.New("not started")
	}
	if err := syscall.Kill(-s.cmd.Process.Pid, sig); err != nil {
		return s.cmd.Process.Signal(sig)
	}
	return nil
}

func (s *ptySession) Wait() error { return s.cmd.Wait() }

func (s *ptySession) Close() error { return s.pty.Close() }

// ExitCode reports the status a finished Session exited with. It is -1 when
// the command never ran.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// PasswordFile writes a password where ansible can read it with
// --connection-password-file or --become-password-file. The file is created
// 0600 and should be removed as soon as the run ends; passing a password on
// the command line instead would put it in every process listing on the box.
func PasswordFile(secret string) (string, error) {
	f, err := os.CreateTemp("", "pave-pass-*")
	if err != nil {
		return "", fmt.Errorf("create password file: %w", err)
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("create password file: %w", err)
	}
	if _, err := io.WriteString(f, secret+"\n"); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write password file: %w", err)
	}
	return f.Name(), nil
}

// Output runs a command and returns its standard output. It is for the short,
// non-interactive helpers — reading a certificate, say — that have no reason
// to go through a pty.
func Output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s: %s", name, firstLine(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
