package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"

	"gopkg.in/yaml.v3"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
	"github.com/Intelligent-Internet/zenith-team/server/internal/platformbundle"
	"github.com/Intelligent-Internet/zenith-team/server/internal/workflow"
)

// The Zenith Team parts of a plugin, relative to plugins/<name>/ (ADR 0016).
const (
	manifestPath     = ".boardly-plugin/plugin.json"
	retiredKeysPath  = ".boardly-plugin/retired-keys.json"
	workflowsPath    = "boardly/workflows"
	managedToolsPath = "boardly/managed-tools"
)

var (
	// Plugin names and Workflow keys share the loader's kebab-case pattern.
	namePattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	versionPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)
	linePattern    = regexp.MustCompile(`\bline (\d+)\b`)
	workflowPrefix = regexp.MustCompile(`^workflow "([^"]+)": `)
	// The parser opens each rule about a form field with the field's name.
	fieldPattern = regexp.MustCompile(`^[A-Za-z]+`)
)

// problem is one rule a plugin breaks. file is the path to annotate, the
// plugin as the caller named it joined with the file's path inside it, or
// empty for a problem no one file carries.
type problem struct {
	file    string
	message string
	// at is the line of the form field the problem names, when its message
	// names no line of its own.
	at int
}

// line is the line a YAML or JSON error names, or the form field's line, or 0.
func (p problem) line() int {
	match := linePattern.FindStringSubmatch(p.message)
	if match == nil && p.at > 0 {
		return p.at
	}
	if match == nil {
		return 0
	}
	line, _ := strconv.Atoi(match[1])
	return line
}

// problemsError fails a command after its problems were printed.
type problemsError struct{ problems []problem }

func (err *problemsError) Error() string {
	if len(err.problems) == 1 {
		return "1 problem found"
	}
	return fmt.Sprintf("%d problems found", len(err.problems))
}

// printProblems writes problems as text (path:line: message) or as GitHub
// workflow commands, which annotate the file and line in a pull request.
func printProblems(output io.Writer, format string, problems []problem) {
	for _, p := range problems {
		line := p.line()
		if format == "github" {
			properties := ""
			if p.file != "" {
				properties = " file=" + escapeProperty(p.file)
				if line > 0 {
					properties += ",line=" + strconv.Itoa(line)
				}
			}
			fmt.Fprintf(output, "::error%s::%s\n", properties, escapeData(p.message))
			continue
		}
		location := p.file
		if location != "" && line > 0 {
			location += ":" + strconv.Itoa(line)
		}
		message := strings.ReplaceAll(p.message, "\n", "\n    ")
		if location == "" {
			fmt.Fprintln(output, message)
		} else {
			fmt.Fprintf(output, "%s: %s\n", location, message)
		}
	}
}

// escapeData and escapeProperty follow the GitHub Actions workflow command
// encoding, so a message or a path cannot end the command early.
func escapeData(value string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(value)
}

func escapeProperty(value string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(value)
}

// pluginManifest is the strict shape of .boardly-plugin/plugin.json.
type pluginManifest struct {
	Format       int                `json:"format"`
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	Description  string             `json:"description"`
	Workflows    *manifestComponent `json:"workflows,omitempty"`
	ManagedTools *manifestComponent `json:"managedTools,omitempty"`
}

type manifestComponent struct {
	Path string `json:"path"`
}

// retiredKeysFile is the strict shape of .boardly-plugin/retired-keys.json.
type retiredKeysFile struct {
	RetiredKeys *[]string `json:"retiredKeys"`
}

