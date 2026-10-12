//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package workspace

import (
	"errors"
	"os"
)

var errAdvisoryFileLockUnsupported = errors.New("advisory file locks are unsupported on this platform")

func tryAdvisoryFileLock(*os.File) (bool, error) {
	return false, errAdvisoryFileLockUnsupported
}

func unlockAdvisoryFile(*os.File) error {
	return nil
}
