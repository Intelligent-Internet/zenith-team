package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// runGit runs one git command without a shell, in dir when dir is set. The
// caller's own configuration and credential helpers apply; git never prompts,
// pathspecs are literal, and read-only commands such as status never take the
// index lock.
func runGit(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_LITERAL_PATHSPECS=1", "GIT_OPTIONAL_LOCKS=0")
	command.Stdin = stdin
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return output, &gitError{args: args, err: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return output, nil
}

type gitError struct {
	args   []string
	err    error
	stderr string
}

// Error names the command and what git printed, with any credential in a URL
// argument left out.
func (err *gitError) Error() string {
	args := make([]string, len(err.args))
	stderr := err.stderr
	for index, arg := range err.args {
		args[index] = redactURL(arg)
		if args[index] != arg {
			stderr = strings.ReplaceAll(stderr, arg, args[index])
		}
	}
	message := fmt.Sprintf("git %s: %v", strings.Join(args, " "), err.err)
	if stderr != "" {
		message += ": " + stderr
	}
	return message
}

func (err *gitError) Unwrap() error { return err.err }

// urlUserinfo is "scheme://userinfo@" at the start of a URL, matched without
// parsing, so a URL url.Parse rejects (a stray % escape) is still redacted.
var urlUserinfo = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://)[^/?#@]*@`)

// redactURL drops the user information from a URL, so a token embedded in a
// clone URL never reaches a log. Any other argument is returned as it is.
func redactURL(arg string) string {
	parsed, err := url.Parse(arg)
	if err != nil {
		return urlUserinfo.ReplaceAllString(arg, "$1")
	}
	if parsed.User == nil || parsed.Host == "" {
		return arg
	}
	parsed.User = nil
	return parsed.String()
}

// exitedWith reports whether err is git exiting with exactly this status.
func exitedWith(err error, status int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == status
}

// gitRepository is a zenith-team-plugins repository bundlectl reads objects from.
// It never checks out, so no attribute or eol conversion touches the bytes.
type gitRepository struct {
	dir string
	// mainRef is the main branch a release tag must be reachable from.
	mainRef string
	// remote is the remote a local checkout's release tags must match, or
	// empty when there is none to match, as in a clone, whose tags came from
	// its source.
	remote string
	// clone marks a partial clone bundlectl made, whose blobs arrive on demand.
	clone bool
}

// openRepository reads a local checkout (a directory with .git) in place, with
// read-only git commands, or clones any other source, a URL or a bare
// repository, into a temporary directory. The caller's credentials apply to
// the clone; bundlectl never handles a token.
//
// A local checkout can hold commits and tags it never pushed, so when it has
// an origin remote, a release must be on origin's main as last fetched and its
// tag must be the one origin publishes. Without one, only its own main is
// checked, and a warning says so.
func openRepository(ctx context.Context, source string, stderr io.Writer) (*gitRepository, func(), error) {
	if _, err := os.Stat(filepath.Join(source, ".git")); err == nil {
		dir, err := filepath.Abs(source)
		if err != nil {
			return nil, nil, err
		}
		repo := &gitRepository{dir: dir, mainRef: "refs/heads/main"}
		if _, err := runGit(ctx, dir, nil, "remote", "get-url", "origin"); err == nil {
			repo.mainRef, repo.remote = "refs/remotes/origin/main", "origin"
		} else {
			fmt.Fprintf(stderr, "bundlectl: warning: %s has no origin remote; a release is checked "+
				"against its local main and tags only\n", dir)
		}
		return repo, func() {}, nil
	}
	temp, err := os.MkdirTemp("", "bundlectl-clone-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(temp) }
	dir := filepath.Join(temp, "boardly-plugins")
	if _, err := runGit(
		ctx,
		"",
		nil,
		"clone",
		"--origin",
		"origin",
		"--filter=blob:none",
		"--no-checkout",
		"--quiet",
		"--",
		source,
		dir,
	); err != nil {
		cleanup()
		return nil, nil, err
	}
	return &gitRepository{dir: dir, mainRef: "refs/remotes/origin/main", clone: true}, cleanup, nil
}

func (repo *gitRepository) git(ctx context.Context, args ...string) ([]byte, error) {
	return runGit(ctx, repo.dir, nil, args...)
}

// resolveCommit returns the commit a revision names, or ok false when the
// repository has no such revision.
func (repo *gitRepository) resolveCommit(ctx context.Context, revision string) (string, bool, error) {
	output, err := repo.git(ctx, "rev-parse", "--verify", "--quiet", revision+"^{commit}")
	if exitedWith(err, 1) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(string(output)), true, nil
}

// onMain reports whether commit is reachable from the main branch.
func (repo *gitRepository) onMain(ctx context.Context, commit string) (bool, error) {
	if _, ok, err := repo.resolveCommit(ctx, repo.mainRef); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("%s has no %s; fetch it and try again", repo.dir, repo.mainRef)
		}
		return false, err
	}
	_, err := repo.git(ctx, "merge-base", "--is-ancestor", commit, repo.mainRef)
	if exitedWith(err, 1) {
		return false, nil
	}
	return err == nil, err
}

