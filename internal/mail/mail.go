// Package mail sends transactional email over SMTP: verification codes and
// invites. The connection is encrypted with STARTTLS, or with TLS from the
// first byte when mail.tls = "implicit"; a relay that offers neither gets
// nothing unless mail.require_tls is off. PLAIN auth when credentials are
// configured.
package mail

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/config"
)

// rootCAs verifies the relay's certificate; nil is the system pool.
var rootCAs *x509.CertPool

// Send delivers one plain-text message. cfg.Mail.SMTPHost is host:port.
func Send(cfg config.Config, to, subject, body string) error {
	return SendReplyTo(cfg, to, "", subject, body)
}

// SendReplyTo is Send with a Reply-To header, left out when replyTo is
// empty.
func SendReplyTo(cfg config.Config, to, replyTo, subject, body string) error {
	m := cfg.Mail
	if m.SMTPHost == "" || m.From == "" {
		return fmt.Errorf("[mail] smtp_host and from must be configured")
	}
	if strings.ContainsAny(replyTo, "\r\n") {
		return fmt.Errorf("reply-to address contains a line break")
	}
	header := ""
	if replyTo != "" {
		header = "Reply-To: " + replyTo + "\n"
	}
	implicit := m.TLS == "implicit"
	host := m.SMTPHost
	if !strings.Contains(host, ":") {
		if implicit {
			host += ":465"
		} else {
			host += ":587"
		}
	}
	hostname, _, _ := net.SplitHostPort(host)
	tlsCfg := &tls.Config{ServerName: hostname, RootCAs: rootCAs}

	msg := strings.NewReplacer("\n", "\r\n").Replace(fmt.Sprintf(
		"From: %s\nTo: %s\n%sSubject: %s\nDate: %s\nMIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\n\n%s\n",
		m.From, to, header, subject, time.Now().Format(time.RFC1123Z), body))

	c, err := dial(host, hostname, implicit, tlsCfg)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", host, err)
	}
	defer c.Close()
	if !implicit {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		} else if m.TLSRequired() {
			return fmt.Errorf("%s does not offer STARTTLS and mail.require_tls is on; not sending in clear", host)
		}
	}
	if m.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", m.SMTPUser, m.SMTPPass, hostname)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// dial opens the SMTP session: plain TCP for STARTTLS, or TLS from the
// first byte.
func dial(addr, hostname string, implicit bool, tlsCfg *tls.Config) (*smtp.Client, error) {
	if !implicit {
		return smtp.Dial(addr)
	}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return nil, err
	}
	c, err := smtp.NewClient(conn, hostname)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}
