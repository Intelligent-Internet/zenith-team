//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package workspace

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryAdvisoryFileLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return false, err
}

func unlockAdvisoryFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
