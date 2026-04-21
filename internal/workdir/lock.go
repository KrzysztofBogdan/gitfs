package workdir

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// Lock is an advisory exclusive lock on .gitfs/lock (spec §6 / pull design §6).
// Callers acquire via AcquireLock and release by calling Release.
type Lock struct {
	f *os.File
}

// AcquireLock opens <root>/.gitfs/lock and takes an exclusive advisory
// flock. If another process holds it, retries briefly (up to timeout)
// then returns an error. A nil timeout disables retry.
func AcquireLock(root string, timeout time.Duration) (*Lock, error) {
	l := Layout{Root: root}
	lockPath := l.Gitfs() + "/lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, fmt.Errorf("lock: %w", err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("workdir is locked by another process")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Release releases the flock and closes the file.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil
	return err
}
