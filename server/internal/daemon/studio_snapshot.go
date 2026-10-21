package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"

	"github.com/google/uuid"
)

// This wire index is consumed by the sealed zenith-team-mcp binary. Keep the
// producer independent of its business-tool dependencies; the round-trip test
// exercises the real MCP reader against the released Skill bytes.
type runSkillIndex struct {
	SchemaVersion int                  `json:"schemaVersion"`
	CompanyID     string               `json:"companyId"`
	RunID         uuid.UUID            `json:"runId"`
	CreatedAt     string               `json:"createdAt"`
	Skills        []runSkillIndexEntry `json:"skills"`
}
type runSkillIndexEntry struct {
	Key         string              `json:"key"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	ContentHash string              `json:"contentHash"`
	Files       []runSkillIndexFile `json:"files"`
}
type runSkillIndexFile struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	Text bool   `json:"text"`
}

func writeStudioSkillSnapshot(envRoot string, run Run, bundle toolSkillBundle) (string, error) {
	return writeRunSkillSnapshot(envRoot, run, []SkillData{bundle})
}

func writeRunSkillSnapshot(envRoot string, run Run, bundles []SkillData) (string, error) {
	if len(bundles) == 0 {
		return "", nil
	}
	root, err := os.MkdirTemp(envRoot, ".skills-")
	if err != nil {
		return "", fmt.Errorf("create skill snapshot: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(root)
		}
	}()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	index := runSkillIndex{
		SchemaVersion: 1, CompanyID: run.CompanyID, RunID: run.ID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	bundles = slices.Clone(bundles)
	slices.SortFunc(bundles, func(a, b SkillData) int { return strings.Compare(a.Name, b.Name) })
	previous := ""
	for _, bundle := range bundles {
		if err := contentbundle.ValidateSkill(contentbundle.SkillReference(bundle), bundle); err != nil {
			return "", err
		}
		if bundle.Name == previous {
			return "", fmt.Errorf("duplicate skill %q", bundle.Name)
		}
		previous = bundle.Name
		entry := runSkillIndexEntry{
			Key:         bundle.Name,
			Name:        bundle.Name,
			Description: bundle.Description,
			ContentHash: bundle.Hash,
		}
		for _, file := range contentbundle.SkillFiles(bundle) {
			filename := filepath.Join(root, bundle.Name, filepath.FromSlash(file.Path))
			if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
				return "", err
			}
			data := contentbundle.SkillFileBytes(file)
			if err := os.WriteFile(filename, data, 0o400); err != nil {
				return "", err
			}
			entry.Files = append(
				entry.Files,
				runSkillIndexFile{
					Path: file.Path,
					Hash: file.SHA256,
					Size: file.SizeBytes,
					Text: utf8.Valid(data) && !bytes.ContainsRune(data, 0),
				},
			)
		}
		index.Skills = append(index.Skills, entry)
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), encoded, 0o400); err != nil {
		return "", err
	}
	complete = true
	return root, nil
}
