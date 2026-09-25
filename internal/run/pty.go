package run

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Cmd is a command to run on the terminal.
type Cmd struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

// Tee runs a command on the terminal pave was started on, its output going to
// screen and copied to log as it arrives. The caller has to have given the
// terminal back first, as for Interactive.
//
// The output goes through a pty, so the command still sees a terminal on
// stdout and colours it the way it would anywhere else. Its stdin is the real
// terminal, and so is its controlling terminal: a password prompt opens
// /dev/tty and is answered there, without going anywhere near the log. ^C
// reaches it the way it reaches anything run from a shell, and pave, in the
// same process group, lets it decide what to do about it.
func Tee(c Cmd, screen, log io.Writer) error {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return fmt.Errorf("open a pty: %w", err)
	}
	defer ptmx.Close()
	// Not a terminal under a test; the pty keeps its default size then.
	pty.InheritSize(os.Stdin, ptmx)

	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdin = os.Stdin
	cmd.Stdout, cmd.Stderr = tty, tty

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGWINCH)
	defer signal.Stop(sigs)

	err = cmd.Start()
	tty.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", c.Path, err)
	}

	copied := make(chan struct{})
	go func() {
		defer close(copied)
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				screen.Write(buf[:n])
				// A log that cannot be written is no reason to stop showing
				// the run.
				if log != nil {
					log.Write(buf[:n])
				}
			}
			if err != nil {
				return // EIO once the last of the command's ends is closed
			}
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	for {
		select {
		case s := <-sigs:
			if s == syscall.SIGWINCH {
				pty.InheritSize(os.Stdin, ptmx)
			}
		case err := <-exited:
			// Something the command started in the background can hold the
			// pty open after it has gone; what it wrote itself is there
			// well within this.
			select {
			case <-copied:
			case <-time.After(time.Second):
			}
			return err
		}
	}
}

// ExitCode reports the status a finished command exited with. It is -1 when
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
