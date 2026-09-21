package e2e

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dir, cleanup, err := makeBinDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		os.Exit(1)
	}
	binDir = dir
	code := m.Run()
	cleanup()
	os.Exit(code)
}
