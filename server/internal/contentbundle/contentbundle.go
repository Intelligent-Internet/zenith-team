// Package contentbundle verifies release-pinned content from zenith-team-plugins.
// It owns the lock and Workflow archive formats without embedding release assets. This
// package exists for the cmd/zen link set: the daemon needs managed Tool
// bundles, while the server and MCP alone link the Workflow catalog.
package contentbundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrInvalid reports pinned content that fails verification: a lock that is
// not canonical, an artifact that does not match its lock, or a file the
// component does not allow.
var ErrInvalid = errors.New("platform bundle is invalid")

// ComponentType is the closed set of Zenith Team components a plugin can ship.
type ComponentType string

// Supported release components. Workflows carry forms and playbooks;
// managedTools carry executable release descriptors and reviewed skill references.
const (
	ComponentTypeWorkflows    ComponentType = "workflows"
	ComponentTypeManagedTools ComponentType = "managedTools"
)

// ContentPath is the component's fixed location inside a plugin.
func ContentPath(component ComponentType) string {
	switch component {
	case ComponentTypeWorkflows:
		return "boardly/workflows"
	case ComponentTypeManagedTools:
		return "boardly/managed-tools"
	default:
		return ""
	}
}

// Each plugin has a lock; Workflow pins also have an archive beside it.
const (
	ArtifactSuffix = ".bundle.tar"
	LockSuffix     = ".lock.json"
)

// Artifact limits. A Workflow catalog is text: each file stays under 1 MiB
// and the whole component under the platform-skill package cap.
const (
	maxArtifactFiles     = 2_000
	maxArtifactFileBytes = 1 << 20
	maxArtifactBytes     = 128 << 20
)