// parseManifest applies the manifest rules; the version is any X.Y.Z here,
// and an import compares it with its tag.
func parseManifest(raw []byte, plugin string) (pluginManifest, []string) {
	var manifest pluginManifest
	if err := contentbundle.DecodeJSON(raw, &manifest); err != nil {
		return manifest, []string{err.Error()}
	}
	var problems []string
	if manifest.Format != 1 {
		problems = append(problems, fmt.Sprintf("format must be 1, got %d", manifest.Format))
	}
	if manifest.Name != plugin {
		problems = append(problems, fmt.Sprintf("name %q must be the plugin's name %q", manifest.Name, plugin))
	}
	if !versionPattern.MatchString(manifest.Version) {
		problems = append(problems, fmt.Sprintf("version %q must be X.Y.Z", manifest.Version))
	}
	if strings.TrimSpace(manifest.Description) == "" {
		problems = append(problems, "description must not be empty")
	}
	switch {
	case manifest.Workflows == nil && manifest.ManagedTools == nil:
		problems = append(
			problems,
			`workflows.path must be "./boardly/workflows" or managedTools.path must be "./boardly/managed-tools"`,
		)
	case manifest.Workflows != nil && manifest.ManagedTools != nil:
		problems = append(problems, "a plugin must declare exactly one Zenith Team component")
	case manifest.ManagedTools != nil && manifest.ManagedTools.Path != "./"+managedToolsPath:
		problems = append(problems, `managedTools.path must be "./boardly/managed-tools"`)
	case manifest.Workflows != nil && manifest.Workflows.Path != "./"+workflowsPath:
		problems = append(problems, `workflows.path must be "./boardly/workflows"`)
	}
	return manifest, problems
}