// checkPublished refuses a tag a local checkout holds but its remote does not
// publish as the same object: an unpushed tag, or one that moved on either
// side. The object is the tag itself, annotated or lightweight, so equal
// objects name the same commit.
func (repo *gitRepository) checkPublished(ctx context.Context, tag string) error {
	if repo.remote == "" {
		return nil
	}
	local, err := repo.git(ctx, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag)
	if err != nil {
		return err
	}
	output, err := repo.git(ctx, "ls-remote", "--tags", repo.remote, "refs/tags/"+tag)
	if err != nil {
		return err
	}
	published := ""
	for line := range strings.SplitSeq(string(output), "\n") {
		if id, ref, ok := strings.Cut(line, "\t"); ok && ref == "refs/tags/"+tag {
			published = id
		}
	}
	switch {
	case published == "":
		return fmt.Errorf("tag %s is not on %s's %s remote; only a published release is pinned, so push "+
			"the tag first", tag, repo.dir, repo.remote)
	case published != strings.TrimSpace(string(local)):
		return fmt.Errorf("tag %s is not the one %s publishes; fetch it with git fetch --tags --force %s "+
			"and import again", tag, repo.remote, repo.remote)
	}
	return nil
}

// treeEntry is one line of git ls-tree: a blob, or a submodule's commit.
type treeEntry struct {
	mode string
	kind string
	id   string
	path string
}

// listTree lists every file under the given repository paths at commit,
// recursively, with full paths.
func (repo *gitRepository) listTree(ctx context.Context, commit string, paths ...string) ([]treeEntry, error) {
	args := append([]string{"ls-tree", "-r", "-z", "--full-tree", commit, "--"}, paths...)
	output, err := repo.git(ctx, args...)
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for record := range strings.SplitSeq(strings.TrimSuffix(string(output), "\x00"), "\x00") {
		if record == "" {
			continue
		}
		meta, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("git ls-tree printed an unexpected entry %q", record)
		}
		entries = append(entries, treeEntry{mode: fields[0], kind: fields[1], id: fields[2], path: name})
	}
	return entries, nil
}

// prefetch fetches, in one request, the blobs under treeish a partial clone
// does not have yet; otherwise git would fetch each one on first read.
func (repo *gitRepository) prefetch(ctx context.Context, treeish string) error {
	if !repo.clone {
		return nil
	}
	output, err := repo.git(ctx, "rev-list", "--objects", "--missing=print", treeish)
	if err != nil {
		return err
	}
	var missing []string
	for line := range strings.SplitSeq(string(output), "\n") {
		if id, ok := strings.CutPrefix(line, "?"); ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	_, err = repo.git(ctx, append([]string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "origin"},
		missing...)...)
	return err
}

// readBlobs returns the raw bytes of each blob id, read in one git cat-file
// process.
func (repo *gitRepository) readBlobs(ctx context.Context, ids []string) (map[string][]byte, error) {
	blobs := map[string][]byte{}
	if len(ids) == 0 {
		return blobs, nil
	}
	output, err := runGit(ctx, repo.dir, strings.NewReader(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	for len(output) > 0 {
		header, rest, ok := bytes.Cut(output, []byte("\n"))
		fields := strings.Fields(string(header))
		if !ok || len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("git cat-file cannot read %q as a blob", header)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || len(rest) < size+1 {
			return nil, fmt.Errorf("git cat-file printed a truncated blob %s", fields[0])
		}
		blobs[fields[0]] = bytes.Clone(rest[:size])
		output = rest[size+1:]
	}
	return blobs, nil
}
