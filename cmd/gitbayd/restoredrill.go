package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/store"
)

// restoreDrillCmd rehearses the archive half of a restore: extract a
// full archive into an empty directory, run verify's checks on what was
// extracted, and report the elapsed time and the newest activity the
// restored database holds.
func restoreDrillCmd() *cobra.Command {
	var into, identity string
	cmd := &cobra.Command{
		Use:   "restore-drill <archive> --into <dir>",
		Short: "restore a full archive into an empty directory, verify it, report elapsed time and the newest recovered activity",
		Long: `Extracts every member of a full backup archive into --into, which must
be empty or absent, and runs the checks of backup --verify on the
extracted copy: database integrity, every repository present and
passing git fsck --connectivity-only, release assets and LFS object
digests. It then prints the newest issue, comment and push in the
restored database, which is the recovery point, and the elapsed time.

The result is a server.root a gitbayd can be pointed at. The archive
does not carry server.secret_key_file or config.toml; the Admin wiki's
Restore drill section covers those and the offsite path.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if into == "" {
				return errors.New("--into <dir> is required")
			}
			return restoreDrill(args[0], identity, into)
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "empty or absent directory to restore into")
	cmd.Flags().StringVar(&identity, "identity", "", "an age identity file that opens an encrypted archive")
	return cmd
}

func restoreDrill(archive, identity, into string) error {
	start := time.Now()
	ents, err := os.ReadDir(into)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(into, 0o700); err != nil {
			return err
		}
	case err != nil:
		return err
	case len(ents) > 0:
		return fmt.Errorf("%s is not empty; restore into an empty or absent directory", into)
	}
	checkErr := checkArchive(archive, identity, into, true)
	if ents, err := os.ReadDir(into); err == nil {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		fmt.Printf("restored into %s: %s\n", into, strings.Join(names, " "))
	}
	// store.Open would create a missing database.
	db := filepath.Join(into, "gitbay.db")
	if _, err := os.Stat(db); err == nil {
		if err := printNewest(db); err != nil {
			checkErr = errors.Join(checkErr, err)
		}
	}
	fmt.Printf("elapsed %s\n", time.Since(start).Round(100*time.Millisecond))
	return checkErr
}

// newest are the recovered timestamps a drill records.
var newest = []struct{ label, query string }{
	{"issue", "SELECT MAX(created_at) FROM issues"},
	{"issue comment", "SELECT MAX(created_at) FROM issue_comments"},
	{"merge request comment", "SELECT MAX(created_at) FROM mr_comments"},
	{"push", "SELECT MAX(created_at) FROM events WHERE kind = 'push'"},
}

func printNewest(db string) error {
	st, err := store.Open(db)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, n := range newest {
		var at *string
		if err := st.DB.QueryRow(n.query).Scan(&at); err != nil {
			return fmt.Errorf("newest %s: %w", n.label, err)
		}
		v := "none"
		if at != nil {
			v = *at
		}
		fmt.Printf("newest %s: %s\n", n.label, v)
	}
	return nil
}
