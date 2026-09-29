package imapc

import (
	"time"

	"gitbay.org/gitbay/internal/config"
)

// Open connects to the configured mailbox, logs in with the password
// file's password, and opens the mailbox: read-only (EXAMINE) or for
// setting flags (SELECT). It returns the number of messages in it.
func Open(in config.MailInbound, readOnly bool, timeout time.Duration) (*Client, int, error) {
	pass, err := in.Password()
	if err != nil {
		return nil, 0, err
	}
	c, err := Dial(in.Addr(), in.TLS == "starttls", timeout)
	if err != nil {
		return nil, 0, err
	}
	if err := c.Login(in.User, pass); err != nil {
		c.Close()
		return nil, 0, err
	}
	open := c.Select
	if readOnly {
		open = c.Examine
	}
	n, err := open(in.MailboxName())
	if err != nil {
		c.Close()
		return nil, 0, err
	}
	return c, n, nil
}
