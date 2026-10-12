package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"boardly.dev/boardly/server/internal/contentbundle"
)

// importOptions are the flags of bundlectl import.
type importOptions struct {
	tag     string
	repo    string
	out     string
	summary string
	verify  bool
	dev     bool
	from    string
	plugin  string
}

func runImport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("import")
	var options importOptions
	flags.StringVar(&options.tag, "tag", "", "the release tag to pin, <plugin>--vX.Y.Z")
	flags.StringVar(&options.repo, "repo", "https://github.com/Intelligent-Internet/boardly-plugins.git",
		"boardly-plugins: a URL to clone, or a local checkout read in place")
	flags.StringVar(&options.out, "out", "internal/platformbundle", "the pin directory")
	flags.StringVar(&options.summary, "summary", "", "also write the Markdown summary to this file")
	flags.BoolVar(&options.verify, "verify", false, "re-import every locked tag and require identical bytes")
	flags.BoolVar(&options.dev, "dev", false, "pin a local checkout for development")
	flags.StringVar(&options.from, "from", "", "with --dev: the boardly-plugins checkout")
	flags.StringVar(&options.plugin, "plugin", "", "with --dev: the plugin to pin")
	if err := parseFlags(flags, args, stdout, false); err != nil {
		return err
	}
	set := map[string]bool{}
	flags.Visit(func(visited *flag.Flag) { set[visited.Name] = true })
	modes := 0
	for _, mode := range []bool{options.tag != "", options.verify, options.dev} {
		if mode {
			modes++
		}
	}
	switch {
	case modes != 1:
		return usageErrorf("import takes exactly one of --tag, --verify and --dev")
	case options.dev && (options.from == "" || options.plugin == ""):
		return usageErrorf("import --dev needs --from and --plugin")
	case options.dev && set["repo"]:
		return usageErrorf("import --dev reads --from, not --repo")
	case !options.dev && (set["from"] || set["plugin"]):
		return usageErrorf("--from and --plugin go with --dev")
	case options.verify && set["summary"]:
		return usageErrorf("import --verify writes no summary")
	}
	if options.tag != "" {
		if _, _, err := parseTag(options.tag); err != nil {
			return err
		}
	}
	if info, err := os.Stat(options.out); err != nil || !info.IsDir() {
		return fmt.Errorf("the pin directory %s does not exist", options.out)
	}
	err := func() error {
		switch {
		case options.verify:
			return verifyPins(ctx, options, stdout, stderr)
		case options.dev:
			return importDev(ctx, options, stdout, stderr)
		default:
			return importRelease(ctx, options, stdout, stderr)
		}
	}()
	var failed *problemsError
	if errors.As(err, &failed) {
		printProblems(stderr, "text", failed.problems)
	}
	return err
}

// parseTag splits <plugin>--vX.Y.Z at the first "--v"; plugin names never
// contain "--".
func parseTag(tag string) (string, string, error) {
	plugin, version, found := strings.Cut(tag, "--v")
	if !found || len(plugin) > 64 || !namePattern.MatchString(plugin) || !versionPattern.MatchString(version) {
		return "", "", usageErrorf("tag %q is not <plugin>--vX.Y.Z", tag)
	}
	return plugin, version, nil
}

// importRelease pins one release tag: it imports the tag, checks it against
// the pin it replaces, writes the lock and any Workflow archive, and prints the summary.
func importRelease(ctx context.Context, options importOptions, stdout, stderr io.Writer) error {
	repo, cleanup, err := openRepository(ctx, options.repo, stderr)
	if err != nil {
		return err
	}
	defer cleanup()
	component, err := importTag(ctx, repo, options.tag, stderr)
	if err != nil {
		return err
	}
	return pin(ctx, options, component, stdout, stderr)
}

