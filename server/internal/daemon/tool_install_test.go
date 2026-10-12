package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestToolTarballIntegrity(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "package.tgz")
	if err := os.WriteFile(file, []byte("substituted package"), 0o600); err != nil {
		t.Fatal(err)
	}
	if verifyToolTarball(file, reviewedToolReleases()[0].Integrity) == nil {
		t.Fatal("accepted substituted npm package")
	}
}

func TestToolInstallLockCanCancelAndReacquire(t *testing.T) {
	t.Parallel()
	filename := filepath.Join(t.TempDir(), "install.lock")
	unlock, err := lockToolInstallation(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if release, err := lockToolInstallation(ctx, filename); err == nil {
		release()
		t.Fatal("acquired an occupied lock")
	}
	unlock()
	unlock, err = lockToolInstallation(t.Context(), filename)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
