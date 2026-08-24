package control

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/mail"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"register"},
		Summary: "create an account (only meaningful for unregistered keys)",
		Run: func(c *Ctx, args []string) int {
			return c.fail(protocol.ExitUsage,
				"this SSH key already belongs to %s. To register a new account, connect with the key it should use:\n  ssh -F /dev/null -i <newkey> git@<host> register ...",
				c.User.Username)
		}})
	register(Command{Path: []string{"email", "add"},
		Summary: "add an address and mail a verification code: email add <address>", Run: runEmailAdd})
	register(Command{Path: []string{"email", "verify"},
		Summary: "confirm a verification code: email verify <code>", Run: runEmailVerify})
}

func siteHost(cfg config.Config) string {
	h := strings.TrimPrefix(strings.TrimPrefix(cfg.Server.SiteURL, "https://"), "http://")
	return strings.TrimSuffix(h, "/")
}

func sendVerification(cfg config.Config, st *store.Store, userID int64, address string) error {
	code, hash, err := store.NewToken()
	if err != nil {
		return err
	}
	if err := st.CreateEmailToken(userID, address, hash, 24*time.Hour); err != nil {
		return err
	}
	body := fmt.Sprintf(
		"Someone (hopefully you) added this address to an account on %s.\n\n"+
			"To verify it, run:\n\n    ssh git@%s email verify %s\n\n"+
			"The code expires in 24 hours. If this wasn't you, ignore this mail.\n",
		siteHost(cfg), siteHost(cfg), code)
	return mail.Send(cfg, address, "verify your email on "+siteHost(cfg), body)
}

func runEmailAdd(c *Ctx, args []string) int {
	if len(args) != 1 || !strings.Contains(args[0], "@") {
		return c.fail(protocol.ExitUsage, "usage: email add <address>")
	}
	if c.Cfg.Mail.SMTPHost == "" {
		return c.fail(protocol.ExitFailure, "this instance has no SMTP configured; ask an admin to verify the address (gitbayd admin email verify)")
	}
	if err := c.Store.AddEmail(c.User.ID, args[0], "", false); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := sendVerification(c.Cfg, c.Store, c.User.ID, args[0]); err != nil {
		return c.fail(protocol.ExitFailure, "sending verification mail: %v", err)
	}
	return c.emit(map[string]string{"address": args[0], "status": "verification_sent"}, func(w io.Writer) {
		fmt.Fprintf(w, "verification code sent to %s\n", args[0])
	})
}

func runEmailVerify(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: email verify <code>")
	}
	address, err := c.Store.ConsumeEmailToken(c.User.ID, store.HashToken(args[0]))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitUsage, "that code is invalid, expired, or already used")
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.VerifyEmail(c.User.ID, address, "smtp"); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.ClearPending(c.User.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"address": address, "status": "verified"}, func(w io.Writer) {
		fmt.Fprintf(w, "%s verified; your account is active\n", address)
	})
}

// RunRegister handles the one command an UNAUTHENTICATED key may run. It is
// dispatched outside the normal registry: the caller has already checked
// that registration is enabled and that argv[0] == "register".
func RunRegister(cfg config.Config, st *store.Store, pub ssh.PublicKey, argv []string,
	stdout, stderr io.Writer) int {
	var username, email, invite string
	args := argv[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--username", "--email", "--invite":
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "%s requires a value\n", args[i])
				return protocol.ExitUsage
			}
			switch args[i] {
			case "--username":
				username = args[i+1]
			case "--email":
				email = args[i+1]
			case "--invite":
				invite = args[i+1]
			}
			i++
		default:
			fmt.Fprintf(stderr, "unexpected argument %q\n", args[i])
			return protocol.ExitUsage
		}
	}
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(stderr, format+"\n", a...)
		return code
	}
	if username == "" {
		return fail(protocol.ExitUsage, "usage: register --username <name> --email <address> | register --username <name> --invite <code>")
	}
	if err := policy.ValidateOwnerName(username); err != nil {
		return fail(protocol.ExitUsage, "%v", err)
	}

	fp := ssh.FingerprintSHA256(pub)
	switch cfg.Registration.Mode {
	case "invite":
		if invite == "" {
			return fail(protocol.ExitDenied, "this instance is invite-only: register --username <name> --invite <code>")
		}
		// One transaction: a failure at any step leaves the invite
		// redeemable and no partial account behind.
		_, err := st.RedeemInvite(store.HashToken(invite), username, fp, pub.Type(), pub.Marshal())
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fail(protocol.ExitDenied, "that invite is invalid or already used")
			}
			return fail(protocol.ExitUsage, "%v", err)
		}
		fmt.Fprintf(stdout, "welcome, %s — your account is active\n", username)
		return protocol.ExitOK

	case "open":
		if email == "" || !strings.Contains(email, "@") {
			return fail(protocol.ExitUsage, "usage: register --username <name> --email <address>")
		}
		uid, err := st.RegisterOpen(username, email, fp, pub.Type(), pub.Marshal())
		if err != nil {
			return fail(protocol.ExitUsage, "%v", err)
		}
		if err := sendVerification(cfg, st, uid, email); err != nil {
			return fail(protocol.ExitFailure, "sending verification mail: %v", err)
		}
		fmt.Fprintf(stdout,
			"account %s created. A verification code was sent to %s.\nActivate with:\n\n    ssh git@%s email verify <code>\n",
			username, email, siteHost(cfg))
		return protocol.ExitOK

	default:
		return fail(protocol.ExitDenied, "registration is closed on this instance")
	}
}


