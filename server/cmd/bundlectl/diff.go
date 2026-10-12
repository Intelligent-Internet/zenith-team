package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// harnessRoot holds the files boardly owns beside the pins, such as the
// zenith-workflows SKILL.md laid over the Workflow catalog.
const harnessRoot = "harness/"

// runDiff shows what the working tree pin changes against a git revision and
// holds every changed plugin to the release rules, so a bump that goes back,
// drops a Workflow or undoes a retirement fails however its pin was written.
func runDiff(ctx context.Context, args []string, stdout, _ io.Writer) error {
	flags := newFlagSet("diff")
	base := flags.String("base", "", "the git revision to compare with, such as origin/main")
	format := flags.String("format", "text", "text or markdown")
	out := flags.String("out", "internal/platformbundle", "the pin directory")
	if err := parseFlags(flags, args, stdout, false); err != nil {
		return err
	}
	switch {
	case *base == "":
		return usageErrorf("diff needs --base")
	case *format != "text" && *format != "markdown":
		return usageErrorf("--format must be text or markdown, got %q", *format)
	}
	before, err := readPinRevision(ctx, *out, *base)
	if err != nil {
		return err
	}
	after, err := readPinDir(*out)
	if err != nil {
		return err
	}
	difference, err := diffPins(before, after)
	if err != nil {
		return err
	}
	var rendered bytes.Buffer
	if *format == "markdown" {
		writeDiffMarkdown(&rendered, *base, difference)
	} else {
		writeDiffText(&rendered, *base, difference)
	}
	if _, err := stdout.Write(rendered.Bytes()); err != nil {
		return err
	}
	if len(difference.problems) > 0 {
		return &problemsError{problems: difference.problems}
	}
	return nil
}

// pinSnapshot is a pin directory at one moment: locks and Workflow archives at its
// top level and each harness file, by path inside the directory.
type pinSnapshot map[string][]byte

func pinFile(name string) bool {
	return !strings.Contains(name, "/") &&
		(strings.HasSuffix(name, contentbundle.LockSuffix) || strings.HasSuffix(name, contentbundle.ArtifactSuffix))
}

// harnessFile reports whether the embed carries a file under harness/:
// //go:embed leaves out every name that starts with "." or "_", such as a
// Finder .DS_Store.
func harnessFile(name string) bool {
	inside, ok := strings.CutPrefix(name, harnessRoot)
	if !ok {
		return false
	}
	for element := range strings.SplitSeq(inside, "/") {
		if strings.HasPrefix(element, ".") || strings.HasPrefix(element, "_") {
			return false
		}
	}
	return true
}

