package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalDirectoryManagerCanonicalizesAndLocksExclusively(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "project")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manager := NewLocalDirectoryManager()
	first, err := manager.Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path() != directory {
		t.Fatalf("canonical path = %q, want %q", first.Path(), directory)
	}
	if _, err := manager.Acquire(directory); !errors.Is(err, ErrLocalDirectoryBusy) {
		t.Fatalf("second acquisition = %v, want ErrLocalDirectoryBusy", err)
	}
	first.Release()
	first.Release()
	second, err := manager.Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
}

func TestValidateLocalDirectoryRejectsRelativeTraversalAndSymlinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative/path", string(filepath.Separator), target + string(filepath.Separator) + ".." + string(filepath.Separator) + "target", link} {
		if _, err := ValidateLocalDirectory(path); err == nil {
			t.Fatalf("path %q was accepted", path)
		}
	}
	if got, err := ValidateLocalDirectory(target); err != nil || got != target {
		t.Fatalf("valid path = (%q, %v)", got, err)
	}
}
