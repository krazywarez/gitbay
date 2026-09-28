//go:build linux

package hookd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// checkPeer refuses a connection from any uid but the daemon's: git,
// and so every hook, runs as the daemon's user.
func checkPeer(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("not a unix socket connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return credErr
	}
	if int(cred.Uid) != os.Getuid() {
		return errors.New("peer uid not permitted")
	}
	return nil
}
