package run

import (
	"fmt"
	"os"
	"os/exec"
)

// Interactive runs a command on the terminal pave itself was started on,
// which is what an editor needs: it wants the real stdin, the real size, and
// nothing else drawing over it. The caller has to have given the terminal
// back first — see the interface's suspend.
func Interactive(c Cmd) error {
	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", c.Path, err)
	}
	return nil
}
