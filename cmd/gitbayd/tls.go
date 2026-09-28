package main

import "crypto/tls"

// serverTLS sets the HTTPS listener's protocol floor: TLS 1.2 and 1.3,
// with Go's default cipher suites.
func serverTLS(c *tls.Config) *tls.Config {
	if c == nil {
		c = &tls.Config{}
	}
	c.MinVersion = tls.VersionTLS12
	return c
}
