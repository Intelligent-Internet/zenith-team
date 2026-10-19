package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"boardly.dev/boardly/server/pkg/agent"
)

// RunHomePrefix starts the name of every Run's isolated home: the folder the
// daemon makes in the Run's env root for its BOARDLY_HOME and run.json
// (Access TECH B3b). The sandbox finds every Run's home on
// this computer by this name (B3e).
const RunHomePrefix = ".boardly-home-"

// sandboxDeniedPrograms are the programs no launch may start, because each
// asks macOS to start something later, outside the sandbox: a launchd job,
// an app or document, or AppleScript (Access TECH B3e). crontab and at need
// no rule: they are setuid, and macOS starts no setuid program inside a
// Seatbelt.
var sandboxDeniedPrograms = []string{"/bin/launchctl", "/usr/bin/open", "/usr/bin/osascript"}

// SeatbeltOptions is what one launch's macOS sandbox denies and allows
// (Access TECH B3d, B3e, B3f, B7b).
type SeatbeltOptions struct {
	// Protected lists the absolute paths that can hold a Zenith Team
	// sign-in. The provider and every process it starts can neither read
	// nor write them: a folder with everything under it, a file together
	// with the temporary siblings an atomic write leaves beside it
	// (<file>.<anything>). A path that does not exist yet is denied as a
	// folder and with its siblings, which also covers a file created there
	// later. No folder above a protected path can be renamed or replaced
	// either: Seatbelt matches paths, so moving a parent would carry the
	// files out from under the rule.
	Protected []string
	// NoWrite lists absolute paths a launch may read but never write: the
	// places macOS or the person's shell start a program from later,
	// outside the sandbox. They are matched as Protected paths are, and the
	// folders above them stay in place too.
	NoWrite []string
	// RunHome is the Run's own isolated home. Every folder on this computer
	// whose name starts with RunHomePrefix is denied, wherever it lies and
	// whatever its parents are renamed to, so no launch reads another Run's
	// run.json and calls through its channel; RunHome alone is opened again.
	// Empty for a launch that is no Run's own (an app preview, the probe).
	RunHome string
	// RunDirs are the Run's own folders: its env root, its workdir and its
	// read-only mounts. None may lie inside a protected path, because the
	// profile would deny the Run its own work; Seatbelt refuses instead.
	RunDirs []string
	// Shared lists the folders that hold every Run's work on this computer:
	// the workspaces root. A launch reads everything in them but writes only
	// inside its own folders there (Writable), so it cannot leave code or
	// notes in another Run's folders for that Run to run later (Access TECH
	// B3f). The folders above a shared folder cannot be renamed either, as
	// for a protected path.
	Shared []string
	// Writable lists the launch's own folders inside Shared: a Run's env root
	// (its scratch, isolated home and provider homes), its workdir, its Task
	// worktree's own folder in the Project main's repository and its Agent's
	// Studio sign-in folder; an app preview's working folders. A folder
	// outside every shared folder needs no rule. A folder whose real path is
	// not where its path puts it inside a shared folder (a link put in its
	// place or above it) stays read-only, as does one that holds a shared
	// folder.
	Writable []string
	// Hidden lists folders the launch may neither read nor write, nor start a
	// program from. A Run tagged in a channel or group DM hides the workspaces
	// root, which holds the Company main with its memory and every other Run's
	// folders, and every local folder the Company bound: a channel answer
	// never draws on the tagger's local folders, bound folders or private
	// Memory, which no Project access governs (Access TECH B7b). The launch's
	// own folders inside a hidden folder (RunHome, RunDirs, Writable) stay
	// open where their path puts them: it reads them and starts programs
	// there, writes the Writable ones, and may look up the folders on the way
	// to them, but not list or read anything else there. An own folder
	// reached through a link stays hidden. The folders above a hidden folder
	// cannot be renamed, as for a protected path. A hidden folder may not hold
	// the person's home folder or the temporary folder, where every provider
	// keeps its own files.
	Hidden []string
	// WritableRoots, when set, are the only trees the launch may write,
	// besides /dev. A connector-only Codex launch sets them in place of
	// Codex's own read-only Seatbelt, which cannot start inside this one.
	WritableRoots []string
}

