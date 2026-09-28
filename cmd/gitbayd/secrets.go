package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/seal"
)

// secretsCmd manages the key file that seals CI secrets, webhook
// secrets, mirror tokens and push device tokens in the database. No
// subcommand prints key material, only key ids.
func secretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "the key file that seals secrets stored in the database",
	}
	run := func(f func(config.Config, io.Writer) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			return f(cfg, os.Stdout)
		}
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "init",
			Short: "create the key file (server.secret_key_file) with one new key",
			Long: `Creates server.secret_key_file, mode 0600, holding one new key. Run as
root, the file is given to the owner of server.root, the daemon's user.
Refuses when the file exists.`,
			RunE: run(initSecrets),
		},
		&cobra.Command{
			Use:   "rotate",
			Short: "seal every secret under a new key and retire the old ones",
			Long: `Adds a new key to the key file, reseals every value under it in one
transaction, then removes the old keys from the file. A running daemon
re-reads the file when it changes, so no restart is needed. Run as the
user that can replace the key file (root, for /etc/gitbay); the file
keeps its owner. Copy the new file off the host afterwards.`,
			RunE: run(rotateSecrets),
		},
		&cobra.Command{
			Use:   "check",
			Short: "open every stored secret and count them per column by key; exit 1 if any does not open",
			RunE:  run(checkSecrets),
		},
	)
	return cmd
}

// initSecrets writes a new key file. Run as root, it hands the file to
// the owner of server.root, since the daemon reads it as that user.
func initSecrets(cfg config.Config, w io.Writer) error {
	path := cfg.Server.SecretKeyFile
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists; gitbayd admin secrets rotate replaces its key", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	uid, gid := -1, -1
	if os.Geteuid() == 0 {
		fi, err := os.Stat(cfg.Server.Root)
		if err != nil {
			return fmt.Errorf("the key file is given to the owner of server.root: %w", err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot read the owner of %s", cfg.Server.Root)
		}
		uid, gid = int(st.Uid), int(st.Gid)
	}
	k, err := seal.NewKey()
	if err != nil {
		return err
	}
	if err := seal.WriteKeys(path, []seal.Key{k}); err != nil {
		return err
	}
	if uid >= 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			// A root-owned file left behind would make a re-run refuse.
			os.Remove(path)
			return fmt.Errorf("could not give %s to the owner of %s, so it was removed: %w", path, cfg.Server.Root, err)
		}
	}
	fmt.Fprintf(w, "wrote %s (key %s). Copy it off this host: backups do not carry it, and a restored database's secrets do not open without it.\n", path, k.ID)
	return nil
}

// rotateSecrets adds a key, reseals under it, then drops the old keys.
// Each step leaves a file that opens every stored value: after the first
// write the file holds old and new keys; the reseal is one transaction;
// the last write happens only after the reseal committed and every value
// is confirmed under the new key. Interrupted anywhere, running it again
// finishes the job.
func rotateSecrets(cfg config.Config, w io.Writer) error {
	path := cfg.Server.SecretKeyFile
	old, err := seal.ReadKeys(path)
	if err != nil {
		return err
	}
	next, err := seal.NewKey()
	if err != nil {
		return err
	}
	if err := seal.WriteKeys(path, append(old, next)); err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	keep := fmt.Sprintf("the key file holds the old keys and %s; run rotate again", next.ID)
	n, err := st.ResealSecrets()
	if err != nil {
		return fmt.Errorf("resealing: %w (%s)", err, keep)
	}
	// Guards against a value sealed outside the reseal transaction under
	// an old key; no test reaches it, since that needs a hook between the
	// two calls.
	use, err := st.SecretKeyUse()
	if err != nil {
		return fmt.Errorf("checking the reseal: %w (%s)", err, keep)
	}
	for id, c := range use {
		if id != next.ID {
			return fmt.Errorf("%d values are not under %s after the reseal (%s)", c, next.ID, keep)
		}
	}
	if err := seal.WriteKeys(path, []seal.Key{next}); err != nil {
		return err
	}
	retired := make([]string, len(old))
	for i, k := range old {
		retired[i] = k.ID
	}
	fmt.Fprintf(w, "key %s: resealed %d values; retired %s. Copy %s off this host.\n", next.ID, n, strings.Join(retired, ", "), path)
	return nil
}

// checkSecrets opens every stored secret and prints, per column, how
// many values each key sealed and every value that does not open. Any
// such value is an error.
func checkSecrets(cfg config.Config, w io.Writer) error {
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	report, err := st.SecretReport()
	if err != nil {
		return err
	}
	failed := 0
	for _, u := range report {
		ids := make([]string, 0, len(u.ByKey))
		for id := range u.ByKey {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var parts []string
		for _, id := range ids {
			if id == "" {
				parts = append(parts, fmt.Sprintf("clear %d (sealed when the daemon next starts)", u.ByKey[id]))
			} else {
				parts = append(parts, fmt.Sprintf("key %s %d", id, u.ByKey[id]))
			}
		}
		if len(parts) == 0 {
			parts = []string{"none"}
		}
		fmt.Fprintf(w, "%s: %s\n", u.Column, strings.Join(parts, ", "))
		for _, f := range u.Failed {
			fmt.Fprintf(w, "%s row %d: %s\n", u.Column, f.RowID, f.Err)
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%s does not open %d stored values", cfg.Server.SecretKeyFile, failed)
	}
	return nil
}