// readPinDir reads the working tree's pin directory.
func readPinDir(dir string) (pinSnapshot, error) {
	snapshot := pinSnapshot{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && pinFile(entry.Name()) {
			if snapshot[entry.Name()], err = os.ReadFile(filepath.Join(dir, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	err = filepath.WalkDir(
		filepath.Join(dir, harnessRoot),
		func(name string, entry fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return fs.SkipAll
			}
			if walkErr != nil || !entry.Type().IsRegular() {
				return walkErr
			}
			relative, err := filepath.Rel(dir, name)
			if err != nil || !harnessFile(filepath.ToSlash(relative)) {
				return err
			}
			snapshot[filepath.ToSlash(relative)], err = os.ReadFile(name)
			return err
		},
	)
	return snapshot, err
}

// readPinRevision reads the same pin directory at a git revision, from the
// repository that holds it. A revision before the directory existed has an
// empty pin.
func readPinRevision(ctx context.Context, dir, revision string) (pinSnapshot, error) {
	output, err := runGit(ctx, dir, nil, "rev-parse", "--show-toplevel", "--show-prefix")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(output), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("git rev-parse printed %q", output)
	}
	repo := &gitRepository{dir: lines[0]}
	prefix := lines[1]
	commit, found, err := repo.resolveCommit(ctx, revision)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s has no revision %s", repo.dir, revision)
	}
	var paths []string
	if prefix != "" {
		paths = append(paths, strings.TrimSuffix(prefix, "/"))
	}
	entries, err := repo.listTree(ctx, commit, paths...)
	if err != nil {
		return nil, err
	}
	names := map[string]string{} // blob id → path inside the pin directory
	var ids []string
	for _, entry := range entries {
		relative, inside := strings.CutPrefix(entry.path, prefix)
		if inside && entry.kind == "blob" && (pinFile(relative) || harnessFile(relative)) {
			names[entry.path] = relative
			ids = append(ids, entry.id)
		}
	}
	slices.Sort(ids)
	blobs, err := repo.readBlobs(ctx, slices.Compact(ids))
	if err != nil {
		return nil, err
	}
	snapshot := pinSnapshot{}
	for _, entry := range entries {
		if relative, ok := names[entry.path]; ok {
			snapshot[relative] = blobs[entry.id]
		}
	}
	return snapshot, nil
}

// pluginChange is what one plugin's pin changes: its locks before and after
// (nil when absent) and its Workflow keys by content.
type pluginChange struct {
	plugin       string
	before       *contentbundle.Lock
	after        *contentbundle.Lock
	added        []string
	changed      []string
	removed      []string
	newlyRetired []string
}

// comparePins compares two verified pins of one plugin; either may be nil.
func comparePins(before, after *contentbundle.Bundle) (pluginChange, error) {
	var change pluginChange
	beforeDigests, afterDigests := map[string]string{}, map[string]string{}
	var err error
	if before != nil {
		lock := before.Lock
		change.plugin, change.before = lock.Plugin, &lock
		if beforeDigests, err = componentDigests(*before); err != nil {
			return change, err
		}
	}
	if after != nil {
		lock := after.Lock
		change.plugin, change.after = lock.Plugin, &lock
		if afterDigests, err = componentDigests(*after); err != nil {
			return change, err
		}
	}
	for key, digest := range afterDigests {
		previous, existed := beforeDigests[key]
		switch {
		case !existed:
			change.added = append(change.added, key)
		case previous != digest:
			change.changed = append(change.changed, key)
		}
	}
	for key := range beforeDigests {
		if _, kept := afterDigests[key]; !kept {
			change.removed = append(change.removed, key)
		}
	}
	if change.after != nil {
		for _, key := range change.after.RetiredKeys {
			if change.before == nil || !slices.Contains(change.before.RetiredKeys, key) {
				change.newlyRetired = append(change.newlyRetired, key)
			}
		}
	}
	slices.Sort(change.added)
	slices.Sort(change.changed)
	slices.Sort(change.removed)
	return change, nil
}

// harnessChange is one harness file that differs between two pins.
type harnessChange struct {
	path  string
	state string // added, removed or changed
}

// pinDiff is everything that differs between two pin directories, and the
// release rules the changed plugins break.
type pinDiff struct {
	changes   []pluginChange
	unchanged []contentbundle.Lock
	harness   []harnessChange
	problems  []problem
}

// diffPins verifies both pins with the loader, compares them plugin by
// plugin, holding each changed plugin to the release rules, and compares
// their harness files byte for byte.
func diffPins(before, after pinSnapshot) (pinDiff, error) {
	var difference pinDiff
	beforeBundles, err := contentbundle.LoadFS(contentbundle.NewFS(before))
	if err != nil {
		return difference, fmt.Errorf("the pin before: %w", err)
	}
	afterBundles, err := contentbundle.LoadFS(contentbundle.NewFS(after))
	if err != nil {
		return difference, fmt.Errorf("the pin after: %w", err)
	}
	byPlugin := func(bundles []contentbundle.Bundle) map[string]*contentbundle.Bundle {
		indexed := map[string]*contentbundle.Bundle{}
		for index := range bundles {
			indexed[bundles[index].Lock.Plugin] = &bundles[index]
		}
		return indexed
	}
	beforeByPlugin, afterByPlugin := byPlugin(beforeBundles), byPlugin(afterBundles)
	plugins := make([]string, 0, len(beforeByPlugin)+len(afterByPlugin))
	for plugin := range beforeByPlugin {
		plugins = append(plugins, plugin)
	}
	for plugin := range afterByPlugin {
		plugins = append(plugins, plugin)
	}
	slices.Sort(plugins)
	for _, plugin := range slices.Compact(plugins) {
		previous, next := beforeByPlugin[plugin], afterByPlugin[plugin]
		if previous != nil && next != nil && previous.Lock.Artifact.SHA256 == next.Lock.Artifact.SHA256 &&
			bytes.Equal(before[plugin+contentbundle.LockSuffix], after[plugin+contentbundle.LockSuffix]) {
			difference.unchanged = append(difference.unchanged, next.Lock)
			continue
		}
		change, err := comparePins(previous, next)
		if err != nil {
			return difference, err
		}
		difference.changes = append(difference.changes, change)
		difference.problems = append(difference.problems,
			replacementProblems(previous, next, change, "plugins/"+plugin, true)...)
	}
	var harness []string
	for name := range before {
		if strings.HasPrefix(name, harnessRoot) {
			harness = append(harness, name)
		}
	}
	for name := range after {
		if strings.HasPrefix(name, harnessRoot) {
			harness = append(harness, name)
		}
	}
	slices.Sort(harness)
	for _, name := range slices.Compact(harness) {
		previous, existed := before[name]
		next, exists := after[name]
		switch {
		case !existed:
			difference.harness = append(difference.harness, harnessChange{path: name, state: "added"})
		case !exists:
			difference.harness = append(difference.harness, harnessChange{path: name, state: "removed"})
		case !bytes.Equal(previous, next):
			difference.harness = append(difference.harness, harnessChange{path: name, state: "changed"})
		}
	}
	return difference, nil
}

// lockRow is one line of the before and after table.
type lockRow struct {
	label  string
	values func(lock contentbundle.Lock) string
}

func rowsForChange(change pluginChange) []lockRow {
	lock := change.after
	if lock == nil {
		lock = change.before
	}
	rows := []lockRow{
		{"Version", func(lock contentbundle.Lock) string { return lock.Version }},
		{"Tag", func(lock contentbundle.Lock) string { return lock.Tag }},
		{"Commit", func(lock contentbundle.Lock) string { return lock.Commit }},
		{"Content tree", func(lock contentbundle.Lock) string { return lock.ContentTreeID }},
	}
	if lock.Component == contentbundle.ComponentTypeManagedTools {
		rows = append(
			rows,
			lockRow{"Content hash", func(lock contentbundle.Lock) string { return lock.TreeHash }},
		)
	} else {
		rows = append(
			rows,
			lockRow{
				"Artifact sha256",
				func(lock contentbundle.Lock) string { return lock.Artifact.SHA256 },
			},
			lockRow{
				"Artifact size",
				func(lock contentbundle.Lock) string { return strconv.FormatInt(lock.Artifact.Size, 10) },
			},
		)
	}
	rows = append(
		rows,
		lockRow{"Files", func(lock contentbundle.Lock) string { return strconv.Itoa(lock.FileCount) }},
	)
	if lock.Component == contentbundle.ComponentTypeWorkflows {
		rows = append(
			rows,
			lockRow{
				"Retired keys",
				func(lock contentbundle.Lock) string { return strings.Join(lock.RetiredKeys, ", ") },
			},
		)
	}
	return rows
}

func versionOf(lock *contentbundle.Lock) string {
	if lock == nil {
		return "none"
	}
	return lock.Version
}

// writePluginMarkdown renders one plugin's change: a before and after table
// of its lock, then its Workflow keys.
func writePluginMarkdown(output *bytes.Buffer, change pluginChange) {
	fmt.Fprintf(output, "### %s %s → %s\n\n", change.plugin, versionOf(change.before), versionOf(change.after))
	output.WriteString("| | Before | After |\n| --- | --- | --- |\n")
	cell := func(lock *contentbundle.Lock, row lockRow) string {
		if lock == nil {
			return "—"
		}
		if value := row.values(*lock); value != "" {
			return "`" + value + "`"
		}
		return "none"
	}
	for _, row := range rowsForChange(change) {
		fmt.Fprintf(output, "| %s | %s | %s |\n", row.label, cell(change.before, row), cell(change.after, row))
	}
	output.WriteString("\n")
	keys := func(label string, list []string) {
		quoted := make([]string, 0, len(list))
		for _, key := range list {
			quoted = append(quoted, "`"+key+"`")
		}
		value := "none"
		if len(quoted) > 0 {
			value = strings.Join(quoted, ", ")
		}
		fmt.Fprintf(output, "- %s (%d): %s\n", label, len(list), value)
	}
	keys("Added", change.added)
	keys("Changed", change.changed)
	keys("Removed", change.removed)
	keys("Newly retired", change.newlyRetired)
	output.WriteString("\n")
}

func writeDiffMarkdown(output *bytes.Buffer, base string, difference pinDiff) {
	fmt.Fprintf(output, "## Platform bundles against `%s`\n\n", base)
	if len(difference.changes) == 0 && len(difference.harness) == 0 {
		output.WriteString("No pinned bundle or harness file changed.\n")
		return
	}
	for _, change := range difference.changes {
		writePluginMarkdown(output, change)
	}
	for _, lock := range difference.unchanged {
		fmt.Fprintf(output, "Unchanged: `%s` %s.\n\n", lock.Plugin, lock.Version)
	}
	if len(difference.harness) == 0 {
		output.WriteString("Harness: unchanged.\n")
	} else {
		output.WriteString("Harness:\n\n")
		for _, change := range difference.harness {
			fmt.Fprintf(output, "- `%s` %s\n", change.path, change.state)
		}
	}
	if len(difference.problems) > 0 {
		output.WriteString("\n### Problems\n\n")
		for _, p := range difference.problems {
			if p.file != "" {
				fmt.Fprintf(output, "- `%s`: %s\n", p.file, p.message)
			} else {
				fmt.Fprintf(output, "- %s\n", p.message)
			}
		}
	}
}

func writeDiffText(output *bytes.Buffer, base string, difference pinDiff) {
	if len(difference.changes) == 0 && len(difference.harness) == 0 {
		fmt.Fprintf(output, "No pinned bundle or harness file changed against %s.\n", base)
		return
	}
	fmt.Fprintf(output, "Platform bundles against %s:\n", base)
	value := func(lock *contentbundle.Lock, row lockRow) string {
		if lock == nil {
			return "-"
		}
		if text := row.values(*lock); text != "" {
			return text
		}
		return "none"
	}
	for _, change := range difference.changes {
		fmt.Fprintf(output, "%s: %s -> %s\n", change.plugin, versionOf(change.before), versionOf(change.after))
		for _, row := range rowsForChange(change)[1:] {
			before, after := value(change.before, row), value(change.after, row)
			if before != after {
				fmt.Fprintf(output, "  %-16s %s -> %s\n", strings.ToLower(row.label), before, after)
			}
		}
		for _, list := range []struct {
			label string
			keys  []string
		}{
			{"added", change.added},
			{"changed", change.changed},
			{"removed", change.removed},
			{"newly retired", change.newlyRetired},
		} {
			fmt.Fprintf(output, "  %-16s %s\n", fmt.Sprintf("%s (%d)", list.label, len(list.keys)),
				strings.Join(list.keys, ", "))
		}
	}
	for _, lock := range difference.unchanged {
		fmt.Fprintf(output, "%s: unchanged at %s\n", lock.Plugin, lock.Version)
	}
	if len(difference.harness) == 0 {
		output.WriteString("harness: unchanged\n")
	}
	for _, change := range difference.harness {
		fmt.Fprintf(output, "%s: %s\n", change.path, change.state)
	}
	if len(difference.problems) > 0 {
		output.WriteString("problems:\n")
		printProblems(output, "text", difference.problems)
	}
}

func componentDigests(bundle contentbundle.Bundle) (map[string]string, error) {
	if bundle.Lock.Component != contentbundle.ComponentTypeManagedTools {
		return workflowDigests(bundle.Files)
	}
	catalog, err := contentbundle.ReadManagedTools(bundle.Files)
	if err != nil {
		return nil, err
	}
	digests := map[string]string{}
	for _, release := range catalog.Releases {
		raw, err := json.Marshal(release)
		if err != nil {
			return nil, err
		}
		digests[release.Key] = digestBytes(raw)
	}
	return digests, nil
}

func digestBytes(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
