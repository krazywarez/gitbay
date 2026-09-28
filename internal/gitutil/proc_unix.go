//go:build unix

package gitutil

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts cmd in a process group of its own, so killTree
// ends what it started too: receive-pack runs index-pack and the hooks.
func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		cmd.Process.Kill()
	}
}
