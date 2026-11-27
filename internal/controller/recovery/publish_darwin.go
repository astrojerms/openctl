package recovery

import "golang.org/x/sys/unix"

// publish atomically refuses a destination created after preflight.
func publish(source, destination string) error {
	return unix.RenamexNp(source, destination, unix.RENAME_EXCL)
}
