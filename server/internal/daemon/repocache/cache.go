// Package repocache manages local bare Git caches.
package repocache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type GitRunner interface {
	// Run must not log env; it may contain a short-lived installation token.
	Run(ctx context.Context, dir string, env []string, args ...string) error
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, dir string, environment []string, args ...string) error {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), environment...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		// Git output and arguments are intentionally omitted: provider helpers
		// can echo credentials even when the remote itself is clean.
		return fmt.Errorf("git operation failed: %w", err)
	}
	return nil
}

type Repository struct {
	Key                       string
	RemoteURL                 string
	AuthenticationEnvironment []string
}

type Handle struct {
	Path      string
	RemoteURL string
}

type Manager struct {
	root   string
	runner GitRunner
	mu     sync.Mutex
	locks  map[string]*repositoryLock
}

type repositoryLock struct {
	mu   sync.Mutex
	refs int
}

func NewManager(root string, runner GitRunner) (*Manager, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) || repositoryVolumeRoot(root) {
		return nil, errors.New("bare repository cache root must be absolute")
	}
	root = filepath.Clean(root)
	if err := requireRealDirectory(root, true); err != nil {
		return nil, errors.New("bare repository cache root must be a real directory")
	}
	if runner == nil {
		runner = OSRunner{}
	}
	return &Manager{root: root, runner: runner, locks: make(map[string]*repositoryLock)}, nil
}

func repositoryVolumeRoot(path string) bool {
	volume := filepath.VolumeName(path)
	root := volume + string(filepath.Separator)
	return filepath.Clean(path) == filepath.Clean(root)
}

// Ensure serializes setup per repository so two claims cannot clone or fetch
// the same cache at once while independent repositories remain concurrent.
func (m *Manager) Ensure(ctx context.Context, repository Repository) (Handle, error) {
	key := strings.TrimSpace(repository.Key)
	if key == "" {
		return Handle{}, errors.New("repository cache key is required")
	}
	remote, err := validateRemote(repository.RemoteURL)
	if err != nil {
		return Handle{}, err
	}
	digest := sha256.Sum256([]byte(key))
	path := filepath.Join(m.root, hex.EncodeToString(digest[:16])+".git")
	environment := append([]string(nil), repository.AuthenticationEnvironment...)
	environment = append(environment, "GIT_TERMINAL_PROMPT=0")

	release := m.acquire(path)
	defer release()
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return Handle{}, fmt.Errorf("create bare repository cache root: %w", err)
	}
	if err := requireRealDirectory(m.root, false); err != nil {
		return Handle{}, errors.New("bare repository cache root is not a real directory")
	}
	info, statErr := os.Lstat(path)
	switch {
	case statErr == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0):
		return Handle{}, errors.New("bare repository cache path is not a real directory")
	case statErr == nil:
		if err := m.runner.Run(
			ctx,
			m.root,
			environment,
			"--git-dir",
			path,
			"remote",
			"set-url",
			"origin",
			remote,
		); err != nil {
			return Handle{}, fmt.Errorf("refresh bare repository remote: %w", err)
		}
		if err := m.runner.Run(
			ctx,
			m.root,
			environment,
			"--git-dir",
			path,
			"fetch",
			"--prune",
			"origin",
			"+refs/heads/*:refs/heads/*",
		); err != nil {
			return Handle{}, fmt.Errorf("fetch bare repository cache: %w", err)
		}
	case errors.Is(statErr, os.ErrNotExist):
		if err := m.runner.Run(ctx, m.root, environment, "clone", "--mirror", "--", remote, path); err != nil {
			_ = os.RemoveAll(path)
			return Handle{}, fmt.Errorf("clone bare repository cache: %w", err)
		}
		if err := requireRealDirectory(path, false); err != nil {
			_ = os.RemoveAll(path)
			return Handle{}, errors.New("cloned repository cache is not a real directory")
		}
	default:
		return Handle{}, fmt.Errorf("inspect bare repository cache: %w", statErr)
	}
	return Handle{Path: path, RemoteURL: remote}, nil
}

func requireRealDirectory(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a real directory")
	}
	return nil
}

func (m *Manager) acquire(path string) func() {
	m.mu.Lock()
	lock := m.locks[path]
	if lock == nil {
		lock = &repositoryLock{}
		m.locks[path] = lock
	}
	lock.refs++
	m.mu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		m.mu.Lock()
		lock.refs--
		if lock.refs == 0 && m.locks[path] == lock {
			delete(m.locks, path)
		}
		m.mu.Unlock()
	}
}

func validateRemote(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("repository remote URL is invalid")
	}
	if strings.HasPrefix(raw, "git@") {
		if strings.ContainsAny(raw, "?#") || !strings.Contains(raw, ":") || strings.Contains(raw, "..") {
			return "", errors.New("repository SSH remote is invalid")
		}
		return raw, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("repository remote must be an absolute credential-free HTTPS or SSH URL")
	}
	switch parsed.Scheme {
	case "https":
		if parsed.User != nil {
			return "", errors.New("repository remote must not contain credentials")
		}
	case "ssh":
		if parsed.User != nil {
			if _, password := parsed.User.Password(); password || parsed.User.Username() != "git" {
				return "", errors.New("repository remote must not contain credentials")
			}
		}
	default:
		return "", errors.New("repository remote scheme is unsupported")
	}
	return parsed.String(), nil
}