// Seatbelt builds the sandbox for one launch. Every path is passed as a
// profile parameter, so a path with spaces or quotes (the desktop's home is
// under "Application Support") needs no escaping. Seatbelt matches the real
// path of a file, so each path is named by its real path as well as the
// spelling it was given.
//
// When several rules match one operation, the last one in the profile
// decides (checked with sandbox-exec on macOS 26.5). So the profile first
// hides the hidden folders and opens the launch's own folders there again,
// then closes the shared folders to writes and opens the launch's own
// folders there, then denies every Run's home and opens the Run's own home
// after that, and denies the sign-in paths last, where nothing reopens them.
func Seatbelt(opts SeatbeltOptions) (*agent.Sandbox, error) {
	params := make(map[string]string)
	param := func(prefix, value string) string {
		for name, existing := range params {
			if existing == value && strings.HasPrefix(name, prefix) {
				return name
			}
		}
		name := prefix + strconv.Itoa(len(params))
		params[name] = value
		return name
	}

	denied, protectedNames, err := pathFilters(opts.Protected, "PROTECTED_", param)
	if err != nil {
		return nil, err
	}
	if len(denied) == 0 {
		return nil, errors.New("execenv: a sandbox needs at least one protected path")
	}
	unwritable, noWriteNames, err := pathFilters(opts.NoWrite, "NO_WRITE_", param)
	if err != nil {
		return nil, err
	}
	var sharedNames []string
	for _, folder := range opts.Shared {
		if !filepath.IsAbs(folder) {
			return nil, fmt.Errorf("execenv: shared folder %q is not absolute", folder)
		}
		sharedNames = append(sharedNames, sandboxSpellings(folder)...)
	}
	own, _, err := inPlace(opts.Shared, opts.Writable)
	if err != nil {
		return nil, err
	}
	var hiddenNames []string
	for _, folder := range opts.Hidden {
		if !filepath.IsAbs(folder) {
			return nil, fmt.Errorf("execenv: hidden folder %q is not absolute", folder)
		}
		hiddenNames = append(hiddenNames, sandboxSpellings(folder)...)
	}
	if needed, inside := firstInside(hiddenNames, launchNeeds()); inside {
		return nil, fmt.Errorf(
			"execenv: a folder this launch may not read holds %s, which every provider needs", needed,
		)
	}
	ownInHidden := slices.Concat(opts.RunDirs, opts.Writable)
	if opts.RunHome != "" {
		ownInHidden = append(ownInHidden, opts.RunHome)
	}
	seen, onTheWay, err := inPlace(opts.Hidden, ownInHidden)
	if err != nil {
		return nil, err
	}
	written, _, err := inPlace(opts.Hidden, opts.Writable)
	if err != nil {
		return nil, err
	}
	// Every folder above a protected, unwritable, shared or hidden path, up to
	// the root, may still be created in and read, but not itself renamed,
	// removed or replaced.
	var parents []string
	for _, name := range slices.Concat(protectedNames, noWriteNames, sharedNames, hiddenNames) {
		for parent := filepath.Dir(name); ; parent = filepath.Dir(parent) {
			parents = append(parents, `(literal (param "`+param("PARENT_", parent)+`"))`)
			if parent == filepath.Dir(parent) {
				break
			}
		}
	}
	slices.Sort(parents)
	parents = slices.Compact(parents)

	if opts.RunHome != "" && !filepath.IsAbs(opts.RunHome) {
		return nil, fmt.Errorf("execenv: run home %q is not absolute", opts.RunHome)
	}
	var runNames []string
	for _, dir := range append([]string{opts.RunHome}, opts.RunDirs...) {
		if strings.TrimSpace(dir) != "" {
			runNames = append(runNames, sandboxSpellings(dir)...)
		}
	}
	if runDir, inside := firstInside(protectedNames, runNames); inside {
		return nil, fmt.Errorf("execenv: the Run's folder %s lies inside a path no Agent may read", runDir)
	}

	var profile strings.Builder
	profile.WriteString("(version 1)\n(allow default)\n")
	if len(hiddenNames) != 0 {
		// A hidden folder's own files and listing stay closed, and so do its
		// programs: macOS starts a program without the read the sandbox checks,
		// and a started program shows what it holds. The launch's own folders
		// there open again, and the folders on the way to them answer a lookup
		// (their own details, not their contents), without which no process
		// could enter its own working folder (Access TECH B7b).
		writeSubpathRule(&profile, "(deny file-read* file-write* process-exec*", hiddenNames, "HIDDEN_", param)
		if len(onTheWay) != 0 {
			profile.WriteString("(allow file-read-metadata")
			for _, name := range onTheWay {
				profile.WriteString("\n  (literal (param \"" + param("ON_THE_WAY_", name) + "\"))")
			}
			profile.WriteString(")\n")
		}
		writeSubpathRule(&profile, "(allow file-read* process-exec*", seen, "SEEN_", param)
		writeSubpathRule(&profile, "(allow file-write*", written, "OWN_", param)
	}
	writeSubpathRule(&profile, "(deny file-write*", sharedNames, "SHARED_", param)
	writeSubpathRule(&profile, "(allow file-write*", own, "OWN_", param)
	everyRunHome := `(regex #"/` + regexp.QuoteMeta(RunHomePrefix) + `[^/]*(/|$)")`
	profile.WriteString("(deny file-read* file-write* " + everyRunHome + ")\n")
	if opts.RunHome != "" {
		writeSubpathRule(
			&profile, "(allow file-read* file-write*", sandboxSpellings(opts.RunHome), "RUN_HOME_", param,
		)
	}
	profile.WriteString("(deny file-read* file-write*")
	for _, filter := range denied {
		profile.WriteString("\n  " + filter)
	}
	profile.WriteString(")\n")
	if len(unwritable) != 0 {
		profile.WriteString("(deny file-write*")
		for _, filter := range unwritable {
			profile.WriteString("\n  " + filter)
		}
		profile.WriteString(")\n")
	}
	profile.WriteString("(deny file-write*")
	for _, filter := range parents {
		profile.WriteString("\n  " + filter)
	}
	profile.WriteString(")\n(deny process-exec*")
	for _, program := range sandboxDeniedPrograms {
		profile.WriteString("\n  (literal (param \"" + param("PROGRAM_", program) + "\"))")
	}
	profile.WriteString(")\n")
	if len(opts.WritableRoots) != 0 {
		profile.WriteString("(deny file-write*\n  (require-all\n    (require-not (subpath \"/dev\"))")
		for _, root := range opts.WritableRoots {
			if !filepath.IsAbs(root) {
				return nil, fmt.Errorf("execenv: writable root %q is not absolute", root)
			}
			for _, name := range sandboxSpellings(root) {
				profile.WriteString(
					"\n    (require-not (subpath (param \"" + param("WRITABLE_", name) + "\")))",
				)
			}
		}
		profile.WriteString("))\n")
	}
	return &agent.Sandbox{Profile: profile.String(), Params: params}, nil
}

