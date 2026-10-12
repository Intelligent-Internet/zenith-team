package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var ErrLocalDirectoryBusy = errors.New("local directory is already executing a Run")

func ValidateLocalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", errors.New("local directory path must be absolute")
	}
	clean := filepath.Clean(path)
	if clean != path {
		return "", errors.New("local directory path must already be canonical")
	}
	if volumeRoot(clean) {
		return "", errors.New("local directory must not be a filesystem root")
	}
	canonical, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("resolve local directory: %w", err)
	}
	if canonical != clean {
		return "", errors.New("local directory path must not contain symbolic links")
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect local directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("local directory path is not a real directory")
	}
	return canonical, nil
}

func volumeRoot(path string) bool {
	volume := filepath.VolumeName(path)
	root := volume + string(filepath.Separator)
	return filepath.Clean(path) == filepath.Clean(root)
}

type LocalDirectoryManager struct {
	mu     sync.Mutex
	active map[string]struct{}
}

func NewLocalDirectoryManager() *LocalDirectoryManager {
	return &LocalDirectoryManager{active: make(map[string]struct{})}
}

type LocalDirectoryLease struct {
	once    sync.Once
	path    string
	release func()
}

func (l *LocalDirectoryLease) Path() string { return l.path }

func (l *LocalDirectoryLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(l.release)
}

func (m *LocalDirectoryManager) Acquire(path string) (*LocalDirectoryLease, error) {
	canonical, err := ValidateLocalDirectory(path)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.active[canonical]; exists {
		return nil, ErrLocalDirectoryBusy
	}
	m.active[canonical] = struct{}{}
	return &LocalDirectoryLease{path: canonical, release: func() {
		m.mu.Lock()
		delete(m.active, canonical)
		m.mu.Unlock()
	}}, nil
}