// parseRetiredKeys applies the retired-keys rules: the list is present,
// sorted and unique, and names Workflow keys.
func parseRetiredKeys(raw []byte) ([]string, []string) {
	var file retiredKeysFile
	if err := contentbundle.DecodeJSON(raw, &file); err != nil {
		return nil, []string{err.Error()}
	}
	if file.RetiredKeys == nil {
		return nil, []string{"retiredKeys is required"}
	}
	keys := *file.RetiredKeys
	var problems []string
	if !slices.IsSorted(keys) || len(slices.Compact(slices.Clone(keys))) != len(keys) {
		problems = append(problems, "retiredKeys must be sorted and unique")
	}
	for _, key := range keys {
		if !namePattern.MatchString(key) {
			problems = append(problems, fmt.Sprintf("retired key %q is not a Workflow key", key))
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return keys, nil
}

// pluginSource is one plugin's Zenith Team parts as its author wrote them, read
// from a folder on disk or from a tagged commit.
type pluginSource struct {
	// display is the plugin's folder as problems name it: the argument the
	// caller gave, or plugins/<name> inside the repository.
	display     string
	name        string
	manifestRaw []byte
	manifest    pluginManifest
	retiredRaw  []byte
	retiredKeys []string
	// files are relative to the declared component's root.
	files map[string][]byte
	// identity carries the lock fields the source fixes: version, tag,
	// commit, manifest sha256 and content tree. The rest is computed.
	identity contentbundle.Lock
	problems []problem
}

func (source pluginSource) component() contentbundle.ComponentType {
	if source.manifest.ManagedTools != nil {
		return contentbundle.ComponentTypeManagedTools
	}
	return contentbundle.ComponentTypeWorkflows
}

func (source pluginSource) contentPath() string { return contentbundle.ContentPath(source.component()) }

// acceptsFile binds all Zenith Team content to the manifest's single component.
func (source *pluginSource) acceptsFile(name string) bool {
	if strings.HasPrefix(name, source.contentPath()+"/") {
		return true
	}
	if name != manifestPath && name != retiredKeysPath {
		source.fail(name, "is outside the declared component")
	}
	return false
}

func (source *pluginSource) fail(file, format string, args ...any) {
	location := ""
	if file != "" {
		location = source.display + "/" + file
	}
	source.problems = append(source.problems, problem{file: location, message: fmt.Sprintf(format, args...)})
}

// readMetadata parses the manifest and the optional retired keys and records
// their problems.
func (source *pluginSource) readMetadata(manifestRaw, retiredRaw []byte) {
	source.manifestRaw = manifestRaw
	if manifestRaw != nil {
		var problems []string
		source.manifest, problems = parseManifest(manifestRaw, source.name)
		for _, message := range problems {
			source.fail(manifestPath, "%s", message)
		}
		digest := sha256.Sum256(manifestRaw)
		source.identity.ManifestSHA256 = hex.EncodeToString(digest[:])
	}
	source.retiredRaw = retiredRaw
	source.retiredKeys = []string{}
	if retiredRaw != nil {
		keys, problems := parseRetiredKeys(retiredRaw)
		for _, message := range problems {
			source.fail(retiredKeysPath, "%s", message)
		}
		if keys != nil {
			source.retiredKeys = keys
		}
	}
}

// readPluginDir reads a plugin folder from disk as a commit of it would carry
// it: regular files only, never through a symlink inside the plugin, and
// never an executable file, which a release import refuses at the tag.
// Inside a git work tree it leaves out the files git ignores, such as a
// Finder .DS_Store, since no commit carries them either.
func readPluginDir(ctx context.Context, dir, display string) pluginSource {
	source := pluginSource{display: display, files: map[string][]byte{}}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		source.fail("", "%v", err)
		return source
	}
	source.name = filepath.Base(absolute)
	if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
		source.fail("", "%s is not a plugin folder", display)
		return source
	}
	if !namePattern.MatchString(source.name) || len(source.name) > 64 {
		source.fail("", "plugin folder %q must be a kebab-case name of at most 64 characters", source.name)
	}
	for _, inner := range []string{".boardly-plugin", "boardly", workflowsPath, managedToolsPath} {
		info, err := os.Lstat(filepath.Join(absolute, inner))
		if err == nil && info.Mode()&fs.ModeSymlink != 0 {
			source.fail(inner, "is a symlink; a plugin's Zenith Team folders are real directories")
			return source
		}
	}
	names, err := pluginFileNames(ctx, absolute)
	if err != nil {
		source.fail("", "%v", err)
		return source
	}
	// read returns a file's bytes, or nil after recording why it cannot. A
	// file git still tracks but the working tree deleted counts as absent.
	read := func(name string) ([]byte, bool) {
		info, err := os.Lstat(filepath.Join(absolute, filepath.FromSlash(name)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil, false
		case err != nil:
			source.fail(name, "%v", err)
		case info.Mode()&fs.ModeSymlink != 0:
			source.fail(name, "is a symlink; plugin files are regular files")
		case !info.Mode().IsRegular():
			source.fail(name, "is not a regular file")
		case info.Mode().Perm()&0o111 != 0:
			source.fail(name, "is executable; plugin files are mode 644")
		default:
			contents, err := os.ReadFile(filepath.Join(absolute, filepath.FromSlash(name)))
			if err != nil {
				source.fail(name, "%v", err)
				return nil, true
			}
			return contents, true
		}
		return nil, true
	}
	var manifestRaw, retiredRaw []byte
	manifestFound := false
	if slices.Contains(names, manifestPath) {
		manifestRaw, manifestFound = read(manifestPath)
	}
	if !manifestFound {
		source.fail(manifestPath, "is missing")
	}
	if slices.Contains(names, retiredKeysPath) {
		retiredRaw, _ = read(retiredKeysPath)
	}
	source.readMetadata(manifestRaw, retiredRaw)
	if info, err := os.Stat(filepath.Join(absolute, source.contentPath())); err != nil || !info.IsDir() {
		source.fail(source.contentPath(), "is missing; a plugin ships its declared component there")
		return source
	}
	for _, name := range names {
		if !source.acceptsFile(name) {
			continue
		}
		if relative, ok := strings.CutPrefix(name, source.contentPath()+"/"); ok {
			if contents, _ := read(name); contents != nil {
				source.files[relative] = contents
			}
		}
	}
	return source
}

// pluginFileNames lists the files under a plugin's .boardly-plugin and
// boardly/workflows folders, relative to the plugin and slash-separated, in
// order. In a git work tree that is what git would commit, tracked files and
// untracked ones it does not ignore; anywhere else it is every file. A
// directory is listed only where git lists one, for a submodule or a nested
// repository.
func pluginFileNames(ctx context.Context, absolute string) ([]string, error) {
	var names []string
	inside, err := runGit(ctx, absolute, nil, "rev-parse", "--is-inside-work-tree")
	if err == nil && strings.TrimSpace(string(inside)) == "true" {
		output, err := runGit(
			ctx,
			absolute,
			nil,
			"ls-files",
			"-z",
			"--cached",
			"--others",
			"--exclude-standard",
			"--",
			".boardly-plugin",
			"boardly",
		)
		if err != nil {
			return nil, err
		}
		for name := range strings.SplitSeq(strings.TrimSuffix(string(output), "\x00"), "\x00") {
			if name != "" {
				names = append(names, strings.TrimSuffix(name, "/"))
			}
		}
	} else {
		for _, folder := range []string{".boardly-plugin", "boardly"} {
			root := filepath.Join(absolute, folder)
			err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
				switch {
				case name == root && errors.Is(walkErr, fs.ErrNotExist):
					return nil
				case walkErr != nil:
					return walkErr
				case entry.IsDir():
					return nil
				}
				relative, err := filepath.Rel(absolute, name)
				names = append(names, filepath.ToSlash(relative))
				return err
			})
			if err != nil {
				return nil, err
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// readPluginCommit reads a plugin from a commit's objects: the raw blobs,
// never a checkout, so no eol conversion or filter touches them. Every file it
// reads must be a plain 100644 blob.
func readPluginCommit(ctx context.Context, repo *gitRepository, commit, plugin string, stderr io.Writer) (
	pluginSource, error,
) {
	root := "plugins/" + plugin
	source := pluginSource{display: root, name: plugin, files: map[string][]byte{}}
	source.identity.Commit = commit
	if err := repo.prefetch(ctx, commit+":"+root); err != nil {
		// A partial clone still reads each missing blob on demand, one by one.
		fmt.Fprintf(stderr, "bundlectl: warning: could not prefetch the plugin's blobs: %v\n", err)
	}
	entries, err := repo.listTree(ctx, commit, root+"/.boardly-plugin", root+"/boardly")
	if err != nil {
		return source, err
	}
	var ids []string
	seen := map[string]bool{}
	readable := map[string]string{} // path inside the plugin → blob id
	for _, entry := range entries {
		relative := strings.TrimPrefix(entry.path, root+"/")
		if relative == ".boardly-plugin" || relative == "boardly" || relative == workflowsPath ||
			relative == managedToolsPath {
			// ls-tree lists a folder by its own path only when it is not a
			// tree: a symlink, a file or a submodule in its place.
			source.fail(
				relative,
				"has git mode %s; a plugin's Zenith Team folders are real directories",
				entry.mode,
			)
			continue
		}
		if !strings.HasPrefix(relative, "boardly/") && relative != manifestPath &&
			relative != retiredKeysPath {
			continue
		}
		seen[relative] = true
		switch entry.mode {
		case "100644":
			readable[relative] = entry.id
			ids = append(ids, entry.id)
		case "120000":
			source.fail(relative, "is a symlink; plugin files are regular files")
		case "160000":
			source.fail(relative, "is a submodule; plugin files are regular files")
		case "100755":
			source.fail(relative, "is executable (mode 100755); plugin files are mode 100644")
		default:
			source.fail(relative, "has git mode %s; plugin files are mode 100644", entry.mode)
		}
	}
	slices.Sort(ids)
	blobs, err := repo.readBlobs(ctx, slices.Compact(ids))
	if err != nil {
		return source, err
	}
	blob := func(relative string) []byte {
		if id, ok := readable[relative]; ok {
			return blobs[id]
		}
		return nil
	}
	if !seen[manifestPath] {
		source.fail(manifestPath, "is missing at commit %s", commit)
	}
	source.readMetadata(blob(manifestPath), blob(retiredKeysPath))
	for relative, id := range readable {
		if !source.acceptsFile(relative) {
			continue
		}
		if name, ok := strings.CutPrefix(relative, source.contentPath()+"/"); ok {
			source.files[name] = blobs[id]
		}
	}
	// "<commit>:<path>^{tree}" would read "^{tree}" as part of the path, so
	// the id and its type are asked for apart. Anything but a tree has been
	// reported above.
	output, err := repo.git(ctx, "rev-parse", "--verify", "--quiet", commit+":"+root+"/"+source.contentPath())
	switch {
	case exitedWith(err, 1):
		source.fail(source.contentPath(), "is missing at commit %s", commit)
	case err != nil:
		return source, err
	default:
		id := strings.TrimSpace(string(output))
		kind, err := repo.git(ctx, "cat-file", "-t", id)
		if err != nil {
			return source, err
		}
		if strings.TrimSpace(string(kind)) == "tree" {
			source.identity.ContentTreeID = id
		}
	}
	return source, nil
}

// built is one component that passed every rule: its lock, its canonical
// artifact and the bundle Decode returned for them.
type built struct {
	bundle   contentbundle.Bundle
	artifact []byte
}

// pinnedBundles splits the bundles this build pins: others, for every plugin
// but the given ones, is the rest of the catalog those plugins must merge
// with, and own holds the given plugins' own pins. A pin that does not
// verify is a problem, unless it belongs to one of the given plugins, which
// are being replaced: a broken pin is exactly what a new import repairs.
func pinnedBundles(plugins []string) ([]contentbundle.Bundle, map[string]*contentbundle.Bundle, []problem) {
	bundles, err := platformbundle.Load()
	return splitPinned(bundles, err, plugins)
}

// splitPinned is pinnedBundles over what a load returned.
func splitPinned(bundles []contentbundle.Bundle, err error, plugins []string) (
	[]contentbundle.Bundle, map[string]*contentbundle.Bundle, []problem,
) {
	var problems []problem
	failures := []error{err}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		failures = joined.Unwrap()
	}
	for _, failure := range failures {
		var loadErr *contentbundle.LoadError
		if failure == nil || errors.As(failure, &loadErr) && slices.Contains(plugins, loadErr.Plugin) {
			continue
		}
		problems = append(problems, problem{message: fmt.Sprintf("a bundle this build pins does not verify: %v",
			failure)})
	}
	var others []contentbundle.Bundle
	own := map[string]*contentbundle.Bundle{}
	for index, bundle := range bundles {
		if slices.Contains(plugins, bundle.Lock.Plugin) {
			own[bundle.Lock.Plugin] = &bundles[index]
		} else {
			others = append(others, bundle)
		}
	}
	return others, own, problems
}

// buildPlugins applies every production rule to the sources, together with
// the pinned bundles of other plugins, and reports every problem:
//   - the content rules the loader enforces (contentbundle.CheckFiles);
//   - no retired key in use, by any plugin; no key in two plugins; and no
//     plugin retiring a key another one ships, since keys never move;
//   - an Encode and Decode round trip with the source's lock;
//   - the merged catalog through the Workflow parser (workflow.CheckCatalog)
//     and the platform-skill loader (tools.PlatformSkills).
//
// It returns the components that were built only when there is no problem.
func buildPlugins(sources []pluginSource, pinned []contentbundle.Bundle) ([]built, []problem) {
	var problems []problem
	owners := map[string]string{}    // Workflow key → plugin
	locations := map[string]string{} // Workflow key → its folder, for a source
	retiredBy := map[string]string{} // retired key → plugin
	shippedBy := map[string]string{} // Workflow key → a plugin that ships it, pinned or checked
	catalog := slices.Clone(pinned)
	for _, bundle := range pinned {
		for _, key := range bundleKeys(bundle.Files) {
			owners[key] = bundle.Lock.Plugin
			shippedBy[key] = bundle.Lock.Plugin
		}
		for _, key := range bundle.Lock.RetiredKeys {
			retiredBy[key] = bundle.Lock.Plugin
		}
	}
	names := map[string]string{}
	for _, source := range sources {
		if previous, taken := names[source.name]; taken && source.name != "" {
			problems = append(problems, problem{message: fmt.Sprintf("%s and %s are both plugin %q",
				previous, source.display, source.name)})
		}
		names[source.name] = source.display
		for _, key := range source.retiredKeys {
			retiredBy[key] = source.name
		}
		for _, key := range keysOf(allowedFiles(source.files)) {
			if _, taken := shippedBy[key]; !taken {
				shippedBy[key] = source.name
			}
		}
	}
	var components []built
	for _, source := range sources {
		sourceProblems := slices.Clone(source.problems)
		fail := func(file, format string, args ...any) {
			sourceProblems = append(
				sourceProblems,
				problem{file: file, message: fmt.Sprintf(format, args...)},
			)
		}
		for _, key := range source.retiredKeys {
			if shipper, shipped := shippedBy[key]; shipped && shipper != source.name {
				fail(
					source.display+"/"+retiredKeysPath,
					"retires Workflow %q, which plugin %s ships; a "+
						"plugin retires only its own keys, and a key never moves between plugins",
					key,
					shipper,
				)
			}
		}
		if source.component() == contentbundle.ComponentTypeManagedTools {
			if len(source.retiredKeys) != 0 {
				fail(source.display+"/"+retiredKeysPath, "managed tools cannot retire Workflow keys")
			}
			if len(sourceProblems) == 0 {
				component, err := encodeComponent(source)
				if err != nil {
					fail(source.display+"/"+source.contentPath(), "%v", err)
				} else {
					components = append(components, component)
				}
			}
			problems = append(problems, sourceProblems...)
			continue
		}
		contentRoot := source.display + "/" + workflowsPath
		var contentProblems []error
		// A plugin whose content could not be read has already said why.
		if len(source.files) > 0 || len(source.problems) == 0 {
			contentProblems = contentbundle.CheckFiles(contentbundle.ComponentTypeWorkflows, source.files)
		}
		for _, err := range contentProblems {
			var fileErr *contentbundle.FileError
			if errors.As(err, &fileErr) {
				fail(contentRoot+"/"+fileErr.Name, "%v", err)
			} else {
				fail(contentRoot, "%v", err)
			}
		}
		allowed := allowedFiles(source.files)
		for _, key := range keysOf(allowed) {
			folder := contentRoot + "/" + key
			if retirer, retired := retiredBy[key]; retired {
				if retirer == source.name {
					fail(
						folder,
						"Workflow %q is listed in %s; a retired key is never reused",
						key,
						retiredKeysPath,
					)
				} else {
					fail(
						folder,
						"Workflow %q is retired by plugin %s; a retired key is never reused",
						key,
						retirer,
					)
					// Left in, it would fail the merge below and hide every
					// other problem.
					dropKey(allowed, key)
					continue
				}
			}
			if owner, taken := owners[key]; taken && owner != source.name {
				fail(
					folder,
					"Workflow %q is also shipped by plugin %s; keys are unique across plugins",
					key,
					owner,
				)
				dropKey(allowed, key)
				continue
			}
			owners[key] = source.name
			locations[key] = folder
		}
		catalogBundle := contentbundle.Bundle{
			Lock: contentbundle.Lock{
				Plugin:    source.name,
				Component: contentbundle.ComponentTypeWorkflows,
			},
			Files: contentbundle.NewFS(allowed),
		}
		if len(sourceProblems) == 0 {
			component, err := encodeComponent(source)
			if err != nil {
				fail(contentRoot, "%v", err)
			} else {
				components = append(components, component)
				catalogBundle = component.bundle
			}
		}
		problems = append(problems, sourceProblems...)
		catalog = append(catalog, catalogBundle)
	}
	merged, _, err := platformbundle.MergeWorkflowCatalog(catalog)
	if err != nil {
		problems = append(problems, problem{message: err.Error()})
		return nil, problems
	}
	// A catalog left with no Workflow after problems that already say why,
	// such as a content folder that is a symlink, is not reported as empty.
	if len(problems) == 0 || len(bundleKeys(merged)) > 0 {
		for _, err := range workflow.CheckCatalog(merged) {
			problems = append(problems, catalogProblems(err, owners, locations, merged)...)
		}
	}
	if _, err := tools.PlatformSkills([]tools.PlatformBundle{platformbundle.WorkflowsSkill(merged)}); err != nil {
		problems = append(problems, problem{message: fmt.Sprintf(
			"the merged %s skill does not load under the platform-skill rules: %v",
			tools.ZenithWorkflowsSkillKey,
			err,
		)})
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return components, nil
}

// encodeComponent writes the canonical artifact and the lock for a source
// and reads them back through the loader, so a pin bundlectl writes is one
// the server accepts.
func encodeComponent(source pluginSource) (built, error) {
	artifact, err := contentbundle.Encode(source.component(), source.files)
	if err != nil {
		return built{}, err
	}
	lock := source.identity
	lock.LockFormat = 1
	lock.Plugin = source.name
	lock.Component = source.component()
	lock.ManifestPath = "plugins/" + source.name + "/" + manifestPath
	lock.ContentPath = "plugins/" + source.name + "/" + source.contentPath()
	lock.TreeHash = contentbundle.TreeHash(source.files)
	lock.FileCount = len(source.files)
	lock.RetiredKeys = source.retiredKeys
	if lock.Component == contentbundle.ComponentTypeManagedTools {
		catalog, err := contentbundle.ReadManagedTools(contentbundle.NewFS(source.files))
		if err != nil {
			return built{}, err
		}
		lock.ManagedTools = &catalog
		lock.TreeHash = contentbundle.TreeHash(map[string][]byte{"release.json": artifact})
		artifact = nil
	} else {
		digest := sha256.Sum256(artifact)
		lock.Artifact = contentbundle.Artifact{
			File:   source.name + contentbundle.ArtifactSuffix,
			SHA256: hex.EncodeToString(digest[:]),
			Size:   int64(len(artifact)),
		}
	}
	bundle, err := contentbundle.Decode(lock, artifact)
	if err != nil {
		return built{}, err
	}
	return built{bundle: bundle, artifact: artifact}, nil
}

// catalogProblems splits one workflow.CheckCatalog error into problems, one
// per rule, each at the file it concerns: the form, the playbook, or the
// Workflow's folder. A YAML type error becomes one problem per line it names;
// a rule about one form field points at that field's line.
func catalogProblems(err error, owners, locations map[string]string, catalog fs.FS) []problem {
	match := workflowPrefix.FindStringSubmatch(err.Error())
	if match == nil {
		return []problem{{message: err.Error()}}
	}
	key := match[1]
	prefix := match[0]
	folder, checked := locations[key]
	if !checked {
		prefix = fmt.Sprintf("%s(pinned in plugin %s) ", prefix, owners[key])
	}
	form, _ := fs.ReadFile(catalog, key+"/workflow.yaml")
	lines := fieldLines(form)
	parts := []error{err}
	if inner := errors.Unwrap(err); inner != nil {
		parts = []error{inner}
		if joined, ok := inner.(interface{ Unwrap() []error }); ok {
			parts = joined.Unwrap()
		}
	}
	var problems []problem
	add := func(message string) {
		message = strings.TrimPrefix(message, match[0])
		file, at := "", 0
		if checked {
			switch {
			case strings.Contains(message, "directory must hold exactly"):
				file = folder
			case strings.HasPrefix(message, "playbook.md"):
				file = folder + "/playbook.md"
			default:
				file = folder + "/workflow.yaml"
				at = lines[fieldPattern.FindString(message)]
			}
		}
		problems = append(problems, problem{file: file, message: prefix + message, at: at})
	}
	for _, part := range parts {
		var typeErr *yaml.TypeError
		if errors.As(part, &typeErr) && len(typeErr.Errors) > 0 {
			for _, detail := range typeErr.Errors {
				add("workflow.yaml: " + strings.TrimSpace(detail))
			}
			continue
		}
		add(part.Error())
	}
	return problems
}

// fieldLines maps each top-level field of a form to its line, or returns nil
// for a form that does not parse (its error names the line itself).
func fieldLines(form []byte) map[string]int {
	var document yaml.Node
	if yaml.Unmarshal(form, &document) != nil || len(document.Content) == 0 ||
		document.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	mapping := document.Content[0].Content
	lines := make(map[string]int, len(mapping)/2)
	for index := 0; index+1 < len(mapping); index += 2 {
		lines[mapping[index].Value] = mapping[index].Line
	}
	return lines
}

// allowedFiles returns the files at paths the component admits, asking the
// loader's own rules: an empty file breaks no text rule, so any problem is
// the path's.
func allowedFiles(files map[string][]byte) map[string][]byte {
	allowed := map[string][]byte{}
	for name, contents := range files {
		empty := map[string][]byte{name: nil}
		if len(contentbundle.CheckFiles(contentbundle.ComponentTypeWorkflows, empty)) == 0 {
			allowed[name] = contents
		}
	}
	return allowed
}

// dropKey removes one Workflow's files.
func dropKey(files map[string][]byte, key string) {
	for name := range files {
		if strings.HasPrefix(name, key+"/") {
			delete(files, name)
		}
	}
}

// keysOf returns the sorted Workflow keys of a component's files.
func keysOf(files map[string][]byte) []string {
	keys := make([]string, 0, len(files))
	for name := range files {
		key, _, _ := strings.Cut(name, "/")
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// bundleKeys returns the sorted Workflow keys of a verified bundle.
func bundleKeys(files fs.FS) []string {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			keys = append(keys, entry.Name())
		}
	}
	return keys
}

// workflowDigests maps each Workflow key of a bundle to a hash of its files,
// so two pins can be compared key by key.
func workflowDigests(files fs.FS) (map[string]string, error) {
	grouped := map[string]map[string][]byte{}
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		contents, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		key, _, _ := strings.Cut(name, "/")
		if grouped[key] == nil {
			grouped[key] = map[string][]byte{}
		}
		grouped[key][name] = contents
		return nil
	})
	digests := make(map[string]string, len(grouped))
	for key, group := range grouped {
		digests[key] = contentbundle.TreeHash(group)
	}
	return digests, err
}
