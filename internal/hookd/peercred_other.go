//go:build !linux

package hookd

import "net"

// checkPeer reads peer credentials on Linux only; elsewhere the
// socket's 0600 mode is the boundary.
func checkPeer(net.Conn) error { return nil }
