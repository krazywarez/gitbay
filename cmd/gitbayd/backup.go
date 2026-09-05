package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

// backupCmd produces one tar.gz holding a consistent database snapshot plus
// every repository and the SSH host keys. Restore by extracting the archive
// into a fresh server.root.
//
// Ordering: the database is snapshotted BEFORE the repositories are read.
// A push that lands mid-backup then shows up only as unreferenced git
// objects in the archive (harmless); the reverse order could leave database
// rows pointing at objects the archive never captured.
func backupCmd() *cobra.Command {
	var out, verify string
	var dbOnly bool
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "write a consistent backup archive (database snapshot first, then repositories)",
		Long: `Writes a tar.gz of the server root: a consistent SQLite snapshot,
all repositories, and the SSH host keys. Transient state (hook socket,
regenerated hook scripts, askpass helper, WAL files) is excluded.

--db-only writes the database snapshot alone. It is seconds and megabytes
rather than minutes and gigabytes, which is what makes a frequent schedule
affordable, and the database is the copy of issues, merge requests and
comments that exists nowhere else. Repositories are not in such an archive,
so it supplements a full backup and does not replace one.

Restore: extract into an empty directory, point server.root at it, start
gitbayd. Host keys are preserved, so clients keep their known_hosts entries.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if verify != "" {
				return verifyBackup(verify)
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			if out == "" {
				out = fmt.Sprintf("gitbay-backup-%s.tar.gz", time.Now().UTC().Format("20060102-150405"))
			}
			return runBackup(cfg, out, dbOnly)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output archive path (default gitbay-backup-<utc timestamp>.tar.gz)")
	cmd.Flags().BoolVar(&dbOnly, "db-only", false, "archive the database snapshot alone, without repositories")
	cmd.Flags().StringVar(&verify, "verify", "", "check an archive instead of writing one: database integrity, and its repositories against the archive's")
	return cmd
}

func runBackup(cfg config.Config, out string, dbOnly bool) error {
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	// 1. Consistent database snapshot, before any repository is read.
	snap := filepath.Join(os.TempDir(), fmt.Sprintf("gitbay-snap-%d.db", os.Getpid()))
	os.Remove(snap)
	defer os.Remove(snap)
	if err := snapshotDB(st, snap); err != nil {
		return fmt.Errorf("database snapshot: %w", err)
	}

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	if err := addFile(tw, snap, "gitbay.db"); err != nil {
		return err
	}

	// 2. Everything under the root except transient or regenerated state.
	// Skipped entirely for --db-only.
	skip := map[string]bool{
		"gitbay.db": true, "gitbay.db-wal": true, "gitbay.db-shm": true,
		"hook.sock": true, "askpass.sh": true, "hooks": true,
	}
	repoCount := 0
	root := cfg.Server.Root
	if !dbOnly {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			if top, _, _ := strings.Cut(rel, string(filepath.Separator)); skip[top] {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() && !d.IsDir() {
				return nil // sockets, symlinks
			}
			if d.IsDir() {
				if strings.HasSuffix(rel, ".git") {
					repoCount++
				}
				return nil // directories are implied by member paths
			}
			return addFile(tw, path, filepath.ToSlash(rel))
		})
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	info, _ := os.Stat(out)
	if dbOnly {
		fmt.Printf("wrote %s (database only, %.1f MB)\n", out, float64(info.Size())/1e6)
		return nil
	}
	fmt.Printf("wrote %s (%d repositories, %.1f MB)\n", out, repoCount, float64(info.Size())/1e6)
	return nil
}

// snapshotDB writes a consistent copy of the live database. VACUUM INTO
// takes a read snapshot, so concurrent daemon writes are safe under WAL.
func snapshotDB(st *store.Store, dest string) error {
	quoted := strings.ReplaceAll(dest, "'", "''")
	_, err := st.DB.Exec(fmt.Sprintf("VACUUM INTO '%s'", quoted))
	return err
}

func addFile(tw *tar.Writer, path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(tw, src)
	return err
}

// verifyBackup reads an archive back: the database snapshot must pass
// SQLite's integrity check, and every repository it names must be in the
// archive. A database-only archive is checked for integrity alone and
// says so. Nothing is written except a temporary copy of the database.
func verifyBackup(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s: not a gzip archive: %w", path, err)
	}
	tr := tar.NewReader(gz)
	tmp, err := os.MkdirTemp("", "gitbay-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dbPath := ""
	inArchive := map[string]bool{}
	members := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: archive damaged after %d members: %w", path, members, err)
		}
		members++
		switch {
		case h.Name == "gitbay.db":
			dbPath = filepath.Join(tmp, "gitbay.db")
			w, err := os.Create(dbPath)
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, tr); err != nil {
				w.Close()
				return fmt.Errorf("%s: extracting the database: %w", path, err)
			}
			w.Close()
		case strings.HasPrefix(h.Name, "repos/"):
			// repos/<owner>/<name>.git/HEAD marks one repository present.
			parts := strings.Split(h.Name, "/")
			if len(parts) == 4 && parts[3] == "HEAD" && strings.HasSuffix(parts[2], ".git") {
				inArchive[parts[1]+"/"+strings.TrimSuffix(parts[2], ".git")] = true
			}
		}
	}
	if dbPath == "" {
		return fmt.Errorf("%s: no gitbay.db in the archive", path)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("%s: database does not open: %w", path, err)
	}
	defer st.Close()
	var integrity string
	if err := st.DB.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("%s: integrity check: %w", path, err)
	}
	if integrity != "ok" {
		return fmt.Errorf("%s: database integrity: %s", path, integrity)
	}
	repos, err := st.ListAllRepos()
	if err != nil {
		return err
	}
	if len(inArchive) == 0 {
		fmt.Printf("%s: database only; integrity ok, %d repositories in the database, none in the archive\n", path, len(repos))
		return nil
	}
	var missing []string
	for _, r := range repos {
		if !inArchive[r.Path()] {
			missing = append(missing, r.Path())
		}
	}
	extra := len(inArchive) - (len(repos) - len(missing))
	fmt.Printf("%s: integrity ok, %d repositories in the database, %d in the archive\n", path, len(repos), len(inArchive))
	if len(missing) > 0 {
		return fmt.Errorf("%s: %d repositories the database names are not in the archive: %s", path, len(missing), strings.Join(missing, ", "))
	}
	if extra > 0 {
		fmt.Printf("%d repositories in the archive that the database does not name (deleted after the snapshot)\n", extra)
	}
	return nil
}
