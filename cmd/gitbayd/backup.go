package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
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
	var out, verify, identity string
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
gitbayd. Host keys are preserved, so clients keep their known_hosts entries.

With [backup] age_recipients set, the archive is encrypted to those age
public keys and its name ends in .age. --verify then needs --identity
<file> holding a matching private key, which is kept off the host.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if verify != "" {
				return verifyBackup(verify, identity)
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			return runBackup(cfg, archivePath(out, cfg, time.Now()), dbOnly)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output archive path (default gitbay-backup-<utc timestamp>.tar.gz; .age is appended when [backup] age_recipients is set)")
	cmd.Flags().BoolVar(&dbOnly, "db-only", false, "archive the database snapshot alone, without repositories")
	cmd.Flags().StringVar(&verify, "verify", "", "check an archive instead of writing one: database integrity, and its repositories against the archive's")
	cmd.Flags().StringVar(&identity, "identity", "", "with --verify: an age identity file that opens an encrypted archive")
	return cmd
}

// archivePath is where the archive goes: out, or a timestamped name,
// ending in .age when the archive is encrypted.
func archivePath(out string, cfg config.Config, now time.Time) string {
	if out == "" {
		out = fmt.Sprintf("gitbay-backup-%s.tar.gz", now.UTC().Format("20060102-150405"))
	}
	if len(cfg.Backup.AgeRecipients) > 0 && !strings.HasSuffix(out, ".age") {
		out += ".age"
	}
	return out
}

func runBackup(cfg config.Config, out string, dbOnly bool) error {
	var rs []age.Recipient
	if len(cfg.Backup.AgeRecipients) > 0 {
		var err error
		if rs, err = cfg.Backup.Recipients(); err != nil {
			return err
		}
	} else if strings.HasSuffix(out, ".age") {
		return fmt.Errorf("%s ends in .age but [backup] age_recipients is not set, so the archive would not be encrypted", out)
	}

	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	// 1. Consistent database snapshot, before any repository is read. It
	// goes in a fresh 0700 directory beside the archive.
	dir := filepath.Dir(out)
	snapDir, err := os.MkdirTemp(dir, ".gitbay-snap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(snapDir)
	snap := filepath.Join(snapDir, "gitbay.db")
	if err := snapshotDB(st, snap); err != nil {
		return fmt.Errorf("database snapshot: %w", err)
	}

	// The archive is written to a temporary name beside out and renamed
	// once complete, so a failed run leaves no partial archive behind.
	f, err := os.CreateTemp(dir, "."+filepath.Base(out)+".tmp-")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	var sink io.Writer = f
	var enc io.WriteCloser
	if len(rs) > 0 {
		if enc, err = age.Encrypt(f, rs...); err != nil {
			return err
		}
		sink = enc
	}
	gz := gzip.NewWriter(sink)
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
	if enc != nil {
		if err := enc.Close(); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), out); err != nil {
		return err
	}
	done = true
	if err := syncDir(dir); err != nil {
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

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
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

// verifyBackup reads an archive back, decrypting it with identity when it
// is encrypted: the database snapshot must pass
// SQLite's integrity check, and every repository it names must be in the
// archive. A database-only archive is checked for integrity alone and
// says so. Nothing is written except a temporary copy of the database.
func verifyBackup(path, identity string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	plain, err := archiveReader(f, path, identity)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(plain)
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
	// Read to the end so gzip checks its trailer and age its final chunk.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return fmt.Errorf("%s: archive truncated or damaged: %w", path, err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("%s: archive truncated or damaged: %w", path, err)
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

const ageHeader = "age-encryption.org/v1\n"

// archiveReader returns the archive's gzip stream, decrypting it first
// when it is an age file.
func archiveReader(f io.Reader, path, identity string) (io.Reader, error) {
	br := bufio.NewReader(f)
	head, _ := br.Peek(len(ageHeader))
	if string(head) != ageHeader {
		if identity != "" {
			fmt.Fprintf(os.Stderr, "%s is not encrypted; --identity was not used\n", path)
		}
		return br, nil
	}
	if identity == "" {
		return nil, fmt.Errorf("%s is encrypted; pass --identity <file> with the private key for one of its recipients", path)
	}
	idf, err := os.Open(identity)
	if err != nil {
		return nil, err
	}
	defer idf.Close()
	ids, err := age.ParseIdentities(idf)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", identity, err)
	}
	r, err := age.Decrypt(br, ids...)
	if err != nil {
		return nil, fmt.Errorf("%s: decrypting: %w", path, err)
	}
	return r, nil
}
