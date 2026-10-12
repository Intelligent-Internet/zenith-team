package agent

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"boardly.dev/boardly/server/internal/contentbundle"
)

const (
	MaxSkillUploadBytes   = 32 << 20
	maxSkillExpandedBytes = 128 << 20
	maxAgentSkills        = 32
)

var (
	ErrSkillInvalid             = errors.New("invalid skill package")
	ErrSkillFrontmatterInvalid  = fmt.Errorf("%w: invalid frontmatter", ErrSkillInvalid)
	ErrSkillNameRequired        = fmt.Errorf("%w: name required", ErrSkillInvalid)
	ErrSkillDescriptionInvalid  = fmt.Errorf("%w: invalid description", ErrSkillInvalid)
	ErrSkillReferenceInvalid    = fmt.Errorf("%w: invalid reference", ErrSkillInvalid)
	ErrSkillReferenceMissing    = fmt.Errorf("%w: missing reference", ErrSkillInvalid)
	ErrSkillArchiveInvalid      = fmt.Errorf("%w: unreadable archive", ErrSkillInvalid)
	ErrSkillFilesInvalid        = fmt.Errorf("%w: unsafe package files", ErrSkillInvalid)
	ErrSkillPackageLimit        = fmt.Errorf("%w: package limit exceeded", ErrSkillInvalid)
	ErrSkillInstructionsInvalid = fmt.Errorf("%w: invalid instruction text", ErrSkillInvalid)
	ErrSkillCountInvalid        = fmt.Errorf("%w: expected 1–32 skills", ErrSkillInvalid)
)

type importedSkill struct {
	Path         string              `json:"path"`
	OriginalName string              `json:"originalName"`
	Bundle       contentbundle.Skill `json:"bundle"`
}

func readSkillUpload(filename string, data []byte) ([]importedSkill, error) {
	if len(data) > MaxSkillUploadBytes {
		return nil, ErrSkillPackageLimit
	}
	if strings.EqualFold(path.Ext(filename), ".md") {
		item, err := parseSkillFiles(map[string][]byte{"SKILL.md": data})
		if err != nil {
			return nil, err
		}
		item.Path = "SKILL.md"
		return []importedSkill{item}, nil
	}
	if !strings.EqualFold(path.Ext(filename), ".zip") {
		return nil, ErrSkillInvalid
	}
	files, err := readSkillZIP(data)
	if err != nil {
		return nil, err
	}
	// A ZIP may wrap a repository-root skill in a download directory whose
	// name describes the repository/ref, not the skill. Treat that single
	// common directory as packaging, preserving paths inside the skill.
	for name := range files {
		wrapper, _, found := strings.Cut(name, "/")
		if !found || files[wrapper+"/SKILL.md"] == nil {
			break
		}
		wrapped := make(map[string][]byte, len(files))
		for name, contents := range files {
			if !strings.HasPrefix(name, wrapper+"/") {
				return discoverSkillFiles(files)
			}
			wrapped[strings.TrimPrefix(name, wrapper+"/")] = contents
		}
		return discoverSkillFiles(wrapped)
	}
	return discoverSkillFiles(files)
}

func discoverSkillFiles(files map[string][]byte) ([]importedSkill, error) {
	var roots []string
	for name := range files {
		if path.Base(name) == "SKILL.md" {
			roots = append(roots, path.Dir(name))
		}
	}
	slices.Sort(roots)
	if len(roots) == 0 || len(roots) > maxAgentSkills {
		return nil, ErrSkillCountInvalid
	}
	items := make([]importedSkill, 0, len(roots))
	for _, root := range roots {
		prefix := root + "/"
		if root == "." {
			prefix = ""
		}
		selected := map[string][]byte{}
		for name, data := range files {
			if strings.HasPrefix(name, prefix) {
				selected[strings.TrimPrefix(name, prefix)] = data
			}
		}
		item, err := parseSkillFiles(selected)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", root, err)
		}
		item.Path = path.Join(root, "SKILL.md")
		items = append(items, item)
	}
	return assignImportedSkillNames(items)
}

