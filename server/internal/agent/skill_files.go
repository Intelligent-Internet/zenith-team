package agent

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// SkillFileChange replaces the UTF-8 content of an existing package file.
type SkillFileChange struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// SkillEditInput is an atomic batch against one installation revision.
type SkillEditInput struct {
	ExpectedRevision int64
	Changes          []SkillFileChange
	DeletePaths      []string
}

// EditFiles publishes a new immutable version of the installed package. Its
// original import remains provenance; existing Run snapshots are never edited.
func (s *DefaultSkillService) EditFiles(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID, skillID uuid.UUID,
	input SkillEditInput,
) (SkillView, error) {
	operations := len(input.Changes) + len(input.DeletePaths)
	if input.ExpectedRevision < 1 || operations == 0 || operations > 2000 {
		return SkillView{}, ErrSkillInvalid
	}
	var old SkillView
	var item importedSkill
	var next generated.SkillVersion
	// Stage an unready version using the import retention path. Storage IO must
	// finish before publication, without holding configuration locks during upload.
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockSkillConfiguration(ctx, q, principal, agentID); err != nil {
			return err
		}
		items, err := listSkillViews(ctx, q, principal.CompanyID, agentID)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(items, func(item SkillView) bool { return item.ID == skillID })
		if index < 0 {
			return ErrSkillNotFound
		}
		old = items[index]
		if old.Revision != input.ExpectedRevision {
			return ErrSkillConflict
		}
		version, err := q.GetSkillVersion(ctx, generated.GetSkillVersionParams{
			CompanyID: principal.CompanyID, ID: old.VersionID,
		})
		if err != nil {
			return skillRowError(err)
		}
		bundle, err := s.loadBundle(ctx, generated.SkillVersion{
			ID: version.ID, CompanyID: version.CompanyID,
			Name: version.Name, Description: version.Description,
			ContentHash: version.ContentHash, FileCount: version.FileCount, SizeBytes: version.SizeBytes,
		})
		if err != nil {
			return err
		}
		edited, err := editSkillBundle(bundle, input)
		if err != nil {
			return err
		}
		record, err := q.CreateSkillEdit(ctx, generated.CreateSkillEditParams{
			CompanyID:       principal.CompanyID,
			AgentID:         agentID,
			CreatedByUserID: principal.UserID,
			SourceType:      version.SourceType,
			SourceUrl:       version.SourceUrl,
			SourceCommit:    version.SourceCommit,
		})
		if err != nil {
			return err
		}
		item = importedSkill{Path: version.SourcePath, Bundle: edited}
		next, err = createSkillPreviewVersion(ctx, q, record, item)
		return err
	})
	if err != nil {
		return SkillView{}, err
	}
	if _, err := s.uploadSkillPreviewVersion(ctx, next, item); err != nil {
		return SkillView{}, err
	}
	var result SkillView
	err = s.mutate(ctx, principal, agentID, func(q *generated.Queries) error {
		if _, err := q.GetSkillEditForPublish(ctx, generated.GetSkillEditForPublishParams{
			CompanyID: principal.CompanyID, AgentID: agentID,
			ID: next.ImportID, CreatedByUserID: principal.UserID,
		}); err != nil {
			return skillRowError(err)
		}
		items, err := listSkillViews(ctx, q, principal.CompanyID, agentID)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(items, func(item SkillView) bool { return item.ID == skillID })
		if index < 0 {
			return ErrSkillNotFound
		}
		if items[index].Revision != old.Revision {
			return ErrSkillConflict
		}
		var total int64
		for _, skill := range items {
			total += skill.SizeBytes
		}
		if total-old.SizeBytes+item.Bundle.SizeBytes > maxSkillExpandedBytes {
			return ErrSkillLimit
		}
		if err := q.MarkSkillImportReady(ctx, generated.MarkSkillImportReadyParams{
			CompanyID: principal.CompanyID, ID: next.ImportID,
		}); err != nil {
			return err
		}
		changed, err := q.UpdateAgentSkill(ctx, generated.UpdateAgentSkillParams{
			CompanyID: principal.CompanyID, AgentID: agentID, ID: skillID,
			Revision: old.Revision, VersionID: next.ID, Enabled: old.Enabled,
		})
		if err != nil {
			return err
		}
		if changed == 0 {
			return ErrSkillConflict
		}
		items, err = listSkillViews(ctx, q, principal.CompanyID, agentID)
		if err != nil {
			return err
		}
		result = items[index]
		return nil
	})
	return result, err
}

func editSkillBundle(bundle contentbundle.Skill, input SkillEditInput) (contentbundle.Skill, error) {
	operations := len(input.Changes) + len(input.DeletePaths)
	if operations == 0 || operations > 2000 {
		return contentbundle.Skill{}, ErrSkillInvalid
	}
	files := make(map[string][]byte, len(bundle.Files)+1)
	for _, file := range contentbundle.SkillFiles(bundle) {
		files[file.Path] = contentbundle.SkillFileBytes(file)
	}
	seen := make(map[string]bool, len(input.Changes)+len(input.DeletePaths))
	checkPath := func(path string) error {
		if !contentbundle.SafeSkillPath(path) || seen[path] {
			return ErrSkillInvalid
		}
		seen[path] = true
		if _, exists := files[path]; !exists {
			return ErrSkillNotFound
		}
		return nil
	}
	var changedBytes int
	for _, change := range input.Changes {
		if err := checkPath(change.Path); err != nil {
			return contentbundle.Skill{}, err
		}
		old, data := files[change.Path], []byte(change.Content)
		changedBytes += len(data)
		if !utf8.Valid(old) || bytes.ContainsRune(old, 0) ||
			!utf8.Valid(data) || bytes.ContainsRune(data, 0) ||
			len(data) > 16<<20 || changedBytes > MaxSkillUploadBytes {
			return contentbundle.Skill{}, ErrSkillInvalid
		}
		files[change.Path] = data
	}
	for _, path := range input.DeletePaths {
		if err := checkPath(path); err != nil {
			return contentbundle.Skill{}, err
		}
		if path == "SKILL.md" {
			return contentbundle.Skill{}, ErrSkillInvalid
		}
		delete(files, path)
	}
	item, err := parseSkillFiles(files)
	if err != nil {
		return contentbundle.Skill{}, err
	}
	// Identity belongs to the installation, not an editable metadata field.
	// This also prevents edits from colliding with default or custom skills.
	if item.OriginalName != bundle.Name {
		return contentbundle.Skill{}, fmt.Errorf("%w: keep the installed skill name", ErrSkillInvalid)
	}
	return item.Bundle, nil
}
