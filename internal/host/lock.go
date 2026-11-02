package host

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var errLocked = errors.New("state directory is in use by another golem or its host")

// Lock takes the exclusive lock on <state>/lock, retrying every 100 ms until
// wait elapses. A host spawned with the returned file as an extra file shares
// the lock, so it holds the state until it exits even if this process dies.
func Lock(state string, wait time.Duration) (*os.File, error) {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(state, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, errLocked
		}
		time.Sleep(100 * time.Millisecond)
	}
}