// importTag reads a release tag and builds its component with every
// production rule. It writes nothing.
func importTag(ctx context.Context, repo *gitRepository, tag string, stderr io.Writer) (built, error) {
	plugin, version, err := parseTag(tag)
	if err != nil {
		return built{}, err
	}
	commit, found, err := repo.resolveCommit(ctx, "refs/tags/"+tag)
	if err != nil {
		return built{}, err
	}
	if !found {
		return built{}, fmt.Errorf("%s has no tag %s", repo.dir, tag)
	}
	onMain, err := repo.onMain(ctx, commit)
	if err != nil {
		return built{}, err
	}
	if !onMain {
		return built{}, fmt.Errorf(
			"tag %s (commit %s) is not reachable from %s; only a release merged to main is pinned",
			tag,
			commit,
			repo.mainRef,
		)
	}
	if err := repo.checkPublished(ctx, tag); err != nil {
		return built{}, err
	}
	source, err := readPluginCommit(ctx, repo, commit, plugin, stderr)
	if err != nil {
		return built{}, err
	}
	if source.manifest.Version != "" && source.manifest.Version != version {
		source.fail(
			manifestPath,
			"version %q does not match the tag's version %q",
			source.manifest.Version,
			version,
		)
	}
	source.identity.Version = version
	source.identity.Tag = tag
	return buildOne(source)
}

// buildOne builds one plugin with the pinned bundles of every other plugin.
func buildOne(source pluginSource) (built, error) {
	others, _, problems := pinnedBundles([]string{source.name})
	components, buildProblems := buildPlugins([]pluginSource{source}, others)
	if problems = append(problems, buildProblems...); len(problems) > 0 {
		return built{}, &problemsError{problems: problems}
	}
	return components[0], nil
}

// importDev pins a local checkout as it is on disk. Its version names the
// checkout's HEAD, with ".dirty" unless the plugin's files are exactly HEAD's;
// TestPinnedBundlesAreReleases keeps such a pin from being committed.
func importDev(ctx context.Context, options importOptions, stdout, stderr io.Writer) error {
	plugin := options.plugin
	if len(plugin) > 64 || !namePattern.MatchString(plugin) {
		return usageErrorf("plugin %q is not a kebab-case name of at most 64 characters", plugin)
	}
	if _, err := os.Stat(filepath.Join(options.from, ".git")); err != nil {
		return fmt.Errorf("%s is not a git checkout", options.from)
	}
	repo := &gitRepository{dir: options.from}
	commit, found, err := repo.resolveCommit(ctx, "HEAD")
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s has no commit yet", options.from)
	}
	root := "plugins/" + plugin
	source := readPluginDir(ctx, filepath.Join(options.from, "plugins", plugin), root)
	source.identity.Commit = commit
	source.identity.Version = "0.0.0-dev+" + commit[:12]
	// Only a pin of exactly HEAD's files is clean, since only then do its
	// commit and content tree reproduce it. An edit, a deletion, or an
	// untracked or skip-worktree difference each make it dirty; ignored files
	// were never read.
	head, err := readPluginCommit(ctx, repo, commit, plugin, io.Discard)
	if err != nil {
		return err
	}
	if sameFiles(source, head) {
		source.identity.ContentTreeID = head.identity.ContentTreeID
	} else {
		source.identity.Version += ".dirty"
	}
	component, err := buildOne(source)
	if err != nil {
		return err
	}
	if err := pin(ctx, options, component, stdout, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "bundlectl: warning: %s now pins a development checkout (%s), not a release. "+
		"TestPinnedBundlesAreReleases / TestPinnedToolBundlesAreReleases fail until "+
		"make bundle-bump replaces it; never commit it.\n",
		filepath.Join(options.out, plugin+contentbundle.LockSuffix), source.identity.Version)
	return nil
}

// sameFiles reports whether a plugin read from disk carries exactly the
// Boardly files of the commit read beside it.
func sameFiles(disk, commit pluginSource) bool {
	if commit.identity.ContentTreeID == "" || !bytes.Equal(disk.manifestRaw, commit.manifestRaw) ||
		!bytes.Equal(disk.retiredRaw, commit.retiredRaw) || len(disk.files) != len(commit.files) {
		return false
	}
	for name, contents := range disk.files {
		if committed, ok := commit.files[name]; !ok || !bytes.Equal(committed, contents) {
			return false
		}
	}
	return true
}

