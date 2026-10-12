package contentbundle

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"slices"
	"strings"
	"unicode/utf8"
)

// BuildSkill seals a package without executing any of its files. The digest
// matches the MCP snapshot reader: sorted paths, lengths and exact file bytes.
func BuildSkill(name, description string, files map[string][]byte) (Skill, error) {
	root, ok := files["SKILL.md"]
	if !ok || !utf8.Valid(root) {
		return Skill{}, ErrSkillInstructionsInvalid
	}
	bundle := Skill{Name: name, Description: description, Content: string(root)}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	digest := sha256.New()
	for _, name := range paths {
		data := files[name]
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(name)))
		digest.Write(length[:])
		digest.Write([]byte(name))
		binary.BigEndian.PutUint64(length[:], uint64(len(data)))
		digest.Write(length[:])
		digest.Write(data)
		bundle.SizeBytes += int64(len(data))
		if name == "SKILL.md" {
			continue
		}
		hash := sha256.Sum256(data)
		file := SkillFile{Path: name, SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(data))}
		file.Data = slices.Clone(data)
		bundle.Files = append(bundle.Files, file)
	}
	bundle.Hash = hex.EncodeToString(digest.Sum(nil))
	if err := ValidateSkill(SkillReference(bundle), bundle); err != nil {
		return Skill{}, err
	}
	return bundle, nil
}

// SkillFileBytes returns binary assets unchanged; text uses the same wire
// representation as release-owned managed Tool skills.
func SkillFileBytes(file SkillFile) []byte {
	if file.Data != nil {
		return file.Data
	}
	return []byte(file.Content)
}

func SkillReference(bundle Skill) SkillRef {
	return SkillRef{
		ID: bundle.ID, Source: bundle.Source, Name: bundle.Name,
		Description: bundle.Description, Hash: bundle.Hash, SizeBytes: bundle.SizeBytes,
		FileCount: len(bundle.Files) + 1,
	}
}

// SkillSetHash is the resume fence. An empty set retains the pre-skill empty
// identity so removing the last skill also retires the previous session.
func SkillSetHash(refs []SkillRef) string {
	if len(refs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, ref.Source+"\x00"+ref.ID+"\x00"+ref.Hash)
	}
	slices.Sort(keys)
	digest := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(digest[:])
}
