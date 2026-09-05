package control

import (
	"fmt"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/mail"
	"gitbay.org/gitbay/internal/store"
)

// maxLoginLinksPerHour bounds what one account's address can be made to
// receive. It matches maxEmailAddsPerHour: enough for a person who mistypes
// and retries, nothing for a script.
const maxLoginLinksPerHour = 5

// loginLinkTTL is longer than the five minutes an SSH-minted link gets.
// That one is pasted from a terminal already open; this one has to survive
// delivery and someone noticing the mail.
const loginLinkTTL = 15 * time.Minute

// RequestLoginLink mails a one-time login link to the account named by
// identifier, which is a username or a verified email address.
//
// It is not a registered command: the caller is an unauthenticated web
// request, and commands run as c.User. RegisterAccount is exported for the
// same reason.
//
// The returned error is for the server log only. Nothing about the outcome
// may reach the caller — that a request found an account, found one without
// a verified address, or found nothing at all must be indistinguishable, or
// the endpoint answers "does this person have an account here?" to anyone
// who asks. Every miss returns nil.
func RequestLoginLink(cfg config.Config, st *store.Store, identifier string) error {
	if cfg.Web.Mode != "accounts" || cfg.Mail.SMTPHost == "" {
		return nil
	}
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil
	}

	var userID int64
	var address string
	if strings.Contains(identifier, "@") {
		id, ok := st.UserIDByVerifiedEmail(identifier)
		if !ok {
			return nil
		}
		userID, address = id, identifier
	} else {
		u, err := st.UserByUsername(identifier)
		if err != nil {
			return nil
		}
		addr, err := st.PrimaryVerifiedEmail(u.ID)
		if err != nil || addr == "" {
			return nil
		}
		userID, address = u.ID, addr
	}

	n, err := st.CountLoginTokensSince(userID, time.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	if n >= maxLoginLinksPerHour {
		return nil
	}

	token, hash, err := store.NewToken()
	if err != nil {
		return err
	}
	if err := st.CreateLoginToken(userID, hash, loginLinkTTL); err != nil {
		return err
	}
	host := siteHost(cfg)
	body := fmt.Sprintf(
		"Someone (hopefully you) asked to log in to %s.\n\n"+
			"Open this link within 15 minutes. It works once:\n\n    %s/login?token=%s\n\n"+
			"If this wasn't you, ignore this mail. Nothing has changed on the account.\n",
		host, strings.TrimSuffix(cfg.Server.SiteURL, "/"), token)
	return mail.Send(cfg, address, "log in to "+host, body)
}