// pin checks a built component against the release it replaces, writes the
// Workflow archive (if present) and then the lock, each atomically, and prints the summary.
func pin(ctx context.Context, options importOptions, component built, stdout, stderr io.Writer) error {
	lock := component.bundle.Lock
	current, currentErr := readPin(options.out, lock.Plugin)
	base := current
	if currentErr != nil || current != nil && current.Lock.Development() {
		var err error
		if base, err = committedRelease(ctx, options.out, lock, currentErr, stderr); err != nil {
			return err
		}
	}
	// A release's summary is its pull request's description, so it compares
	// with the release it replaces; a development pin's compares with the pin
	// it overwrites.
	before := base
	if lock.Development() && currentErr == nil {
		before = current
	}
	change, err := comparePins(before, &component.bundle)
	if err != nil {
		return err
	}
	ruleChange := change
	if before != base {
		if ruleChange, err = comparePins(base, &component.bundle); err != nil {
			return err
		}
	}
	problems := replacementProblems(base, &component.bundle, ruleChange, "plugins/"+lock.Plugin, true)
	if len(problems) > 0 {
		return &problemsError{problems: problems}
	}
	if base != nil && commitMovedOnly(base.Lock, lock) {
		fmt.Fprintf(stderr, "bundlectl: %s %s was pinned from commit %s; its tag now names commit %s with "+
			"byte-identical content, so only the lock's commit changes\n", lock.Plugin, lock.Version,
			base.Lock.Commit, lock.Commit)
	}
	encodedLock, err := contentbundle.MarshalLock(lock)
	if err != nil {
		return err
	}
	if lock.Artifact.File != "" {
		if err := writeFileAtomic(
			filepath.Join(options.out, lock.Artifact.File),
			component.artifact,
		); err != nil {
			return err
		}
	}
	if err := writeFileAtomic(
		filepath.Join(options.out, lock.Plugin+contentbundle.LockSuffix),
		encodedLock,
	); err != nil {
		return err
	}
	var summary bytes.Buffer
	summary.WriteString("## Platform bundle import\n\n")
	writePluginMarkdown(&summary, change)
	if _, err := stdout.Write(summary.Bytes()); err != nil {
		return err
	}
	if options.summary != "" {
		return os.WriteFile(options.summary, summary.Bytes(), 0o644)
	}
	return nil
}

// readPin reads and verifies the plugin's current pin in a pin directory, or
// returns nil when there is none.
func readPin(out, plugin string) (*contentbundle.Bundle, error) {
	raw, err := os.ReadFile(filepath.Join(out, plugin+contentbundle.LockSuffix))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lock, err := contentbundle.ParseLock(raw)
	if err == nil && lock.Plugin != plugin {
		err = fmt.Errorf("%w: it pins plugin %q", contentbundle.ErrInvalid, lock.Plugin)
	}
	if err != nil {
		return nil, fmt.Errorf("the current pin %s: %w", plugin+contentbundle.LockSuffix, err)
	}
	var artifact []byte
	if lock.Artifact.File != "" {
		artifact, err = os.ReadFile(filepath.Join(out, lock.Artifact.File))
		if err != nil {
			return nil, fmt.Errorf("the current pin %s: %w", lock.Artifact.File, err)
		}
	}
	bundle, err := contentbundle.Decode(lock, artifact)
	if err != nil {
		return nil, fmt.Errorf("the current pin does not verify: %w", err)
	}
	return &bundle, nil
}

// committedRelease finds the release baseline for a development or broken
// working-tree pin. A committed development pin falls back to the last release
// in first-parent history; it never becomes a release baseline itself.
// Unreadable pins or incomplete history fail a release import closed.
func committedRelease(
	ctx context.Context, out string, next contentbundle.Lock, currentErr error, stderr io.Writer,
) (*contentbundle.Bundle, error) {
	plugin := next.Plugin
	reason := "is a development pin"
	if currentErr != nil {
		reason = fmt.Sprintf("does not verify (%v)", currentErr)
	}
	release, revision, err := lastCommittedRelease(ctx, out, plugin)
	switch {
	case err != nil && !next.Development():
		return nil, fmt.Errorf("the %s pin %s, and its committed release baseline cannot be read (%w), so "+
			"the release rules cannot be applied; fetch complete history or restore the last release pin "+
			"and import again", plugin, reason, err)
	case err != nil:
		fmt.Fprintf(stderr, "bundlectl: warning: the %s pin %s, and its committed release baseline cannot "+
			"be read (%v); a development pin is judged against nothing\n", plugin, reason, err)
	case release == nil:
		fmt.Fprintf(stderr, "bundlectl: the %s pin %s, and no preceding release is pinned\n", plugin, reason)
	default:
		fmt.Fprintf(stderr, "bundlectl: the %s pin %s; judging the import against %s, committed at %s\n",
			plugin, reason, release.Lock.Version, revision)
	}
	return release, nil
}