// SandboxDeniesRead reports whether the sandbox opts builds would deny the
// launch reading path, the real path of a file: a path in any Run's isolated
// home; a protected path, anything under it, or a temporary sibling of it;
// or a path in a hidden folder outside the launch's own folders there. The
// daemon itself runs outside the sandbox, so it asks this before it reads a
// file a Run named (Access TECH B3e, B7b). It denies at least what the
// profile denies, and it opens no Run's own home either. It reads only
// Protected, Hidden, RunDirs and Writable.
func SandboxDeniesRead(opts SeatbeltOptions, path string) bool {
	path = filepath.Clean(path)
	for part := range strings.SplitSeq(path, string(filepath.Separator)) {
		if strings.HasPrefix(part, RunHomePrefix) {
			return true
		}
	}
	for _, protectedPath := range opts.Protected {
		for _, name := range sandboxSpellings(protectedPath) {
			if within(name, path) || strings.HasPrefix(path, name+".") {
				return true
			}
		}
	}
	hidden := false
	for _, folder := range opts.Hidden {
		for _, name := range sandboxSpellings(folder) {
			hidden = hidden || within(name, path)
		}
	}
	if !hidden {
		return false
	}
	open, _, err := inPlace(opts.Hidden, slices.Concat(opts.RunDirs, opts.Writable))
	if err != nil {
		return true
	}
	return !slices.ContainsFunc(open, func(folder string) bool { return within(folder, path) })
}

// within reports whether path is folder or lies under it.
func within(folder, path string) bool {
	return path == folder || strings.HasPrefix(path, folder+string(filepath.Separator))
}

