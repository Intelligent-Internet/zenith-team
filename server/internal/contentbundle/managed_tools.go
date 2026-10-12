package contentbundle

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ManagedTools pins executable packages and the expected identities of their skills.
// ContractVersion versions the daemon's install/export contract, independently
// of either the plugin release or the upstream npm package version.
type ManagedTools struct {
	ContractVersion int           `json:"contractVersion"`
	Releases        []ToolRelease `json:"releases"`
}

// ReadManagedTools validates a component before an importer or daemon uses it.
func ReadManagedTools(root fs.FS) (ManagedTools, error) {
	raw, err := fs.ReadFile(root, "release.json")
	if err != nil {
		return ManagedTools{}, fmt.Errorf("managed tools release: %w", err)
	}
	var catalog ManagedTools
	if err := DecodeJSON(raw, &catalog); err != nil {
		return catalog, fmt.Errorf("release.json: %w", err)
	}
	if catalog.ContractVersion != 1 || len(catalog.Releases) == 0 || len(catalog.Releases) > 16 {
		return catalog, errors.New("managed tools require contractVersion 1 and 1 to 16 releases")
	}
	keys, commands, skills := map[string]bool{}, map[string]bool{}, map[string]bool{}
	byID := map[string]ToolRelease{}
	for _, release := range catalog.Releases {
		if err := validateRelease(release); err != nil {
			return catalog, fmt.Errorf("release %q: %w", release.Key, err)
		}
		if keys[release.Key] || commands[release.Command] {
			return catalog, errors.New("managed tool keys and commands must be unique")
		}
		keys[release.Key], commands[release.Command] = true, true
		byID[release.ID()] = release
		if release.Skill.ID == "" {
			continue
		}
		ref := release.Skill
		if ref.ToolKey != release.Key || ref.ToolVersion != release.Version || ref.ID != release.ID() ||
			ref.Source != "managed_tool" ||
			skills[ref.Name] {
			return catalog, fmt.Errorf(
				"release %s has an inconsistent or duplicate skill identity",
				release.Key,
			)
		}
		skills[ref.Name] = true
		if !skillKeyPattern.MatchString(ref.Name) || len(ref.Name) > 64 ||
			!sha256Pattern.MatchString(ref.Hash) {
			return catalog, errors.New("invalid reviewed skill reference")
		}
		if ref.FileCount <= 0 || ref.FileCount > 2000 || ref.SizeBytes <= 0 || ref.SizeBytes > 128<<20 {
			return catalog, errors.New("invalid reviewed skill size or file count")
		}
	}
	if len(skills) == 0 {
		return catalog, errors.New("managed tools require an authoring skill")
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("managed tool dependency cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		release, ok := byID[id]
		if !ok {
			return fmt.Errorf("managed tool dependency %s is unavailable", id)
		}
		visiting[id] = true
		seen := map[string]bool{}
		for _, dependency := range release.Dependencies {
			if seen[dependency] {
				return fmt.Errorf("duplicate managed tool dependency %s", dependency)
			}
			seen[dependency] = true
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for _, release := range catalog.Releases {
		if err := visit(release.ID()); err != nil {
			return catalog, err
		}
	}
	err = fs.WalkDir(root, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if name == "release.json" {
			return nil
		}
		return fmt.Errorf("managed tools pin metadata only; unexpected file %s", name)
	})
	return catalog, err
}

var (
	npmPackagePattern = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	npmVersionPattern = regexp.MustCompile(
		`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?$`,
	)
	environmentPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

func validateRelease(release ToolRelease) error {
	if !skillKeyPattern.MatchString(release.Key) || len(release.Key) > 64 ||
		strings.TrimSpace(release.Name) == "" ||
		!skillKeyPattern.MatchString(release.Command) ||
		!npmPackagePattern.MatchString(release.PackageName) ||
		!npmVersionPattern.MatchString(release.Version) ||
		release.MinimumNodeMajor < 22 {
		return errors.New("invalid managed tool identity or runtime requirement")
	}
	encoded, ok := strings.CutPrefix(release.Integrity, "sha512-")
	digest, err := base64.StdEncoding.DecodeString(encoded)
	if !ok || err != nil || len(digest) != 64 || base64.StdEncoding.EncodeToString(digest) != encoded {
		return errors.New("npm integrity must be a canonical SHA512")
	}
	for _, name := range []string{release.HomeEnv, release.APIURLEnv, release.SkipSetupEnv} {
		if name != "" && !environmentPattern.MatchString(name) {
			return errors.New("invalid managed tool environment variable")
		}
	}
	if release.HomeEnv != "" || release.APIURLEnv != "" || release.APIURL != "" {
		origin, err := url.Parse(release.APIURL)
		if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
			origin.Path != "" ||
			origin.RawQuery != "" ||
			origin.Fragment != "" ||
			release.HomeEnv == "" ||
			release.APIURLEnv == "" ||
			release.HomeEnv == release.APIURLEnv {
			return errors.New("authenticated tools require a home, API environment and HTTPS origin")
		}
	}
	if release.Skill.ID == "" && release.Skill != (SkillRef{}) {
		return errors.New("incomplete managed tool skill reference")
	}
	if release.Skill.ID == "" && len(release.SkillSources) != 0 ||
		release.Skill.ID != "" && (len(release.SkillSources) == 0 || len(release.SkillSources) > 8) {
		return errors.New("skill sources must match the reviewed skill")
	}
	seen := map[string]bool{}
	for _, source := range release.SkillSources {
		if !SafeSkillPath(source) || !strings.HasPrefix(source, "skills/") || seen[source] {
			return errors.New("skill sources must be unique package-relative directories under skills")
		}
		seen[source] = true
	}
	return validateToolContract(release)
}

// Contract v1 matches the host's Studio login, launcher and renderer adapters.
// Package versions, integrity, API origins and skill bytes remain plugin-owned;
// changing these adapter identities requires a host contract change.
func validateToolContract(release ToolRelease) error {
	switch release.Key {
	case "zenith-studio":
		if release.PackageName != "@zenith-studio/cli" || release.Command != "zenith-studio" ||
			release.HomeEnv != "ZENITH_STUDIO_CLI_HOME" || release.APIURLEnv != "ZENITH_STUDIO_CLI_API_URL" ||
			release.SkipSetupEnv != "ZENITH_STUDIO_CLI_SKIP_SETUP" ||
			release.Skill.ID == "" || release.Skill.Name != "zenith-studio-cli" ||
			len(release.Dependencies) != 1 || !strings.HasPrefix(release.Dependencies[0], "hyperframes@") {
			return errors.New("Studio release differs from host contract v1")
		}
	case "hyperframes":
		if release.PackageName != "hyperframes" || release.Command != "hyperframes" ||
			release.HomeEnv != "" || release.APIURLEnv != "" || release.APIURL != "" || release.SkipSetupEnv != "" ||
			release.Skill != (SkillRef{}) || len(release.Dependencies) != 0 {
			return errors.New("renderer release differs from host contract v1")
		}
	default:
		return fmt.Errorf("tool %s is unsupported by host contract v1", release.Key)
	}
	return nil
}

// ReadSkill reconstructs the existing Run skill wire representation and
// compares the complete file set with the reviewed upstream reference.
func ReadSkill(root fs.FS, ref SkillRef) (Skill, error) {
	if !skillKeyPattern.MatchString(ref.Name) {
		return Skill{}, errors.New("invalid skill key")
	}
	dir := "skills/" + ref.Name
	bundle := Skill{
		ID:          ref.ID,
		Source:      ref.Source,
		Name:        ref.Name,
		Description: ref.Description,
		Hash:        ref.Hash,
		SizeBytes:   ref.SizeBytes,
	}
	err := fs.WalkDir(root, dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			return errors.New("skill files must be regular files")
		}
		raw, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(name, dir+"/")
		if relative == "SKILL.md" {
			bundle.Content = string(raw)
			return nil
		}
		digest := sha256.Sum256(raw)
		bundle.Files = append(
			bundle.Files,
			SkillFile{
				Path:      relative,
				Content:   string(raw),
				SHA256:    hex.EncodeToString(digest[:]),
				SizeBytes: int64(len(raw)),
			},
		)
		return nil
	})
	if err != nil {
		return Skill{}, err
	}
	if err := ValidateSkill(ref, bundle); err != nil {
		return Skill{}, err
	}
	return bundle, nil
}

// ToolRelease pins one npm executable and its optional reviewed skill.
type ToolRelease struct {
	HomeEnv          string   `json:"homeEnv,omitempty"`
	APIURLEnv        string   `json:"apiUrlEnv,omitempty"`
	APIURL           string   `json:"apiUrl,omitempty"`
	Key              string   `json:"key"`
	Name             string   `json:"name"`
	PackageName      string   `json:"packageName"`
	Version          string   `json:"version"`
	Integrity        string   `json:"integrity"`
	Command          string   `json:"command"`
	MinimumNodeMajor int      `json:"minimumNodeMajor"`
	SkipSetupEnv     string   `json:"skipSetupEnv"`
	Dependencies     []string `json:"dependencies,omitempty"`
	SkillSources     []string `json:"skillSources,omitempty"`
	Skill            SkillRef `json:"skill"`
}

// ID is the immutable executable identity.
func (r ToolRelease) ID() string { return r.Key + "@" + r.Version }

// SkillRef is the exact identity and content digest frozen for a Run.
type SkillRef struct {
	ToolKey     string `json:"toolKey,omitempty"`
	ToolVersion string `json:"toolVersion,omitempty"`
	ID          string `json:"id"`
	Source      string `json:"source"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Hash        string `json:"hash"`
	SizeBytes   int64  `json:"sizeBytes"`
	FileCount   int    `json:"fileCount"`
}

// Skill is the provider-neutral Run snapshot representation.
type Skill struct {
	ID          string      `json:"id"`
	Source      string      `json:"source,omitempty"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Hash        string      `json:"hash,omitempty"`
	SizeBytes   int64       `json:"sizeBytes,omitempty"`
	Content     string      `json:"content"`
	Files       []SkillFile `json:"files,omitempty"`
}

// SkillFile carries a package-relative file with its digest.
type SkillFile struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Data      []byte `json:"data,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
}

var skillKeyPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var (
	ErrSkillDescriptionInvalid  = errors.New("invalid skill description")
	ErrSkillInstructionsInvalid = errors.New("invalid skill instructions")
	ErrSkillFilesInvalid        = errors.New("invalid skill files")
	ErrSkillPackageLimit        = errors.New("skill package exceeds file count or size limit")
)

// ValidateSkill checks the requested identity, every path and every byte. Counts
// include SKILL.md; Files contains supporting files only, matching execenv.
func ValidateSkill(ref SkillRef, bundle Skill) error {
	if bundle.ID != ref.ID || bundle.Source != ref.Source || bundle.Hash != ref.Hash ||
		bundle.Name != ref.Name || bundle.Description != ref.Description || bundle.SizeBytes != ref.SizeBytes {
		return errors.New("skill identity differs from frozen reference")
	}
	if !skillKeyPattern.MatchString(bundle.Name) || len(bundle.Name) > 64 || !SafeSkillPath(bundle.Name) ||
		bundle.Name == "boardly-commerce" {
		return errors.New("invalid skill key")
	}
	if !utf8.ValidString(bundle.Description) || utf8.RuneCountInString(bundle.Description) > 1024 ||
		strings.TrimSpace(
			bundle.Description,
		) != bundle.Description || strings.ContainsFunc(bundle.Description, unicode.IsControl) {
		return ErrSkillDescriptionInvalid
	}
	if len(bundle.Content) > 1<<20 {
		return ErrSkillPackageLimit
	}
	if len(bundle.Content) == 0 || !utf8.ValidString(bundle.Content) ||
		strings.ContainsRune(bundle.Content, 0) {
		return ErrSkillInstructionsInvalid
	}
	if ref.FileCount > 2000 {
		return ErrSkillPackageLimit
	}
	if len(bundle.Files)+1 != ref.FileCount {
		return errors.New("skill file count differs from frozen reference")
	}
	files := SkillFiles(bundle)
	digest := sha256.New()
	var size int64
	paths := make(map[string]bool, len(files))
	for _, file := range files {
		key := norm.NFC.String(strings.ToLower(file.Path))
		if paths[key] {
			return fmt.Errorf("%w: paths collide", ErrSkillFilesInvalid)
		}
		paths[key] = true
	}
	for _, file := range files {
		data := SkillFileBytes(file)
		if len(data) > 16<<20 {
			return ErrSkillPackageLimit
		}
		if !SafeSkillPath(file.Path) ||
			!utf8.ValidString(file.Content) || (file.Data != nil && file.Content != "") {
			return ErrSkillFilesInvalid
		}
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			if paths[norm.NFC.String(strings.ToLower(parent))] {
				return fmt.Errorf("%w: file occupies a directory path", ErrSkillFilesInvalid)
			}
		}
		if file.Path != "SKILL.md" &&
			(path.Base(file.Path) == "SKILL.md" || path.Base(file.Path) == ".boardly.json") {
			return fmt.Errorf("%w: reserved file", ErrSkillFilesInvalid)
		}
		fileHash := sha256.Sum256(data)
		if file.SHA256 != hex.EncodeToString(fileHash[:]) || file.SizeBytes != int64(len(data)) {
			return errors.New("skill file digest differs from manifest")
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(file.Path)))
		digest.Write(length[:])
		digest.Write([]byte(file.Path))
		binary.BigEndian.PutUint64(length[:], uint64(len(data)))
		digest.Write(length[:])
		digest.Write(data)
		size += int64(len(data))
	}
	if size > 128<<20 {
		return ErrSkillPackageLimit
	}
	if size != ref.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != ref.Hash {
		return errors.New("skill content differs from frozen reference")
	}
	return nil
}

// SkillFiles includes SKILL.md and sorts all package paths bytewise.
func SkillFiles(bundle Skill) []SkillFile {
	rootHash := sha256.Sum256([]byte(bundle.Content))
	files := make([]SkillFile, 0, len(bundle.Files)+1)
	files = append(files, SkillFile{
		Path: "SKILL.md", Content: bundle.Content,
		SHA256: hex.EncodeToString(rootHash[:]), SizeBytes: int64(len(bundle.Content)),
	})
	files = append(files, bundle.Files...)
	slices.SortFunc(files, func(a, b SkillFile) int { return strings.Compare(a.Path, b.Path) })
	return files
}

// SafeSkillPath accepts portable paths contained in one skill.
func SafeSkillPath(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || path.Clean(value) != value ||
		value == "." || value == ".." || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") ||
		strings.ContainsAny(value, "\\:\x00<>\"|?*") || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > 16 {
		return false
	}
	for _, part := range parts {
		if len(part) > 255 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		base, _, _ := strings.Cut(strings.ToUpper(part), ".")
		switch base {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return false
		}
		if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
			base[3] >= '1' && base[3] <= '9' {
			return false
		}
	}
	return true
}
