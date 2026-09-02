//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// A step runs as its own process group, so ending it ends everything it
// started. Killing only the shell leaves its children alive and holding
// the log pipe, and a wait on that pipe lasts as long as the longest
// child: dash forks a single command rather than exec'ing it, so on a
// Debian host "sh -c 'sleep 120'" survived its shell by two minutes.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		cmd.Process.Kill()
	}
}