var (
	pluginNamePattern     = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	releaseVersionPattern = regexp.MustCompile(`^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)
	// A local import (bundlectl import --dev) pins a checkout, never a tag.
	// TestPinnedBundlesAreReleases keeps one from being committed.
	devVersionPattern   = regexp.MustCompile(`^0\.0\.0-dev\+[0-9a-f]{12}(?:\.dirty)?$`)
	gitObjectPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	workflowFilePattern = regexp.MustCompile(`^([a-z0-9]+(?:-[a-z0-9]+)*)/(?:workflow\.yaml|playbook\.md)$`)
)

// Lock pins one component of one zenith-team-plugins release. bundlectl import
// writes it with MarshalLock; Load refuses a lock that is not byte-identical
// to that encoding, so a hand edit cannot slip past review as a no-op.
type Lock struct {
	LockFormat int           `json:"lockFormat"`
	Plugin     string        `json:"plugin"`
	Component  ComponentType `json:"component"`
	Version    string        `json:"version"`
	// Tag is <plugin>--v<version> and Commit the zenith-team-plugins commit it
	// points to. A development pin has no tag.
	Tag    string `json:"tag"`
	Commit string `json:"commit"`
	// ManifestPath and ManifestSHA256 pin the plugin's
	// .boardly-plugin/plugin.json at Commit; ContentPath and ContentTreeID
	// pin the component's folder as a git tree. A dirty development pin has
	// no tree id.
	ManifestPath   string   `json:"manifestPath"`
	ManifestSHA256 string   `json:"manifestSha256"`
	ContentPath    string   `json:"contentPath"`
	ContentTreeID  string   `json:"contentTreeId"`
	Artifact       Artifact `json:"artifact,omitzero"`
	// Managed Tool pins carry the descriptor directly; only Workflows have
	// an external archive. TreeHash covers the canonical descriptor JSON.
	ManagedTools *ManagedTools `json:"managedTools,omitempty"`
	// TreeHash covers every file of the component, forms included; see
	// TreeHash.
	TreeHash    string   `json:"treeHash"`
	FileCount   int      `json:"fileCount"`
	RetiredKeys []string `json:"retiredKeys"`
}

// Artifact names the committed tar and pins its bytes.
type Artifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Development reports whether the lock pins a local checkout rather than a
// release tag.
func (lock Lock) Development() bool {
	return devVersionPattern.MatchString(lock.Version)
}

// Bundle is one verified component: its lock and its files, read-only, at ".".
type Bundle struct {
	Lock  Lock
	Files fs.FS
}

// NewFS serves files read-only exactly as a verified Bundle serves its own:
// directories implied by the paths, entries in name order, mode 0444 and a
// zero time. bundlectl serves files that have not passed Decode yet with it,
// so the catalog parser can still report every problem in them.
func NewFS(files map[string][]byte) fs.FS {
	return newMemoryFS(files)
}

// ParseLock decodes a lock strictly: known fields only, one JSON value, and
// the exact bytes MarshalLock produces.
func ParseLock(raw []byte) (Lock, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var lock Lock
	if err := decoder.Decode(&lock); err != nil {
		return Lock{}, fmt.Errorf("%w: lock: %w", ErrInvalid, err)
	}
	if decoder.More() {
		return Lock{}, fmt.Errorf("%w: lock holds more than one JSON value", ErrInvalid)
	}
	if err := ValidateLock(lock); err != nil {
		return Lock{}, err
	}
	canonical, err := MarshalLock(lock)
	if err != nil {
		return Lock{}, err
	}
	if !bytes.Equal(canonical, raw) {
		return Lock{}, fmt.Errorf(
			"%w: lock is not in canonical form; rewrite it with bundlectl import",
			ErrInvalid,
		)
	}
	return lock, nil
}

// MarshalLock is the one encoding of a lock: two-space indent and a trailing
// newline, retired keys as an array even when there are none.
func MarshalLock(lock Lock) ([]byte, error) {
	if lock.RetiredKeys == nil {
		lock.RetiredKeys = []string{}
	}
	encoded, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// ValidateLock checks a lock's own fields; Decode verifies the pinned content.
func ValidateLock(lock Lock) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: lock %q: %s", ErrInvalid, lock.Plugin, fmt.Sprintf(format, args...))
	}
	switch {
	case lock.LockFormat != 1:
		return invalid("lockFormat must be 1, got %d", lock.LockFormat)
	case len(lock.Plugin) > 64 || !pluginNamePattern.MatchString(lock.Plugin):
		return invalid("plugin must be a kebab-case name of at most 64 characters")
	case ContentPath(lock.Component) == "":
		return invalid("unknown component %q", lock.Component)
	case lock.Component == ComponentTypeManagedTools && len(lock.RetiredKeys) != 0:
		return invalid("managed tools cannot retire Workflow keys")
	case lock.Component == ComponentTypeWorkflows && lock.ManagedTools != nil:
		return invalid("Workflow locks cannot carry managed Tool metadata")
	case lock.Component == ComponentTypeManagedTools &&
		(lock.ManagedTools == nil || lock.Artifact != (Artifact{}) || lock.FileCount != 1):
		return invalid("managed Tool locks contain one inline descriptor and no archive")
	case lock.Component == ComponentTypeWorkflows && lock.Artifact.File != lock.Plugin+ArtifactSuffix:
		return invalid("artifact file must be %q", lock.Plugin+ArtifactSuffix)
	case lock.Component == ComponentTypeWorkflows && !sha256Pattern.MatchString(lock.Artifact.SHA256):
		return invalid("artifact sha256 must be lowercase sha256 hex")
	case !sha256Pattern.MatchString(lock.TreeHash) ||
		!sha256Pattern.MatchString(lock.ManifestSHA256):
		return invalid("treeHash and manifestSha256 must be lowercase sha256 hex")
	case lock.Component == ComponentTypeWorkflows && (lock.Artifact.Size <= 0 || lock.Artifact.Size > maxArtifactBytes):
		return invalid("artifact size %d is out of range", lock.Artifact.Size)
	case lock.FileCount <= 0 || lock.FileCount > maxArtifactFiles:
		return invalid("fileCount %d is out of range", lock.FileCount)
	case !gitObjectPattern.MatchString(lock.Commit):
		return invalid("commit must be a 40-character git object id")
	case lock.ManifestPath != "plugins/"+lock.Plugin+"/.boardly-plugin/plugin.json":
		return invalid("manifestPath must be plugins/%s/.boardly-plugin/plugin.json", lock.Plugin)
	case lock.ContentPath != "plugins/"+lock.Plugin+"/"+ContentPath(lock.Component):
		return invalid("contentPath must be plugins/%s/%s", lock.Plugin, ContentPath(lock.Component))
	}
	if lock.Development() {
		if lock.Tag != "" {
			return invalid("a development pin has no tag")
		}
		if lock.ContentTreeID != "" && !gitObjectPattern.MatchString(lock.ContentTreeID) {
			return invalid("contentTreeId must be empty or a 40-character git object id")
		}
	} else {
		if !releaseVersionPattern.MatchString(lock.Version) {
			return invalid("version %q must be X.Y.Z", lock.Version)
		}
		if lock.Tag != lock.Plugin+"--v"+lock.Version {
			return invalid("tag must be %s--v%s", lock.Plugin, lock.Version)
		}
		if !gitObjectPattern.MatchString(lock.ContentTreeID) {
			return invalid("contentTreeId must be a 40-character git object id")
		}
	}
	if !slices.IsSorted(lock.RetiredKeys) ||
		len(slices.Compact(slices.Clone(lock.RetiredKeys))) != len(lock.RetiredKeys) {
		return invalid("retiredKeys must be sorted and unique")
	}
	for _, key := range lock.RetiredKeys {
		if !pluginNamePattern.MatchString(key) {
			return invalid("retired key %q is not a Workflow key", key)
		}
	}
	return nil
}

// Decode verifies the pinned content and returns its files. Managed Tools
// carry their descriptor inline and require an empty artifact. Workflow archive
// checks run in the order ADR 0016 fixes, cheapest and most telling first:
//  1. size and sha256 against the lock (a hand edit, a CRLF checkout, a Git
//     LFS pointer or a truncation stops here);
//  2. a strict USTAR allowlist in bytewise path order;
//  3. the tree hash and file count;
//  4. UTF-8, LF only, and the size caps;
//  5. no retired key present;
//  6. a canonical re-encode that must equal the artifact.
func Decode(lock Lock, artifact []byte) (Bundle, error) {
	if err := ValidateLock(lock); err != nil {
		return Bundle{}, err
	}
	if lock.Component == ComponentTypeManagedTools {
		if len(artifact) != 0 {
			return Bundle{}, fmt.Errorf("%w: managed Tools have no archive", ErrInvalid)
		}
		raw, err := json.Marshal(lock.ManagedTools)
		if err != nil {
			return Bundle{}, err
		}
		encoded, err := Encode(lock.Component, map[string][]byte{"release.json": raw})
		if err != nil {
			return Bundle{}, err
		}
		files := map[string][]byte{"release.json": encoded}
		if TreeHash(files) != lock.TreeHash {
			return Bundle{}, fmt.Errorf("%w: managed Tool metadata hash differs from the lock", ErrInvalid)
		}
		return Bundle{Lock: lock, Files: NewFS(files)}, nil
	}
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s: %s", ErrInvalid, lock.Artifact.File, fmt.Sprintf(format, args...))
	}
	digest := sha256.Sum256(artifact)
	if int64(len(artifact)) != lock.Artifact.Size || hex.EncodeToString(digest[:]) != lock.Artifact.SHA256 {
		return Bundle{}, invalid("size or sha256 does not match the lock")
	}
	files, err := readArtifact(lock.Component, artifact)
	if err != nil {
		return Bundle{}, invalid("%v", err)
	}
	if len(files) != lock.FileCount || TreeHash(files) != lock.TreeHash {
		return Bundle{}, invalid("file count or tree hash does not match the lock")
	}
	if err := checkFiles(lock.Component, files); err != nil {
		return Bundle{}, invalid("%v", err)
	}
	for name := range files {
		if key, _, _ := strings.Cut(name, "/"); slices.Contains(lock.RetiredKeys, key) {
			return Bundle{}, invalid("retired key %q is present", key)
		}
	}
	encoded, err := Encode(lock.Component, files)
	if err != nil || !bytes.Equal(encoded, artifact) {
		return Bundle{}, invalid("not in canonical form")
	}
	return Bundle{Lock: lock, Files: newMemoryFS(files)}, nil
}

// Encode writes managed Tool metadata as canonical JSON. For Workflows it is
// the one writer of artifact format 1: USTAR regular files in
// bytewise path order, mode 0644, owner 0:0 with empty names, mtime 0, no
// directory, PAX or GNU entries, ending in the two zero blocks. It refuses
// any file Decode would refuse, so bundlectl can never write a bad pin.
func Encode(component ComponentType, files map[string][]byte) ([]byte, error) {
	if err := checkFiles(component, files); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if component == ComponentTypeManagedTools {
		catalog, err := ReadManagedTools(NewFS(files))
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(catalog)
		if err != nil {
			return nil, err
		}
		return append(raw, '\n'), nil
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range sortedNames(files) {
		header := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(files[name])),
			ModTime:  time.Unix(0, 0),
			Format:   tar.FormatUSTAR,
		}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := writer.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// TreeHash is the lock's content hash, the algorithm platform-skill packages
// already use: for each file in bytewise path order, the big-endian uint64
// length of its path, the path, the big-endian uint64 length of its bytes and
// the bytes, all through one sha256.
func TreeHash(files map[string][]byte) string {
	hash := sha256.New()
	var length [8]byte
	for _, name := range sortedNames(files) {
		binary.BigEndian.PutUint64(length[:], uint64(len(name)))
		hash.Write(length[:])
		hash.Write([]byte(name))
		binary.BigEndian.PutUint64(length[:], uint64(len(files[name])))
		hash.Write(length[:])
		hash.Write(files[name])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func readArtifact(component ComponentType, artifact []byte) (map[string][]byte, error) {
	reader := tar.NewReader(bytes.NewReader(artifact))
	files := map[string][]byte{}
	previous := ""
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		canonicalHeader := header.Typeflag == tar.TypeReg && header.Format == tar.FormatUSTAR &&
			header.Mode == 0o644 && header.Uid == 0 && header.Gid == 0 && header.Uname == "" &&
			header.Gname == "" && header.ModTime.Unix() == 0 && header.Linkname == "" &&
			header.Devmajor == 0 && header.Devminor == 0 && len(header.PAXRecords) == 0
		if !canonicalHeader {
			return nil, fmt.Errorf("member %q is not a canonical regular file", header.Name)
		}
		if err := checkName(component, header.Name); err != nil {
			return nil, err
		}
		if header.Name <= previous {
			return nil, fmt.Errorf("member %q is duplicated or out of bytewise order", header.Name)
		}
		previous = header.Name
		if header.Size < 0 || header.Size > maxArtifactFileBytes || len(files) == maxArtifactFiles {
			return nil, fmt.Errorf("member %q exceeds the size or count cap", header.Name)
		}
		contents, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		files[header.Name] = contents
	}
	return files, nil
}

// checkFiles is the component's allowlist and text rules, shared by Encode
// and Decode: the first problem CheckFiles finds.
func checkFiles(component ComponentType, files map[string][]byte) error {
	if problems := CheckFiles(component, files); len(problems) > 0 {
		return problems[0]
	}
	return nil
}

// FileError is a content rule one file of a component breaks. Name is the
// file's path inside the component; the message already names it.
type FileError struct {
	Name string
	Err  error
}

func (err *FileError) Error() string { return err.Err.Error() }
func (err *FileError) Unwrap() error { return err.Err }

// CheckFiles applies the component's allowlist and text rules and returns
// every problem, in the order Encode meets them: a *FileError for each rule a
// file breaks, a plain error for a rule about the whole component. Encode and
// Decode refuse on the first; bundlectl reports them all, so an author sees
// every problem at once and the validator can never disagree with the loader.
// A file whose path is not allowed is not checked further.
func CheckFiles(component ComponentType, files map[string][]byte) []error {
	var problems []error
	if len(files) == 0 || len(files) > maxArtifactFiles {
		problems = append(problems,
			fmt.Errorf("a component holds 1 to %d files, got %d", maxArtifactFiles, len(files)))
	}
	if len(files) > 0 && ContentPath(component) == "" {
		return append(problems, fmt.Errorf("unknown component %q", component))
	}
	var total int
	for _, name := range sortedNames(files) {
		fileProblem := func(format string, args ...any) {
			problems = append(problems, &FileError{Name: name, Err: fmt.Errorf(format, args...)})
		}
		if err := checkName(component, name); err != nil {
			problems = append(problems, &FileError{Name: name, Err: err})
			continue
		}
		contents := files[name]
		if len(contents) > maxArtifactFileBytes {
			fileProblem("%s is larger than %d bytes", name, maxArtifactFileBytes)
		}
		// The whole-component cap is reported once, at the file that crosses it.
		if total <= maxArtifactBytes && total+len(contents) > maxArtifactBytes {
			problems = append(problems,
				fmt.Errorf("the component is larger than %d bytes", maxArtifactBytes))
		}
		total += len(contents)
		if !utf8.Valid(contents) {
			fileProblem("%s is not UTF-8", name)
		}
		if bytes.HasPrefix(contents, []byte{0xEF, 0xBB, 0xBF}) {
			fileProblem("%s starts with a byte order mark", name)
		}
		if bytes.IndexByte(contents, '\r') >= 0 {
			fileProblem("%s has a CR byte; content is LF only", name)
		}
	}
	if len(problems) == 0 && component == ComponentTypeManagedTools {
		if _, err := ReadManagedTools(NewFS(files)); err != nil {
			problems = append(problems, err)
		}
	}
	return problems
}

func checkName(component ComponentType, name string) error {
	if component == ComponentTypeManagedTools {
		if name == "release.json" {
			return nil
		}
		return fmt.Errorf("%q is not release.json; managed tools pin metadata only", name)
	}
	if component != ComponentTypeWorkflows {
		return fmt.Errorf("unknown component %q", component)
	}
	match := workflowFilePattern.FindStringSubmatch(name)
	if match == nil || len(match[1]) > 64 {
		return fmt.Errorf("%q is not <workflow-key>/workflow.yaml or <workflow-key>/playbook.md", name)
	}
	return nil
}

func sortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// LoadError is one pinned component Load could not verify: its lock, its
// artifact, or an artifact with no lock. Plugin is the plugin the file names,
// so a caller replacing that plugin's pin can set its failure aside; the
// message already names the file.
type LoadError struct {
	Plugin string
	Err    error
}

func (err *LoadError) Error() string { return err.Err.Error() }
func (err *LoadError) Unwrap() error { return err.Err }

// LoadFS verifies the components pinned in root: <plugin>.lock.json, plus
// <plugin>.bundle.tar for Workflows. bundlectl reads a pin directory, or the
// pin at a git revision, with it.
func LoadFS(root fs.FS) ([]Bundle, error) {
	return loadBundles(root)
}

func loadBundles(root fs.FS) ([]Bundle, error) {
	locks, err := fs.Glob(root, "*"+LockSuffix)
	if err != nil {
		return nil, err
	}
	artifacts, err := fs.Glob(root, "*"+ArtifactSuffix)
	if err != nil {
		return nil, err
	}
	var bundles []Bundle
	var failures []error
	locked := map[string]bool{}
	for _, name := range locks {
		locked[strings.TrimSuffix(name, LockSuffix)+ArtifactSuffix] = true
		bundle, err := loadBundle(root, name)
		if err != nil {
			failures = append(failures, &LoadError{
				Plugin: strings.TrimSuffix(name, LockSuffix),
				Err:    fmt.Errorf("%s: %w", name, err),
			})
			continue
		}
		bundles = append(bundles, bundle)
		if bundle.Lock.Artifact.File == "" {
			delete(locked, strings.TrimSuffix(name, LockSuffix)+ArtifactSuffix)
		}
	}
	for _, name := range artifacts {
		if !locked[name] {
			failures = append(failures, &LoadError{
				Plugin: strings.TrimSuffix(name, ArtifactSuffix),
				Err:    fmt.Errorf("%w: %s has no lock", ErrInvalid, name),
			})
		}
	}
	return bundles, errors.Join(failures...)
}

func loadBundle(root fs.FS, lockName string) (Bundle, error) {
	raw, err := fs.ReadFile(root, lockName)
	if err != nil {
		return Bundle{}, err
	}
	lock, err := ParseLock(raw)
	if err != nil {
		return Bundle{}, err
	}
	if lockName != lock.Plugin+LockSuffix {
		return Bundle{}, fmt.Errorf("%w: the lock for plugin %q must be named %s", ErrInvalid, lock.Plugin,
			lock.Plugin+LockSuffix)
	}
	var artifact []byte
	if lock.Artifact.File != "" {
		artifact, err = fs.ReadFile(root, lock.Artifact.File)
		if err != nil {
			return Bundle{}, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	return Decode(lock, artifact)
}