// inPlace returns the spellings, as Seatbelt must name them (the cleaned path
// and its real path), of the folders that lie inside a container where their
// path says, and of the folders on the way to them from each such container,
// the container included. Containers may nest (a bound folder that holds the
// workspaces root), so every container that holds a folder counts. A folder
// outside every container is left out, and so is one that holds a container.
// So is a folder whose real path is not the container's real path with the
// same rest: it was reached through a link, put in its place or above it by
// an earlier launch, and opening it would open whatever the link names,
// perhaps another Run's folder. A folder may be spelled under the
// container's real path (an app preview's folder is kept by its real path).
func inPlace(containers, folders []string) (inside, onTheWay []string, err error) {
	for _, folder := range folders {
		if strings.TrimSpace(folder) == "" {
			continue
		}
		if !filepath.IsAbs(folder) {
			return nil, nil, fmt.Errorf("execenv: folder %q is not absolute", folder)
		}
		folder = filepath.Clean(folder)
		for _, container := range containers {
			container = filepath.Clean(container)
			realContainer := realPath(container)
			rest, err := filepath.Rel(container, folder)
			if err != nil || !filepath.IsLocal(rest) {
				rest, err = filepath.Rel(realContainer, folder)
			}
			if err != nil || rest == "." || !filepath.IsLocal(rest) {
				continue
			}
			if realPath(folder) != filepath.Join(realContainer, rest) {
				continue
			}
			inside = append(inside, sandboxSpellings(folder)...)
			onTheWay = append(onTheWay, container, realContainer)
			step := ""
			for part := range strings.SplitSeq(filepath.Dir(rest), string(filepath.Separator)) {
				if part == "." {
					break
				}
				step = filepath.Join(step, part)
				onTheWay = append(
					onTheWay,
					filepath.Join(container, step),
					filepath.Join(realContainer, step),
				)
			}
		}
	}
	slices.Sort(inside)
	slices.Sort(onTheWay)
	return slices.Compact(inside), slices.Compact(onTheWay), nil
}

// writeSubpathRule writes one rule that names each folder as a subpath
// through a parameter, or nothing when there is no folder.
func writeSubpathRule(
	profile *strings.Builder,
	rule string,
	folders []string,
	prefix string,
	param func(prefix, value string) string,
) {
	if len(folders) == 0 {
		return
	}
	profile.WriteString(rule)
	for _, name := range folders {
		profile.WriteString("\n  (subpath (param \"" + param(prefix, name) + "\"))")
	}
	profile.WriteString(")\n")
}

// launchNeeds are the spellings of the folders every launch uses outside its
// own: the person's home folder, where each provider keeps its home, and the
// temporary folder. No hidden folder may hold them.
func launchNeeds() []string {
	var needs []string
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		needs = append(needs, sandboxSpellings(home)...)
	}
	return append(needs, sandboxSpellings(os.TempDir())...)
}

// pathFilters names each path under every spelling: a folder as a subpath,
// a file as a literal plus the temporary siblings an atomic write leaves
// beside it, and a path that does not exist yet as a subpath plus those
// siblings. It returns the filters and the spellings.
func pathFilters(
	paths []string,
	prefix string,
	param func(prefix, value string) string,
) ([]string, []string, error) {
	var filters, names []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return nil, nil, fmt.Errorf("execenv: sandbox path %q is not absolute", path)
		}
		info, err := os.Stat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("execenv: inspect sandbox path: %w", err)
		}
		for _, name := range sandboxSpellings(path) {
			names = append(names, name)
			filter := "subpath"
			if err == nil && !info.IsDir() {
				filter = "literal"
			}
			filters = append(filters, `(`+filter+` (param "`+param(prefix, name)+`"))`)
			if err != nil || !info.IsDir() {
				filters = append(filters, `(prefix (param "`+param(prefix, name+".")+`"))`)
			}
		}
	}
	return filters, names, nil
}

// sandboxSpellings is path cleaned and, when it differs, its real path. The
// real path of a path that does not exist yet is its nearest existing
// parent's real path with the rest appended, so /var/... still becomes
// /private/var/... on macOS.
func sandboxSpellings(path string) []string {
	path = filepath.Clean(path)
	resolved := realPath(path)
	if resolved == path {
		return []string{path}
	}
	return []string{path, resolved}
}

func realPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(realPath(parent), filepath.Base(path))
}
