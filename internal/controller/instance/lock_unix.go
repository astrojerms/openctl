//go:build linux || darwin

// Package instance serializes controller and offline recovery access to state.
package instance

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// Filename is intentionally retained after Close: unlinking a flock file permits
// a second inode to be locked while another process still holds the original.
const Filename = ".controller.lock"

// Lock holds a kernel-managed exclusive lock until Close or process exit.
type Lock struct {
	mu   sync.Mutex
	file *os.File
}

// Acquire takes a nonblocking exclusive lock in an existing state directory.
// No PID files or process-liveness heuristics are used.
func Acquire(stateDir string) (*Lock, error) {
	fd, err := unix.Open(filepath.Join(stateDir, Filename), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open controller lock: %w", err)
	}
	f := os.NewFile(uintptr(fd), Filename)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("controller lock must be a regular file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("controller state is in use (stop the controller before recovery): %w", err)
	}
	return &Lock{file: f}, nil
}

// Close releases the lock. It is safe to call more than once.
func (l *Lock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
