package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/krazywarez/forge/internal/config"
	"github.com/krazywarez/forge/internal/protocol"
	"github.com/krazywarez/forge/internal/sshd"
)

// authorizedKeysCmd backs sshd's AuthorizedKeysCommand in system mode:
//
//	AuthorizedKeysCommand /usr/bin/forged --config /etc/forge/config.toml authorized-keys %t %k
//	AuthorizedKeysCommandUser git
//
// It prints a forced-command authorized_keys line for registered keys and
// nothing for unknown ones — so unknown keys fail authentication inside
// sshd, before any forge code runs. That is why system mode requires
// registration = "closed".
func authorizedKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "authorized-keys <key-type> <key-base64>",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(args[0] + " " + args[1]))
			if err != nil {
				return nil // unparseable key: no output, auth fails
			}
			key, err := st.SSHKeyByFingerprint(ssh.FingerprintSHA256(pub))
			if err != nil {
				return nil // unknown key: no output, auth fails
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			fmt.Printf("restrict,command=\"%s --config %s shell --key-id %d\" %s %s\n",
				self, configPath, key.ID, args[0], args[1])
			return nil
		},
	}
}

// shellCmd is the forced command sshd runs for an authenticated key. The
// original client command arrives in SSH_ORIGINAL_COMMAND; dispatch is the
// same code path as the embedded listener.
func shellCmd() *cobra.Command {
	var keyID int64
	cmd := &cobra.Command{
		Use:    "shell",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			key, err := st.SSHKeyByID(keyID)
			if err != nil {
				fmt.Fprintln(os.Stderr, "key no longer registered")
				os.Exit(protocol.ExitDenied)
			}
			user, err := st.UserByID(key.UserID)
			if err != nil {
				fmt.Fprintln(os.Stderr, "account no longer exists")
				os.Exit(protocol.ExitDenied)
			}
			_ = st.TouchSSHKey(key.ID)

			cmdline := os.Getenv("SSH_ORIGINAL_COMMAND")
			if cmdline == "" {
				fmt.Fprintf(os.Stderr, "forge control plane: interactive shells are not available.\nTry: ssh <host> help\n")
				os.Exit(protocol.ExitUsage)
			}
			code := sshd.Exec(cfg, st, user, key.Scope, cmdline, os.Stdin, os.Stdout, os.Stderr)
			st.Close()
			os.Exit(code)
			return nil
		},
	}
	cmd.Flags().Int64Var(&keyID, "key-id", 0, "registered key id (set by authorized-keys)")
	cmd.MarkFlagRequired("key-id")
	return cmd
}
