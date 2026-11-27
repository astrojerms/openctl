package recovery

import "golang.org/x/sys/unix"

// publish atomically refuses a destination created after preflight.
func publish(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}
