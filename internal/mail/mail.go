// Package mail sends transactional email over SMTP: verification codes and
// invites. STARTTLS is used when the server offers it; PLAIN auth when
// credentials are configured.
package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/config"
)

// Send delivers one plain-text message. cfg.Mail.SMTPHost is host:port.
func Send(cfg config.Config, to, subject, body string) error {
	m := cfg.Mail
	if m.SMTPHost == "" || m.From == "" {
		return fmt.Errorf("[mail] smtp_host and from must be configured")
	}
	host := m.SMTPHost
	if !strings.Contains(host, ":") {
		host += ":587"
	}
	hostname, _, _ := net.SplitHostPort(host)

	msg := strings.NewReplacer("\n", "\r\n").Replace(fmt.Sprintf(
		"From: %s\nTo: %s\nSubject: %s\nDate: %s\nMIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\n\n%s\n",
		m.From, to, subject, time.Now().Format(time.RFC1123Z), body))

	c, err := smtp.Dial(host)
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", host, err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: hostname}); err != nil {
			return fmt.Errorf("starttls: %w", err)
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
