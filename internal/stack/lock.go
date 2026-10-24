package stack

import (
	"fmt"
	"os"

	"github.com/mhvelplund/git-stack/internal/exitcode"
)

// Lock serializes mutations across all worktrees of the clone with an OS file
// lock (flock, or LockFileEx on Windows), so a lock held by a process that
// died is released by the OS. It does not wait for a busy lock.
//
// The lock file is left on disk on unlock: removing it would let two
// processes lock different inodes of the same path (see gh-stack's FileLock).
func (st Store) Lock() (unlock func(), err error) {
	p := st.lockPath()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	if err := tryLockFile(f); err != nil {
		f.Close()
		if isLockBusy(err) {
			return nil, exitcode.New(exitcode.Locked, "stack is locked by another git-stack process")
		}
		return nil, fmt.Errorf("locking %s: %w", p, err)
	}
	return func() {
		unlockFile(f)
		f.Close()
	}, nil
}
