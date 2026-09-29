package imapc

import (
	"bufio"
	"bytes"
	"net"
	"testing"
)

// FuzzReadResponse feeds server bytes to the response reader, literals
// included.
func FuzzReadResponse(f *testing.F) {
	f.Add([]byte("* 1 FETCH (UID 3 BODY[] {5}\r\nhello)\r\ng1 OK done\r\n"))
	f.Add([]byte("* SEARCH 1 2 3\r\n* OK {99999999999}\r\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		a, b := net.Pipe()
		defer b.Close()
		c := &Client{conn: a, r: bufio.NewReader(bytes.NewReader(in)), left: cmdBudget}
		for i := 0; i < 8; i++ {
			if _, err := c.readResponse(); err != nil {
				return
			}
		}
	})
}
