package run

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// Pause prints msg on the terminal pave was started on and waits for esc, q
// or enter, so what a run printed can be read, scrolled and copied before the
// interface is drawn over it again. Like Tee, the caller has to have given
// the terminal back first.
func Pause(msg string) error {
	os.Stdout.WriteString("\x1b[7m " + msg + " \x1b[0m")

	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("read the keyboard: %w", err)
	}
	defer term.Restore(fd, old)

	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return err
		}
		// An esc on its own is the key; one with more after it in the same
		// read is an arrow or a function key, which is not.
		switch {
		case n == 1 && buf[0] == 0x1b, buf[0] == 'q', buf[0] == '\r', buf[0] == 0x03:
			os.Stdout.WriteString("\r\n")
			return nil
		}
	}
}
