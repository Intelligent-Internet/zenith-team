package agent

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"
)

type skillPreviewDraft struct {
	Item     importedSkill
	Existing *SkillExisting
	Copy     *importedSkill
}

func targetSkillPreviewNames(items []importedSkill, current []SkillView, targetID uuid.UUID) ([]importedSkill, error) {
	index := slices.IndexFunc(current, func(skill SkillView) bool { return skill.ID == targetID })
	if index < 0 {
		return nil, ErrSkillNotFound
	}
	target := current[index]
	index = slices.IndexFunc(items, func(item importedSkill) bool { return item.Path == target.SourcePath })
	if index < 0 {
		index = slices.IndexFunc(
			items,
			func(item importedSkill) bool { return item.Bundle.Name == target.Name },
		)
	}
	if len(items) == 1 {
		index = 0
	}
	if index < 0 {
		return nil, fmt.Errorf("%w: update source does not contain the installed skill", ErrSkillInvalid)
	}
	item, err := renameImportedSkill(items[index], target.Name)
	if err != nil {
		return nil, err
	}
	items[index] = item
	taken := map[string]bool{target.Name: true}
	for i, item := range items {
		if i == index {
			continue
		}
		name := availableSkillName(item.Bundle.Name, taken)
		renamed, err := renameImportedSkill(item, name)
		if err != nil {
			return nil, err
		}
		items[i], taken[name] = renamed, true
	}
	return items, nil
}

func prepareSkillPreviewNames(items []importedSkill, current []SkillView) ([]skillPreviewDraft, error) {
	taken := make(map[string]bool, len(items)+len(current))
	existing := make(map[string]SkillExisting, len(current))
	for _, item := range items {
		taken[item.Bundle.Name] = true
	}
	for _, skill := range current {
		taken[skill.Name] = true
		existing[skill.Name] = SkillExisting{ID: skill.ID, Revision: skill.Revision}
	}
	drafts := make([]skillPreviewDraft, 0, len(items))
	for _, item := range items {
		draft := skillPreviewDraft{Item: item}
		if skill, found := existing[item.Bundle.Name]; found {
			renamed, err := renameImportedSkill(item, availableSkillName(item.Bundle.Name, taken))
			if err != nil {
				return nil, err
			}
			taken[renamed.Bundle.Name] = true
			draft.Existing, draft.Copy = &skill, &renamed
		}
		drafts = append(drafts, draft)
	}
	return drafts, nil
}

func normalizeImportedSkillName(original string) string {
	var slug strings.Builder
	for _, r := range norm.NFKD.String(strings.ToLower(strings.TrimSpace(original))) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if r == 'đ' {
			r = 'd'
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			slug.WriteRune(r)
		} else if slug.Len() > 0 && !strings.HasSuffix(slug.String(), "-") {
			slug.WriteByte('-')
		}
	}
	name := strings.TrimRight(slug.String(), "-")
	if name == "" {
		hash := sha256.Sum256([]byte(original))
		name = fmt.Sprintf("skill-%x", hash[:4])
	}
	name = strings.TrimRight(name[:min(len(name), 64)], "-")
	// Protect actual native slots and release-owned skills, not the Zenith prefix.
	switch name {
	case "zenith-platform", "zenith-workflows", "zenith-studio-cli", "zenith-studio-cli-skill-installer",
		"synced", "anthropic-skills":
		name = "custom-" + name
	}
	return name
}

func availableSkillName(base string, taken map[string]bool) string {
	name := base
	for n := 2; taken[name]; n++ {
		suffix := fmt.Sprintf("-%d", n)
		name = strings.TrimRight(base[:min(len(base), 64-len(suffix))], "-") + suffix
	}
	return name
}

func assignImportedSkillNames(items []importedSkill) ([]importedSkill, error) {
	taken := make(map[string]bool, len(items))
	assigned := make([]bool, len(items))
	// Portable source names keep their natural names ahead of normalized ones.
	for i, item := range items {
		if item.OriginalName == item.Bundle.Name && !taken[item.Bundle.Name] {
			taken[item.Bundle.Name], assigned[i] = true, true
		}
	}
	for i, item := range items {
		if assigned[i] {
			continue
		}
		name := availableSkillName(item.Bundle.Name, taken)
		renamed, err := renameImportedSkill(item, name)
		if err != nil {
			return nil, err
		}
		items[i], taken[name] = renamed, true
	}
	return items, nil
}

func renameImportedSkill(item importedSkill, name string) (importedSkill, error) {
	if name == item.Bundle.Name {
		return item, nil
	}
	content, err := replaceSkillFrontmatterName(item.Bundle.Content, name)
	if err != nil {
		return importedSkill{}, err
	}
	files := map[string][]byte{"SKILL.md": []byte(content)}
	for _, file := range item.Bundle.Files {
		files[file.Path] = contentbundle.SkillFileBytes(file)
	}
	item.Bundle, err = contentbundle.BuildSkill(name, item.Bundle.Description, files)
	if err != nil {
		return importedSkill{}, skillBundleError(err)
	}
	return item, nil
}

func replaceSkillFrontmatterName(content, name string) (string, error) {
	lines := strings.SplitAfter(content, "\n")
	end := 1
	for end < len(lines) && strings.TrimRight(lines[end], "\r\n") != "---" {
		end++
	}
	if end == len(lines) {
		return "", fmt.Errorf("%w: missing frontmatter delimiter", ErrSkillInvalid)
	}
	var front yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "")), &front); err != nil {
		return "", fmt.Errorf("%w: malformed frontmatter", ErrSkillInvalid)
	}
	if len(front.Content) == 0 || front.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("%w: frontmatter must be a mapping", ErrSkillInvalid)
	}
	fields := front.Content[0].Content
	for i := 0; i < len(fields); i += 2 {
		if fields[i].Value == "name" {
			fields[i+1].SetString(name)
		}
	}
	encoded, err := yaml.Marshal(&front)
	if err != nil {
		return "", fmt.Errorf("%w: encode frontmatter", ErrSkillInvalid)
	}
	header := string(encoded)
	if strings.HasSuffix(lines[0], "\r\n") {
		header = strings.ReplaceAll(header, "\n", "\r\n")
	}
	// Only metadata is adapted. Relative paths, instructions and assets stay intact.
	return lines[0] + header + strings.Join(lines[end:], ""), nil
}
