//go:build !linux

package main

import (
	"errors"
	"os"
	"os/exec"
)

type buildCgroups struct{}

func prepareBuildCgroups() (*buildCgroups, error) {
	return nil, errors.New("build cgroups need Linux")
}

func (c *buildCgroups) create(id int64, memory, cpus string) (string, *os.File, error) {
	return "", nil, errors.New("build cgroups need Linux")
}

func (c *buildCgroups) remove(dir string) {}

func intoCgroup(cmd *exec.Cmd, f *os.File) {}
