package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/backuplock"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// backupCmd produces one tar.gz holding a consistent database snapshot plus
// every repository and the SSH host keys. Restore by extracting the archive
// into a fresh server.root.
//
// Ordering: the database is snapshotted BEFORE the repositories are read,
// and each repository's refs before its objects. A push that lands
// mid-backup then shows up only as unreferenced git objects in the archive
// (harmless) or not at all; the reverse order could leave database rows or
// refs pointing at objects the archive never captured.
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

Restore: extract into an empty directory, point server.root at it,
restore server.secret_key_file from its own backup (mode 0600, owned by
the daemon user), start gitbayd. No archive carries the key file, and
without it gitbayd refuses to start. Host keys are preserved, so clients
keep their known_hosts entries.

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
	cmd.Flags().StringVar(&verify, "verify", "", "check an archive instead of writing one: database integrity, its repositories against the archive's, and git connectivity of each")
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

	dir := filepath.Dir(out)
	// An archive under the root would be in the next full backup's walk.
	if config.Within(cfg.Server.Root, dir) {
		return fmt.Errorf("%s is inside server.root %s; write the archive elsewhere", out, cfg.Server.Root)
	}
	removeStale(dir, time.Now().Add(-staleAge))

	// Deletes, renames and transfers wait until the walk finishes, so
	// every repository the snapshot names is still on disk when the walk
	// reaches it (#259). A database-only archive reads no repository.
	if !dbOnly {
		release, err := backuplock.Hold(cfg.Server.Root)
		if err != nil {
			return fmt.Errorf("backup lock: %w", err)
		}
		defer release()
	}

	// VACUUM INTO copies sealed values as they are, so the backup needs
	// no key file, and it migrates nothing. store.Open would create a
	// missing database, so its absence is checked first.
	dbFile := filepath.Join(cfg.Server.Root, "gitbay.db")
	if _, err := os.Stat(dbFile); err != nil {
		return fmt.Errorf("database: %w", err)
	}
	st, err := store.Open(dbFile)
	if err != nil {
		return err
	}
	defer st.Close()

	// 1. Consistent database snapshot, before any repository is read. It
	// goes in a fresh 0700 directory beside the archive.
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
		backuplock.Name: true,
	}
	repoCount := 0
	root := cfg.Server.Root
	if !dbOnly {
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if walkErr != nil {
				if vanished(walkErr, rel) {
					return nil
				}
				return walkErr
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
			// A repository's refs were archived on entering it.
			if strings.HasSuffix(filepath.Dir(rel), ".git") && refNames[d.Name()] {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				// A directory entry, even for one that holds no file (a
				// bare repository's refs/heads and refs/tags once every
				// ref is packed), so extraction recreates it: git's own
				// repository discovery needs refs/ to exist.
				if err := addDir(tw, path, filepath.ToSlash(rel)); err != nil {
					if vanished(err, rel) {
						return filepath.SkipDir
					}
					return err
				}
				if strings.HasSuffix(rel, ".git") {
					repoCount++
					if err := addRefs(tw, path, filepath.ToSlash(rel)); err != nil {
						return err
					}
					afterRefs(path)
				}
				return nil
			}
			beforeAdd(path)
			if err := addFile(tw, path, filepath.ToSlash(rel)); !vanished(err, rel) {
				return err
			}
			return nil
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

// staleAge is how old a snapshot directory or temporary archive must be
// before a later run removes it. A run that is still writing one is
// younger than this.
const staleAge = 24 * time.Hour

// removeStale removes what a killed run left in dir: snapshot
// directories and temporary archives last modified before cutoff.
func removeStale(dir string, cutoff time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		snap := e.IsDir() && strings.HasPrefix(name, ".gitbay-snap-")
		tmp := e.Type().IsRegular() && strings.HasPrefix(name, ".") && strings.Contains(name, ".tmp-")
		if !snap && !tmp {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		p := filepath.Join(dir, name)
		if err := os.RemoveAll(p); err != nil {
			fmt.Fprintf(os.Stderr, "removing stale %s: %v\n", p, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "removed stale %s\n", p)
	}
}

// refNames are what a repository's refs are read from. WalkDir would
// reach objects/ before packed-refs and refs/, so a push landing mid-walk
// could leave an archived ref naming objects the archive lacks. addRefs
// archives these first on entering the repository; objects are only ever
// added, so the walk that follows finds every object those refs reach.
var refNames = map[string]bool{"HEAD": true, "packed-refs": true, "refs": true}

// afterRefs runs between a repository's refs and the rest of it. Tests
// use it to write into the repository at that point.
var afterRefs = func(repo string) {}

// addRefs archives HEAD, packed-refs and refs/ of the repository at
// path, whichever exist.
func addRefs(tw *tar.Writer, path, name string) error {
	for _, f := range []string{"HEAD", "packed-refs"} {
		fi, err := os.Lstat(filepath.Join(path, f))
		if errors.Is(err, fs.ErrNotExist) || err == nil && !fi.Mode().IsRegular() {
			continue
		}
		if err != nil {
			return err
		}
		if err := addFile(tw, filepath.Join(path, f), name+"/"+f); err != nil {
			return err
		}
	}
	refs := filepath.Join(path, "refs")
	if _, err := os.Lstat(refs); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(refs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		member := name + "/" + filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			return addDir(tw, p, member)
		case d.Type().IsRegular():
			return addFile(tw, p, member)
		}
		return nil
	})
}

// beforeAdd runs before each file the walk archives outside refs. Tests
// use it to remove a file between listing and reading.
var beforeAdd = func(path string) {}

// vanished reports a file or directory under a repository's objects/
// that went between the walk listing it and reading it: a pack or loose
// object a concurrent gc or receive.autogc removed. The walk skips it.
// Refs archived earlier reach only objects that are still reachable, and
// a repack writes those into a new pack before removing the old one; if
// one is lost regardless, verify's fsck reports it.
func vanished(err error, rel string) bool {
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := 0; i+2 < len(parts); i++ {
		if strings.HasSuffix(parts[i], ".git") && parts[i+1] == "objects" {
			return true
		}
	}
	return false
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

// addFile opens before writing the header, so a file removed after the
// walk listed it fails before the archive has a member for it.
func addFile(tw *tar.Writer, path, name string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
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
	_, err = io.CopyN(tw, src, hdr.Size)
	return err
}

// addDir writes a directory entry, so an empty directory survives
// extraction. The mode never exceeds 0755, whatever the source directory
// carries.
func addDir(tw *tar.Writer, path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = name + "/"
	hdr.Mode = hdr.Mode&^0o777 | hdr.Mode&0o755
	return tw.WriteHeader(hdr)
}

// verifyBackup reads an archive back, decrypting it with identity when it
// is encrypted: the database snapshot must pass SQLite's integrity check,
// every repository it names must be in the archive, and each of those
// must pass git fsck --connectivity-only. A database-only archive is
// checked for integrity alone and says so. Repositories are extracted to
// a temporary directory for the check, so it needs free space for them.
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
			if err := extractTo(tr, dbPath); err != nil {
				return fmt.Errorf("%s: extracting the database: %w", path, err)
			}
		case strings.HasPrefix(h.Name, "repos/"):
			trimmed := strings.TrimSuffix(h.Name, "/")
			// repos/<owner>/<name>.git/HEAD marks one repository present.
			parts := strings.Split(trimmed, "/")
			if len(parts) == 4 && parts[3] == "HEAD" && strings.HasSuffix(parts[2], ".git") {
				inArchive[parts[1]+"/"+strings.TrimSuffix(parts[2], ".git")] = true
			}
			if !filepath.IsLocal(trimmed) {
				return fmt.Errorf("%s: member %q leaves the archive root", path, h.Name)
			}
			dest := filepath.Join(tmp, filepath.FromSlash(trimmed))
			switch h.Typeflag {
			case tar.TypeDir:
				// The archive's directory modes do not matter to fsck, and
				// a hostile one would stop RemoveAll cleaning up.
				if err := os.MkdirAll(dest, 0o700); err != nil {
					return fmt.Errorf("%s: creating %s: %w", path, h.Name, err)
				}
			case tar.TypeReg:
				if err := extractTo(tr, dest); err != nil {
					return fmt.Errorf("%s: extracting %s: %w", path, h.Name, err)
				}
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
		fmt.Printf("%d repositories in the archive that the database does not name (created after the snapshot)\n", extra)
	}
	var broken []string
	for _, r := range repos {
		dir := filepath.Join(tmp, "repos", r.OwnerName, r.Name+".git")
		if err := gitutil.FsckConnectivity(dir); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", r.Path(), err)
			broken = append(broken, r.Path())
		}
	}
	if len(broken) > 0 {
		return fmt.Errorf("%s: %d repositories fail the connectivity check: %s", path, len(broken), strings.Join(broken, ", "))
	}
	fmt.Printf("connectivity ok on %d repositories\n", len(repos))
	return nil
}

// extractTo writes one archive member to dest, owner-only.
func extractTo(r io.Reader, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	w, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
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