func readSkillZIP(data []byte) (map[string][]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrSkillArchiveInvalid
	}
	if len(reader.File) > 4000 {
		return nil, ErrSkillPackageLimit
	}
	files := map[string][]byte{}
	seen := map[string]bool{}
	var total uint64
	for _, file := range reader.File {
		name := strings.TrimSuffix(file.Name, "/")
		if !contentbundle.SafeSkillPath(name) {
			return nil, ErrSkillFilesInvalid
		}
		if !file.FileInfo().IsDir() && !file.Mode().IsRegular() {
			return nil, ErrSkillFilesInvalid
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, ErrSkillFilesInvalid
		}
		seen[key] = true
		if file.FileInfo().IsDir() {
			continue
		}
		if len(files) >= 2000 || file.UncompressedSize64 > 16<<20 ||
			file.UncompressedSize64 > maxSkillExpandedBytes-total {
			return nil, ErrSkillPackageLimit
		}
		total += file.UncompressedSize64
		stream, err := file.Open()
		if err != nil {
			return nil, ErrSkillArchiveInvalid
		}
		contents, readErr := io.ReadAll(io.LimitReader(stream, int64(file.UncompressedSize64)+1))
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil || uint64(len(contents)) != file.UncompressedSize64 {
			return nil, ErrSkillArchiveInvalid
		}
		if path.Base(name) == ".DS_Store" || strings.HasPrefix(name, "__MACOSX/") {
			continue
		}
		files[name] = contents
	}
	return files, nil
}

var skillReferenceLink = regexp.MustCompile(`!?\[[^\]]*\]\(([^\s)]+)(?:\s+"[^"]*")?\)`)

func parseSkillFiles(files map[string][]byte) (importedSkill, error) {
	content := files["SKILL.md"]
	if len(content) > 1<<20 {
		return importedSkill{}, ErrSkillPackageLimit
	}
	if len(content) == 0 || !utf8.Valid(content) {
		return importedSkill{}, ErrSkillInstructionsInvalid
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return importedSkill{}, ErrSkillFrontmatterInvalid
	}
	end := strings.Index(normalized[4:], "\n---\n")
	if end < 0 {
		return importedSkill{}, ErrSkillFrontmatterInvalid
	}
	var front struct {
		Name          string            `yaml:"name"`
		Description   string            `yaml:"description"`
		License       string            `yaml:"license"`
		Compatibility string            `yaml:"compatibility"`
		Metadata      map[string]string `yaml:"metadata"`
		AllowedTools  string            `yaml:"allowed-tools"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(normalized[4 : 4+end]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&front); err != nil {
		return importedSkill{}, ErrSkillFrontmatterInvalid
	}
	front.Description = strings.Join(strings.Fields(front.Description), " ")
	if strings.TrimSpace(front.Name) == "" {
		return importedSkill{}, ErrSkillNameRequired
	}
	if front.Description == "" {
		return importedSkill{}, ErrSkillDescriptionInvalid
	}
	if utf8.RuneCountInString(front.Compatibility) > 500 {
		return importedSkill{}, ErrSkillFrontmatterInvalid
	}
	for name, data := range files {
		for _, part := range strings.Split(name, "/") {
			if strings.HasPrefix(part, ".") || part == "node_modules" {
				return importedSkill{}, ErrSkillFilesInvalid
			}
		}
		if !strings.EqualFold(path.Ext(name), ".md") {
			continue
		}
		for _, match := range skillReferenceLink.FindAllSubmatch(data, -1) {
			ref, err := url.Parse(strings.Trim(string(match[1]), "<>"))
			if err != nil {
				return importedSkill{}, ErrSkillReferenceInvalid
			}
			if ref.IsAbs() || ref.Host != "" || ref.Path == "" {
				continue
			}
			target := path.Join(path.Dir(name), ref.Path)
			if !contentbundle.SafeSkillPath(target) {
				return importedSkill{}, ErrSkillReferenceInvalid
			}
			if _, ok := files[target]; !ok {
				return importedSkill{}, ErrSkillReferenceMissing
			}
		}
	}
	name := normalizeImportedSkillName(front.Name)
	if name != front.Name {
		updated, err := replaceSkillFrontmatterName(string(content), name)
		if err != nil {
			return importedSkill{}, err
		}
		files = maps.Clone(files)
		files["SKILL.md"] = []byte(updated)
	}
	bundle, err := contentbundle.BuildSkill(name, front.Description, files)
	if err != nil {
		return importedSkill{}, skillBundleError(err)
	}
	return importedSkill{OriginalName: front.Name, Bundle: bundle}, nil
}

func skillBundleError(err error) error {
	switch {
	case errors.Is(err, contentbundle.ErrSkillDescriptionInvalid):
		return ErrSkillDescriptionInvalid
	case errors.Is(err, contentbundle.ErrSkillInstructionsInvalid):
		return ErrSkillInstructionsInvalid
	case errors.Is(err, contentbundle.ErrSkillFilesInvalid):
		return ErrSkillFilesInvalid
	case errors.Is(err, contentbundle.ErrSkillPackageLimit):
		return ErrSkillPackageLimit
	default:
		return fmt.Errorf("%w: %w", ErrSkillInvalid, err)
	}
}