func lastCommittedRelease(ctx context.Context, out, plugin string) (*contentbundle.Bundle, string, error) {
	pin, err := pinAt(ctx, out, plugin, "HEAD")
	if err != nil || pin == nil || !pin.Lock.Development() {
		return pin, "HEAD", err
	}
	// Include archive-only changes so a corrupt Workflow archive is not skipped.
	// First-parent history follows the pin states adopted by this branch, including
	// merge results, rather than choosing a release from an unmerged side branch.
	history, err := runGit(ctx, out, nil, "log", "--first-parent", "--format=%H", "HEAD", "--",
		plugin+contentbundle.LockSuffix, plugin+contentbundle.ArtifactSuffix)
	if err != nil {
		return nil, "", err
	}
	for _, revision := range strings.Fields(string(history)) {
		pin, err := pinAt(ctx, out, plugin, revision)
		if err != nil {
			return nil, "", fmt.Errorf("pin at %s: %w", revision, err)
		}
		if pin != nil && !pin.Lock.Development() {
			return pin, revision, nil
		}
	}
	shallow, err := runGit(ctx, out, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(string(shallow)) == "true" {
		return nil, "", errors.New("shallow history cannot establish the preceding release; fetch full history")
	}
	return nil, "", nil
}

// pinAt returns the plugin's verified pin at a revision, including development
// pins, or nil when the revision pins none.
func pinAt(ctx context.Context, out, plugin, revision string) (*contentbundle.Bundle, error) {
	snapshot, err := readPinRevision(ctx, out, revision)
	if err != nil {
		return nil, err
	}
	bundles, loadErr := contentbundle.LoadFS(contentbundle.NewFS(snapshot))
	for index, bundle := range bundles {
		if bundle.Lock.Plugin != plugin {
			continue
		}
		return &bundles[index], nil
	}
	failures := []error{loadErr}
	if joined, ok := loadErr.(interface{ Unwrap() []error }); ok {
		failures = joined.Unwrap()
	}
	for _, failure := range failures {
		var pinErr *contentbundle.LoadError
		if errors.As(failure, &pinErr) && pinErr.Plugin == plugin {
			return nil, failure
		}
	}
	return nil, nil
}

// replacementProblems are the rules between a plugin's pinned release, base,
// and what replaces it, next (nil when a pin is dropped). Every command
// applies the key rules: a removed Workflow key is retired, and a retired key
// stays retired. Versions are compared only between releases: never lower
// and, for a release being pinned (release), the same version only from
// byte-identical content, since a release tag never moves, and a MAJOR
// version for any removal or retirement. A plugin folder check reads may still
// carry the pinned version while its content moves on toward its next
// release, so check leaves those two out. A development base is no release
// and judges nothing. folder is the plugin's folder, where problems point.
func replacementProblems(
	base, next *contentbundle.Bundle, change pluginChange, folder string, release bool,
) []problem {
	if base == nil || base.Lock.Development() {
		return nil
	}
	old := base.Lock
	if next == nil {
		message := fmt.Sprintf("%s %s is no longer pinned, so its %d Workflows would disappear; a Workflow "+
			"key is retired, never dropped", old.Plugin, old.Version, len(change.removed))
		return []problem{{message: message}}
	}
	lock := next.Lock
	if old.Component != lock.Component {
		return []problem{
			{file: folder + "/" + manifestPath, message: "a plugin cannot change its component type"},
		}
	}
	manifestFile, retiredFile := folder+"/"+manifestPath, folder+"/"+retiredKeysPath
	var problems []problem
	if !lock.Development() && versionPattern.MatchString(lock.Version) {
		order := compareVersions(lock.Version, old.Version)
		major, _, _ := strings.Cut(lock.Version, ".")
		oldMajor, _, _ := strings.Cut(old.Version, ".")
		switch {
		case order < 0:
			problems = append(problems, problem{file: manifestFile, message: fmt.Sprintf(
				"%s %s is lower than the pinned %s; a pin never goes back", lock.Plugin, lock.Version,
				old.Version)})
		case release && order == 0 && !sameRelease(old, lock):
			problems = append(problems, problem{file: manifestFile, message: fmt.Sprintf(
				"%s %s is already pinned from commit %s with other content; a release tag never moves, "+
					"so release a new version",
				lock.Plugin,
				lock.Version,
				old.Commit,
			)})
		case release && len(change.removed)+len(change.newlyRetired) > 0 && compareNumbers(major, oldMajor) <= 0:
			problems = append(problems, problem{file: manifestFile, message: fmt.Sprintf(
				"removing or retiring a %s takes a MAJOR release, and %s %s -> %s is not one",
				componentKeyName(lock.Component), lock.Plugin, old.Version, lock.Version)})
		}
	}
	if lock.Component == contentbundle.ComponentTypeManagedTools {
		return problems
	}
	for _, key := range change.removed {
		if !slices.Contains(lock.RetiredKeys, key) {
			problems = append(problems, problem{file: retiredFile, message: fmt.Sprintf(
				"Workflow %q was removed but is not retired; add it to retiredKeys (a MAJOR release)",
				key,
			)})
		}
	}
	for _, key := range old.RetiredKeys {
		if !slices.Contains(lock.RetiredKeys, key) {
			problems = append(problems, problem{file: retiredFile, message: fmt.Sprintf(
				"retired key %q is no longer listed; a retired key stays retired", key)})
		}
	}
	return problems
}

// sameRelease reports whether two release locks pin the same content, with
// the same manifest and retirements, whatever commit each names.
func sameRelease(old, next contentbundle.Lock) bool {
	next.Commit = old.Commit
	oldEncoded, oldErr := contentbundle.MarshalLock(old)
	nextEncoded, nextErr := contentbundle.MarshalLock(next)
	return oldErr == nil && nextErr == nil && bytes.Equal(oldEncoded, nextEncoded)
}

// commitMovedOnly reports whether a release is re-pinned from another commit
// with nothing else changed, as when a provisional commit gives way to the
// tagged one.
func commitMovedOnly(old, next contentbundle.Lock) bool {
	return !old.Development() && !next.Development() && old.Commit != next.Commit && sameRelease(old, next)
}

// compareVersions orders two X.Y.Z versions.
func compareVersions(left, right string) int {
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	for index := range min(len(leftParts), len(rightParts)) {
		if order := compareNumbers(leftParts[index], rightParts[index]); order != 0 {
			return order
		}
	}
	return 0
}

// compareNumbers orders two decimal numbers written without leading zeros, as
// a version's are, however many digits they have.
func compareNumbers(left, right string) int {
	if order := cmp.Compare(len(left), len(right)); order != 0 {
		return order
	}
	return strings.Compare(left, right)
}

// verifyPins re-imports the tag of every lock in the pin directory and
// requires the lock and any Workflow archive to be byte-identical: the provenance check
// behind make bundle-verify. A development pin cannot be verified.
func verifyPins(ctx context.Context, options importOptions, stdout, stderr io.Writer) error {
	locks, err := filepath.Glob(filepath.Join(options.out, "*"+contentbundle.LockSuffix))
	if err != nil {
		return err
	}
	artifacts, err := filepath.Glob(filepath.Join(options.out, "*"+contentbundle.ArtifactSuffix))
	if err != nil {
		return err
	}
	if len(locks) == 0 {
		return fmt.Errorf("%s pins no bundle", options.out)
	}
	var problems []problem
	for _, artifact := range artifacts {
		lockName := strings.TrimSuffix(artifact, contentbundle.ArtifactSuffix) + contentbundle.LockSuffix
		if !slices.Contains(locks, lockName) {
			problems = append(problems, problem{file: artifact, message: "has no lock"})
		}
	}
	var repo *gitRepository
	for _, lockPath := range locks {
		raw, err := os.ReadFile(lockPath)
		if err != nil {
			return err
		}
		lock, err := contentbundle.ParseLock(raw)
		if err != nil {
			problems = append(problems, problem{file: lockPath, message: err.Error()})
			continue
		}
		if lock.Development() {
			problems = append(problems, problem{file: lockPath, message: fmt.Sprintf(
				"pins a development checkout (%s), not a release; only a release can be verified, "+
					"so replace it with make bundle-bump", lock.Version)})
			continue
		}
		if lock.Component == contentbundle.ComponentTypeManagedTools &&
			slices.Contains(
				artifacts,
				filepath.Join(options.out, lock.Plugin+contentbundle.ArtifactSuffix),
			) {
			problems = append(
				problems,
				problem{file: lockPath, message: "managed Tool pins must not have an archive"},
			)
			continue
		}
		if repo == nil {
			var cleanup func()
			if repo, cleanup, err = openRepository(ctx, options.repo, stderr); err != nil {
				return err
			}
			defer cleanup()
		}
		component, err := importTag(ctx, repo, lock.Tag, stderr)
		var failed *problemsError
		if errors.As(err, &failed) {
			problems = append(problems, failed.problems...)
			continue
		}
		if err != nil {
			problems = append(
				problems,
				problem{file: lockPath, message: fmt.Sprintf("re-importing %s: %v", lock.Tag, err)},
			)
			continue
		}
		reimported, err := contentbundle.MarshalLock(component.bundle.Lock)
		if err != nil {
			return err
		}
		var committed []byte
		if lock.Artifact.File != "" {
			committed, err = os.ReadFile(filepath.Join(options.out, lock.Artifact.File))
			if err != nil {
				return err
			}
		}
		switch {
		case !bytes.Equal(reimported, raw):
			problems = append(problems, problem{file: lockPath, message: fmt.Sprintf(
				"does not match a fresh import of %s: %s", lock.Tag,
				strings.Join(lockDifferences(lock, component.bundle.Lock), "; "))})
		case !bytes.Equal(committed, component.artifact):
			problems = append(
				problems,
				problem{file: filepath.Join(options.out, lock.Artifact.File), message: fmt.Sprintf(
					"is not the artifact %s produces; restore the committed one with git restore, or re-import "+
						"it with make bundle-bump TAG=%s",
					lock.Tag,
					lock.Tag,
				)},
			)
		default:
			digest := lock.Artifact.SHA256
			if lock.Component == contentbundle.ComponentTypeManagedTools {
				digest = lock.TreeHash
			}
			fmt.Fprintf(
				stdout,
				"%s: %s verified (commit %s, sha256 %s)\n",
				lock.Plugin,
				lock.Tag,
				lock.Commit,
				digest,
			)
		}
	}
	if len(problems) > 0 {
		return &problemsError{problems: problems}
	}
	return nil
}

// lockDifferences names each lock field where the committed pin and a fresh
// import disagree.
func lockDifferences(committed, fresh contentbundle.Lock) []string {
	fields := []struct {
		name          string
		pinned, found string
	}{
		{"version", committed.Version, fresh.Version},
		{"commit", committed.Commit, fresh.Commit},
		{"manifestSha256", committed.ManifestSHA256, fresh.ManifestSHA256},
		{"contentTreeId", committed.ContentTreeID, fresh.ContentTreeID},
		{"artifact.sha256", committed.Artifact.SHA256, fresh.Artifact.SHA256},
		{
			"artifact.size",
			strconv.FormatInt(committed.Artifact.Size, 10),
			strconv.FormatInt(fresh.Artifact.Size, 10),
		},
		{"treeHash", committed.TreeHash, fresh.TreeHash},
		{"fileCount", strconv.Itoa(committed.FileCount), strconv.Itoa(fresh.FileCount)},
		{"retiredKeys", strings.Join(committed.RetiredKeys, ","), strings.Join(fresh.RetiredKeys, ",")},
	}
	var differences []string
	for _, field := range fields {
		if field.pinned != field.found {
			differences = append(
				differences,
				fmt.Sprintf("%s is %q, the tag gives %q", field.name, field.pinned,
					field.found),
			)
		}
	}
	if len(differences) == 0 {
		differences = append(differences, "the encoding differs")
	}
	return differences
}

// writeFileAtomic replaces name with data through a temporary file in the
// same directory and a rename, so a reader never sees half a pin file.
func writeFileAtomic(name string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(temp.Name(), name)
}

func componentKeyName(component contentbundle.ComponentType) string {
	if component == contentbundle.ComponentTypeManagedTools {
		return "Tool"
	}
	return "Workflow"
}
