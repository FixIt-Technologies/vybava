//go:build unix

package perfrig

import (
	"os/exec"
	"syscall"
)

// setProcGroup runs the command in its own process group and, on ctx cancel,
// SIGTERMs the WHOLE group — killing only the shell would orphan its children
// and leave the load running after a guard abort.
func setProcGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process != nil {
			return syscall.Kill(-c.Process.Pid, syscall.SIGTERM)
		}
		return nil
	}
}
