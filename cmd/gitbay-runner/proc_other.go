//go:build !unix

package main

import "os/exec"

func ownProcessGroup(cmd *exec.Cmd) {}

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
