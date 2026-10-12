package repocache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedGitCall struct {
	dir  string
	env  []string
	args []string
}

type recordingGitRunner struct {
	mu    sync.Mutex
	calls []recordedGitCall
}

func (r *recordingGitRunner) Run(_ context.Context, dir string, env []string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(
		r.calls,
		recordedGitCall{dir: dir, env: append([]string(nil), env...), args: append([]string(nil), args...)},
	)
	if len(args) >= 4 && args[0] == "clone" && args[1] == "--mirror" {
		return os.MkdirAll(args[len(args)-1], 0o700)
	}
	return nil
}

func TestManagerClonesThenFetchesBareCacheWithoutCredentialArguments(t *testing.T) {
	runner := &recordingGitRunner{}
	manager, err := NewManager(filepath.Join(t.TempDir(), "cache"), runner)
	if err != nil {
		t.Fatal(err)
	}
	repository := Repository{
		Key: "github:installation-1:repository-2", RemoteURL: "https://github.com/acme/boardly.git",
		AuthenticationEnvironment: []string{"GIT_ASKPASS=/private/helper", "GITHUB_TOKEN=short-lived-secret"},
	}
	first, err := manager.Ensure(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Ensure(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != second.Path || filepath.Ext(first.Path) != ".git" {
		t.Fatalf("cache paths = %q/%q", first.Path, second.Path)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("git calls = %d, want clone + set-url + fetch", len(runner.calls))
	}
	allArgs := make([]string, 0)
	for _, call := range runner.calls {
		allArgs = append(allArgs, call.args...)
	}
	if strings.Contains(strings.Join(allArgs, " "), "short-lived-secret") {
		t.Fatalf("credential appeared in git arguments: %q", allArgs)
	}
	if got := runner.calls[0].env; !contains(got, "GITHUB_TOKEN=short-lived-secret") ||
		!contains(got, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("clone environment = %q", got)
	}
}

func TestManagerRejectsCredentialBearingOrAmbiguousRemote(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "cache"), &recordingGitRunner{})
	if err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{
		"https://secret@github.com/acme/repo.git",
		"https://github.com/acme/repo.git?token=secret",
		"file:///private/repo",
		"../repo.git",
	} {
		if _, err := manager.Ensure(
			context.Background(),
			Repository{Key: "repo", RemoteURL: remote},
		); err == nil {
			t.Fatalf("remote %q was accepted", remote)
		}
	}
}

func TestManagerRejectsFilesystemRootAsCache(t *testing.T) {
	if _, err := NewManager(string(filepath.Separator), &recordingGitRunner{}); err == nil {
		t.Fatal("filesystem root was accepted as repository cache")
	}
}

func TestManagerRejectsExistingNonRealCacheRootAtConstruction(t *testing.T) {
	parent := t.TempDir()
	realDirectory := filepath.Join(parent, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(parent, "symlink")
	if err := os.Symlink(realDirectory, symlink); err != nil {
		t.Fatal(err)
	}
	regularFile := filepath.Join(parent, "file")
	if err := os.WriteFile(regularFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{symlink, regularFile} {
		if _, err := NewManager(root, &recordingGitRunner{}); err == nil {
			t.Fatalf("non-real repository cache root %q was accepted", root)
		}
	}
}

func TestManagerRejectsSymlinkCacheRootWithoutWritingThroughIt(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "cache")
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &recordingGitRunner{}
	manager, err := NewManager(root, runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Ensure(context.Background(), Repository{
		Key: "repo", RemoteURL: "https://github.com/acme/repo.git",
	}); err == nil {
		t.Fatal("symlink repository cache root was accepted")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("git calls = %d, want none", len(runner.calls))
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target was mutated: %v", entries)
	}
}

func TestManagerRejectsSymlinkRepositoryEntryWithoutCallingGit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	outside := filepath.Join(t.TempDir(), "outside.git")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := Repository{Key: "repo", RemoteURL: "https://github.com/acme/repo.git"}
	path := cachePathForTest(root, repository.Key)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	runner := &recordingGitRunner{}
	manager, err := NewManager(root, runner)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Ensure(context.Background(), repository); err == nil {
		t.Fatal("symlink repository cache entry was accepted")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("git calls = %d, want none", len(runner.calls))
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("repository symlink was unexpectedly replaced")
	}
}

type blockingGitRunner struct {
	entered chan struct{}
	release <-chan struct{}
}

func (r *blockingGitRunner) Run(_ context.Context, _ string, _ []string, args ...string) error {
	if len(args) >= 4 && args[0] == "clone" {
		r.entered <- struct{}{}
		<-r.release
		return os.MkdirAll(args[len(args)-1], 0o700)
	}
	return nil
}

func TestManagerSerializesSameRepositoryWithoutBlockingDifferentRepositories(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	manager, err := NewManager(
		filepath.Join(t.TempDir(), "cache"),
		&blockingGitRunner{entered: entered, release: release},
	)
	if err != nil {
		t.Fatal(err)
	}
	errors := make(chan error, 2)
	for _, key := range []string{"repo-a", "repo-b"} {
		go func() {
			_, err := manager.Ensure(
				context.Background(),
				Repository{Key: key, RemoteURL: "https://github.com/acme/" + key + ".git"},
			)
			errors <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("a different repository was blocked by an in-progress clone")
		}
	}
	close(release)
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cachePathForTest(root, key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(root, hex.EncodeToString(digest[:16])+".git")
}
