// Package backuplock keeps repository deletes, renames, transfers and
// prunes out of a full backup's way (#259). The backup runs in its own
// process (gitbayd admin backup) and a delete in the daemon's, so the
// lock is flock(2) on a file under server.root: the backup holds it
// exclusively from its database snapshot until the last repository is
// archived, and each delete, move or prune holds it shared while it runs.
package backuplock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Name is the lock file under server.root. Backups skip it.
const Name = "backup.lock"

// ErrBusy is TryShared's answer while a backup holds the lock.
var ErrBusy = errors.New("a backup is running; repositories cannot be deleted, renamed, moved or pruned until it finishes, usually within minutes")

// open opens the lock file read-only, which is all flock needs, so the
// daemon's user can lock a file a root-run backup created. O_NOFOLLOW
// refuses a symlink planted at Name instead of following it, since the
// path is inside server.root where only the daemon's own user writes.
func open(root string) (*os.File, error) {
	return os.OpenFile(filepath.Join(root, Name), os.O_RDONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
}

// Hold takes the lock exclusively, waiting for deletes and moves under
// way to finish. Closing the file releases it.
func Hold(root string) (func(), error) {
	f, err := open(root)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// TryShared takes the lock shared without waiting: ErrBusy while a
// backup holds it.
func TryShared(root string) (func(), error) {
	f, err := open(root)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
