package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/contentbundle"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

var (
	ErrSkillDaemonUpgrade = errors.New("daemon skill contract requires an upgrade")
	ErrSkillNotFound      = errors.New("agent skill not found")
	ErrSkillConflict      = errors.New("agent skill revision or name conflict")
	ErrSkillUnavailable   = errors.New("skill storage or source unavailable")
	ErrSkillLimit         = errors.New("agent skill limit reached")
)

// SkillService owns reviewed imports, Agent installations and immutable
// versions. Skills carry procedures; installing one grants no execution rights.
type SkillService interface {
	ListForMessage(context.Context, auth.UserPrincipal, MessageSkillScope) ([]MessageSkill, error)
	List(context.Context, auth.UserPrincipal, uuid.UUID) ([]SkillView, error)
	PreviewUpload(context.Context, auth.UserPrincipal, uuid.UUID, SkillUploadInput) (SkillPreview, error)
	PreviewGitHub(context.Context, auth.UserPrincipal, uuid.UUID, SkillGitHubInput) (SkillPreview, error)
	Install(context.Context, auth.UserPrincipal, uuid.UUID, SkillInstallInput) ([]SkillView, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, SkillUpdateInput) (SkillView, error)
	EditFiles(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, SkillEditInput) (SkillView, error)
	Remove(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) error
	ReadReference(context.Context, auth.UserPrincipal, SkillReadInput) (SkillReferenceView, error)
	ResolveForRun(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (contentbundle.Skill, error)
	Cleanup(context.Context) error
}

var _ SkillService = (*DefaultSkillService)(nil)

type SkillObjects interface {
	Upload(context.Context, string, io.Reader, int64, string, string) error
	GetReader(context.Context, string) (io.ReadCloser, error)
	DeleteObject(context.Context, string) error
}

type SkillServiceOptions struct {
	Store      *db.Store
	Bus        *events.Bus
	Objects    SkillObjects
	HTTPClient *http.Client
}

type DefaultSkillService struct {
	store   *db.Store
	bus     *events.Bus
	objects SkillObjects
	http    *http.Client
}

func NewSkillService(options SkillServiceOptions) (*DefaultSkillService, error) {
	if options.Store == nil || options.Bus == nil {
		return nil, errors.New("skill store and bus are required")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &DefaultSkillService{store: options.Store, bus: options.Bus, objects: options.Objects, http: client}, nil
}

type SkillFileSummary struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
	Text      bool   `json:"text"`
}

type SkillView struct {
	ID           uuid.UUID          `json:"id"`
	AgentID      uuid.UUID          `json:"agentId"`
	VersionID    uuid.UUID          `json:"versionId"`
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	Enabled      bool               `json:"enabled"`
	Revision     int64              `json:"revision"`
	ContentHash  string             `json:"contentHash"`
	SizeBytes    int64              `json:"sizeBytes"`
	FileCount    int32              `json:"fileCount"`
	Files        []SkillFileSummary `json:"files"`
	SourceType   string             `json:"sourceType"`
	SourceURL    string             `json:"sourceUrl"`
	SourceCommit string             `json:"sourceCommit"`
	SourcePath   string             `json:"sourcePath"`
	UpdatedAt    time.Time          `json:"updatedAt"`
	Edited       bool               `json:"edited"`
}

type SkillPreviewVersion struct {
	VersionID    uuid.UUID          `json:"versionId"`
	Name         string             `json:"name"`
	Description  string             `json:"description"`
	ContentHash  string             `json:"contentHash"`
	SizeBytes    int64              `json:"sizeBytes"`
	Files        []SkillFileSummary `json:"files"`
	SourcePath   string             `json:"sourcePath"`
	Instructions string             `json:"instructions"`
}

type SkillCandidate struct {
	SkillPreviewVersion
	OriginalName  string               `json:"originalName,omitempty"`
	ExistingSkill *SkillExisting       `json:"existingSkill,omitempty"`
	AddAsNew      *SkillPreviewVersion `json:"addAsNew,omitempty"`
}

type SkillExisting struct {
	ID       uuid.UUID `json:"id"`
	Revision int64     `json:"revision"`
}

type SkillPreview struct {
	ID           uuid.UUID        `json:"id"`
	ExpiresAt    time.Time        `json:"expiresAt"`
	SourceType   string           `json:"sourceType"`
	SourceURL    string           `json:"sourceUrl"`
	SourceCommit string           `json:"sourceCommit"`
	Skills       []SkillCandidate `json:"skills"`
}

type SkillUploadInput struct {
	Filename       string
	Data           []byte
	ReplaceSkillID uuid.UUID
}
type SkillGitHubInput struct {
	URL            string
	ReplaceSkillID uuid.UUID
}
type SkillInstallInput struct {
	ImportID         uuid.UUID
	VersionIDs       []string
	ReplaceSkillID   uuid.UUID
	ExpectedRevision int64
	Replacements     []SkillReplacementInput
}
type SkillReplacementInput struct {
	VersionID        uuid.UUID
	SkillID          uuid.UUID
	ExpectedRevision int64
}
type SkillUpdateInput struct {
	ExpectedRevision int64
	Enabled          bool
}
type SkillReadInput struct {
	AgentID   uuid.UUID
	SkillID   uuid.UUID
	VersionID uuid.UUID
	Path      string
}
type SkillReferenceView struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

func (s *DefaultSkillService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
) ([]SkillView, error) {
	if err := authorizeMemberRead(ctx, s.store.Queries(), principal); err != nil {
		return nil, err
	}
	if _, err := s.store.Queries().
		GetSkillAgent(ctx, generated.GetSkillAgentParams{CompanyID: principal.CompanyID, ID: agentID}); err != nil {
		return nil, skillRowError(err)
	}
	return listSkillViews(ctx, s.store.Queries(), principal.CompanyID, agentID)
}

func listSkillViews(ctx context.Context, q *generated.Queries, companyID, agentID uuid.UUID) ([]SkillView, error) {
	rows, err := q.ListAgentSkills(ctx, generated.ListAgentSkillsParams{CompanyID: companyID, AgentID: agentID})
	if err != nil {
		return nil, err
	}
	items := make([]SkillView, 0, len(rows))
	for _, row := range rows {
		var files []SkillFileSummary
		if err := json.Unmarshal(row.Manifest, &files); err != nil {
			return nil, err
		}
		items = append(items, SkillView{
			ID: row.ID, AgentID: row.AgentID, VersionID: row.VersionID,
			Name: row.Name, Description: row.Description, Enabled: row.Enabled, Revision: row.Revision,
			ContentHash: row.ContentHash, SizeBytes: row.SizeBytes, FileCount: row.FileCount, Files: files,
			SourceType: row.SourceType, SourceURL: row.SourceUrl, SourceCommit: row.SourceCommit,
			SourcePath: row.SourcePath, UpdatedAt: row.UpdatedAt.Time, Edited: row.Edited,
		})
	}
	return items, nil
}

func lockSkillConfiguration(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
) error {
	role, err := lockMemberRole(ctx, q, principal.CompanyID, principal.UserID, "skill mutation")
	if err != nil {
		return err
	}
	row, err := q.LockSkillAgent(ctx, generated.LockSkillAgentParams{CompanyID: principal.CompanyID, ID: agentID})
	if err != nil {
		return skillRowError(err)
	}
	if err := authorizeAgentConfiguration(role, principal, row); err != nil {
		return err
	}
	if row.ArchivedAt.Valid {
		return ErrBindingAgentArchived
	}
	return nil
}

func (s *DefaultSkillService) authorizeImport(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
) error {
	return s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockSkillConfiguration(ctx, q, principal, agentID); err != nil {
			return err
		}
		count, err := q.CountRecentSkillImports(
			ctx,
			generated.CountRecentSkillImportsParams{CompanyID: principal.CompanyID, AgentID: agentID},
		)
		if err != nil {
			return err
		}
		if count >= 20 {
			return ErrSkillLimit
		}
		return nil
	})
}

func (s *DefaultSkillService) PreviewUpload(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input SkillUploadInput,
) (SkillPreview, error) {
	if err := s.authorizeImport(ctx, principal, agentID); err != nil {
		return SkillPreview{}, err
	}
	items, err := readSkillUpload(input.Filename, input.Data)
	if err != nil {
		return SkillPreview{}, err
	}
	source := "zip"
	if strings.EqualFold(path.Ext(input.Filename), ".md") {
		source = "markdown"
	}
	return s.saveSkillPreview(ctx, principal, agentID, skillImportSource{Type: source, ReplaceSkillID: input.ReplaceSkillID}, items)
}

type skillImportSource struct {
	Type           string
	URL            string
	Commit         string
	ReplaceSkillID uuid.UUID
}

func (s *DefaultSkillService) saveSkillPreview(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	source skillImportSource,
	items []importedSkill,
) (SkillPreview, error) {
	if s.objects == nil {
		return SkillPreview{}, ErrSkillUnavailable
	}
	var record generated.SkillImport
	var versions []generated.SkillVersion
	var copies = make(map[int]generated.SkillVersion)
	var drafts []skillPreviewDraft
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockSkillConfiguration(ctx, q, principal, agentID); err != nil {
			return err
		}
		count, err := q.CountRecentSkillImports(
			ctx,
			generated.CountRecentSkillImportsParams{CompanyID: principal.CompanyID, AgentID: agentID},
		)
		if err != nil {
			return err
		}
		if count >= 20 {
			return ErrSkillLimit
		}
		current, err := listSkillViews(ctx, q, principal.CompanyID, agentID)
		if err != nil {
			return err
		}
		if source.ReplaceSkillID != uuid.Nil {
			items, err = targetSkillPreviewNames(items, current, source.ReplaceSkillID)
			if err != nil {
				return err
			}
		}
		drafts, err = prepareSkillPreviewNames(items, current)
		if err != nil {
			return err
		}
		record, err = q.CreateSkillImport(ctx, generated.CreateSkillImportParams{
			CompanyID:       principal.CompanyID,
			AgentID:         agentID,
			CreatedByUserID: principal.UserID,
			SourceType:      source.Type,
			SourceUrl:       source.URL,
			SourceCommit:    source.Commit,
		})
		if err != nil {
			return err
		}
		for i, draft := range drafts {
			row, err := createSkillPreviewVersion(ctx, q, record, draft.Item)
			if err != nil {
				return err
			}
			versions = append(versions, row)
			if draft.Copy != nil {
				row, err := createSkillPreviewVersion(ctx, q, record, *draft.Copy)
				if err != nil {
					return err
				}
				copies[i] = row
			}
		}
		return nil
	})
	if err != nil {
		return SkillPreview{}, err
	}
	preview := SkillPreview{
		ID:           record.ID,
		ExpiresAt:    record.ExpiresAt.Time,
		SourceType:   source.Type,
		SourceURL:    source.URL,
		SourceCommit: source.Commit,
		Skills:       []SkillCandidate{},
	}
	for i, version := range versions {
		draft := drafts[i]
		candidate, err := s.uploadSkillPreviewVersion(ctx, version, draft.Item)
		if err != nil {
			return SkillPreview{}, err
		}
		item := SkillCandidate{SkillPreviewVersion: candidate, ExistingSkill: draft.Existing}
		if draft.Item.OriginalName != candidate.Name {
			item.OriginalName = draft.Item.OriginalName
		}
		if draft.Copy != nil {
			copy, err := s.uploadSkillPreviewVersion(ctx, copies[i], *draft.Copy)
			if err != nil {
				return SkillPreview{}, err
			}
			item.AddAsNew = &copy
		}
		preview.Skills = append(preview.Skills, item)
	}
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockSkillConfiguration(ctx, q, principal, agentID); err != nil {
			return err
		}
		return q.MarkSkillImportReady(
			ctx,
			generated.MarkSkillImportReadyParams{CompanyID: principal.CompanyID, ID: record.ID},
		)
	})
	return preview, err
}

func createSkillPreviewVersion(
	ctx context.Context,
	q *generated.Queries,
	record generated.SkillImport,
	item importedSkill,
) (generated.SkillVersion, error) {
	manifest, err := json.Marshal(skillManifest(item.Bundle))
	if err != nil {
		return generated.SkillVersion{}, err
	}
	return q.CreateSkillVersion(ctx, generated.CreateSkillVersionParams{
		CompanyID: record.CompanyID, ImportID: record.ID,
		Name: item.Bundle.Name, Description: item.Bundle.Description, SourcePath: item.Path,
		ContentHash: item.Bundle.Hash, SizeBytes: item.Bundle.SizeBytes,
		FileCount: int32(len(item.Bundle.Files) + 1), Manifest: manifest,
	})
}

func (s *DefaultSkillService) uploadSkillPreviewVersion(
	ctx context.Context,
	version generated.SkillVersion,
	item importedSkill,
) (SkillPreviewVersion, error) {
	bundle := item.Bundle
	bundle.ID, bundle.Source = version.ID.String(), "company"
	data, err := json.Marshal(bundle)
	if err != nil {
		return SkillPreviewVersion{}, err
	}
	if err := s.objects.Upload(ctx, skillObjectKey(version), bytes.NewReader(data), int64(len(data)), "application/json", "bundle.json"); err != nil {
		return SkillPreviewVersion{}, fmt.Errorf("%w: upload bundle", ErrSkillUnavailable)
	}
	return SkillPreviewVersion{
		VersionID: version.ID, Name: version.Name, Description: version.Description,
		ContentHash: version.ContentHash, SizeBytes: version.SizeBytes,
		Files: skillManifest(bundle), SourcePath: version.SourcePath, Instructions: bundle.Content,
	}, nil
}

func skillManifest(bundle contentbundle.Skill) []SkillFileSummary {
	files := contentbundle.SkillFiles(bundle)
	result := make([]SkillFileSummary, 0, len(files))
	for _, file := range files {
		data := contentbundle.SkillFileBytes(file)
		result = append(
			result,
			SkillFileSummary{
				Path:      file.Path,
				SizeBytes: file.SizeBytes,
				SHA256:    file.SHA256,
				Text:      utf8.Valid(data) && !bytes.ContainsRune(data, 0),
			},
		)
	}
	return result
}

func (s *DefaultSkillService) Install(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input SkillInstallInput,
) ([]SkillView, error) {
	if input.ImportID == uuid.Nil || len(input.VersionIDs) == 0 || len(input.VersionIDs) > maxAgentSkills {
		return nil, ErrSkillInvalid
	}
	ids := make([]uuid.UUID, 0, len(input.VersionIDs))
	for _, value := range input.VersionIDs {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || slices.Contains(ids, id) {
			return nil, ErrSkillInvalid
		}
		ids = append(ids, id)
	}
	replacements, err := skillInstallReplacements(input, ids)
	if err != nil {
		return nil, err
	}
	var result []SkillView
	err = s.mutate(ctx, principal, agentID, func(q *generated.Queries) error {
		if _, err := q.GetSkillImportForInstall(
			ctx,
			generated.GetSkillImportForInstallParams{
				CompanyID:       principal.CompanyID,
				AgentID:         agentID,
				ID:              input.ImportID,
				CreatedByUserID: principal.UserID,
			},
		); err != nil {
			return skillRowError(err)
		}
		versions, err := q.ListSkillImportVersions(
			ctx,
			generated.ListSkillImportVersionsParams{
				CompanyID: principal.CompanyID,
				ImportID:  input.ImportID,
			},
		)
		if err != nil {
			return err
		}
		current, err := listSkillViews(ctx, q, principal.CompanyID, agentID)
		if err != nil {
			return err
		}
		for _, id := range ids {
			index := slices.IndexFunc(
				versions,
				func(row generated.SkillVersion) bool { return row.ID == id },
			)
			if index < 0 {
				return ErrSkillNotFound
			}
			version := versions[index]
			if replacement, replacing := replacements[id]; replacing {
				index := slices.IndexFunc(
					current,
					func(row SkillView) bool { return row.ID == replacement.SkillID },
				)
				if index < 0 {
					return ErrSkillNotFound
				}
				old := current[index]
				if old.Name != version.Name || old.Revision != replacement.ExpectedRevision {
					return ErrSkillConflict
				}
				_, err = q.UpdateAgentSkill(
					ctx,
					generated.UpdateAgentSkillParams{
						CompanyID: principal.CompanyID,
						AgentID:   agentID,
						ID:        old.ID,
						Revision:  old.Revision,
						Enabled:   old.Enabled,
						VersionID: version.ID,
					},
				)
				if err != nil {
					return err
				}
				current[index].VersionID = version.ID
				current[index].Revision++
				continue
			}
			index = slices.IndexFunc(current, func(row SkillView) bool { return row.Name == version.Name })
			if index >= 0 {
				if current[index].VersionID == version.ID {
					continue
				}
				return ErrSkillConflict
			}
			if len(current) >= maxAgentSkills {
				return ErrSkillLimit
			}
			row, err := q.InsertAgentSkill(
				ctx,
				generated.InsertAgentSkillParams{
					CompanyID: principal.CompanyID,
					AgentID:   agentID,
					VersionID: version.ID,
					Name:      version.Name,
				},
			)
			if err != nil {
				return err
			}
			current = append(current, SkillView{ID: row.ID, Name: row.Name, VersionID: row.VersionID})
		}
		result, err = listSkillViews(ctx, q, principal.CompanyID, agentID)
		if err != nil {
			return err
		}
		var total int64
		for _, skill := range result {
			total += skill.SizeBytes
		}
		if total > maxSkillExpandedBytes {
			return ErrSkillLimit
		}
		return nil
	})
	return result, err
}

func skillInstallReplacements(input SkillInstallInput, ids []uuid.UUID) (map[uuid.UUID]SkillReplacementInput, error) {
	items := input.Replacements
	if input.ReplaceSkillID != uuid.Nil {
		if len(ids) != 1 || input.ExpectedRevision < 1 || len(items) != 0 {
			return nil, ErrSkillInvalid
		}
		items = []SkillReplacementInput{{VersionID: ids[0], SkillID: input.ReplaceSkillID, ExpectedRevision: input.ExpectedRevision}}
	} else if input.ExpectedRevision != 0 {
		return nil, ErrSkillInvalid
	}
	if len(items) > len(ids) {
		return nil, ErrSkillInvalid
	}
	result := make(map[uuid.UUID]SkillReplacementInput, len(items))
	seen := make(map[uuid.UUID]bool, len(items))
	for _, item := range items {
		if item.SkillID == uuid.Nil || item.ExpectedRevision < 1 || !slices.Contains(ids, item.VersionID) || seen[item.SkillID] {
			return nil, ErrSkillInvalid
		}
		if _, duplicate := result[item.VersionID]; duplicate {
			return nil, ErrSkillInvalid
		}
		result[item.VersionID], seen[item.SkillID] = item, true
	}
	return result, nil
}

func (s *DefaultSkillService) Update(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID, skillID uuid.UUID,
	input SkillUpdateInput,
) (SkillView, error) {
	if input.ExpectedRevision < 1 {
		return SkillView{}, ErrSkillInvalid
	}
	var result SkillView
	err := s.mutate(ctx, principal, agentID, func(q *generated.Queries) error {
		items, err := listSkillViews(ctx, q, principal.CompanyID, agentID)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(items, func(item SkillView) bool { return item.ID == skillID })
		if index < 0 {
			return ErrSkillNotFound
		}
		old := items[index]
		if old.Revision != input.ExpectedRevision {
			return ErrSkillConflict
		}
		_, err = q.UpdateAgentSkill(
			ctx,
			generated.UpdateAgentSkillParams{
				CompanyID: principal.CompanyID,
				AgentID:   agentID,
				ID:        skillID,
				Revision:  input.ExpectedRevision,
				VersionID: old.VersionID,
				Enabled:   input.Enabled,
			},
		)
		if err != nil {
			return err
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

func (s *DefaultSkillService) Remove(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID, skillID uuid.UUID,
	revision int64,
) error {
	if revision < 1 {
		return ErrSkillInvalid
	}
	return s.mutate(ctx, principal, agentID, func(q *generated.Queries) error {
		count, err := q.DeleteAgentSkill(
			ctx,
			generated.DeleteAgentSkillParams{
				CompanyID: principal.CompanyID,
				AgentID:   agentID,
				ID:        skillID,
				Revision:  revision,
			},
		)
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrSkillConflict
		}
		return nil
	})
}

func (s *DefaultSkillService) mutate(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	fn func(*generated.Queries) error,
) error {
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockSkillConfiguration(ctx, q, principal, agentID); err != nil {
			return err
		}
		if err := fn(q); err != nil {
			return err
		}
		if err := q.TouchAgentSkills(
			ctx,
			generated.TouchAgentSkillsParams{CompanyID: principal.CompanyID, ID: agentID},
		); err != nil {
			return err
		}
		_, err := finishAgentMutation(ctx, q, principal, &pending, agentID)
		return err
	})
	if err != nil {
		return err
	}
	for _, event := range pending {
		s.bus.Publish(ctx, Topic, event)
	}
	return nil
}

func (s *DefaultSkillService) ReadReference(
	ctx context.Context,
	principal auth.UserPrincipal,
	input SkillReadInput,
) (SkillReferenceView, error) {
	items, err := s.List(ctx, principal, input.AgentID)
	if err != nil {
		return SkillReferenceView{}, err
	}
	index := slices.IndexFunc(items, func(item SkillView) bool { return item.ID == input.SkillID })
	if index < 0 {
		return SkillReferenceView{}, ErrSkillNotFound
	}
	if input.VersionID != uuid.Nil && input.VersionID != items[index].VersionID {
		return SkillReferenceView{}, ErrSkillConflict
	}
	version, err := s.store.Queries().
		GetSkillVersion(ctx, generated.GetSkillVersionParams{CompanyID: principal.CompanyID, ID: items[index].VersionID})
	if err != nil {
		return SkillReferenceView{}, skillRowError(err)
	}
	bundle, err := s.loadBundle(
		ctx,
		generated.SkillVersion{
			ID:          version.ID,
			CompanyID:   version.CompanyID,
			Name:        version.Name,
			Description: version.Description,
			ContentHash: version.ContentHash,
			FileCount:   version.FileCount,
			SizeBytes:   version.SizeBytes,
		},
	)
	if err != nil {
		return SkillReferenceView{}, err
	}
	for _, file := range contentbundle.SkillFiles(bundle) {
		data := contentbundle.SkillFileBytes(file)
		if file.Path == input.Path && utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
			return SkillReferenceView{Path: file.Path, Content: string(data), SHA256: file.SHA256}, nil
		}
	}
	return SkillReferenceView{}, ErrSkillNotFound
}

func (s *DefaultSkillService) ResolveForRun(
	ctx context.Context,
	companyID, runID, versionID uuid.UUID,
) (contentbundle.Skill, error) {
	row, err := s.store.Queries().
		GetFrozenRunSkill(ctx, generated.GetFrozenRunSkillParams{CompanyID: companyID, RunID: runID, VersionID: versionID})
	if err != nil {
		return contentbundle.Skill{}, skillRowError(err)
	}
	return s.loadBundle(ctx, row)
}

func (s *DefaultSkillService) loadBundle(ctx context.Context, row generated.SkillVersion) (contentbundle.Skill, error) {
	if s.objects == nil {
		return contentbundle.Skill{}, ErrSkillUnavailable
	}
	reader, err := s.objects.GetReader(ctx, skillObjectKey(row))
	if err != nil {
		return contentbundle.Skill{}, ErrSkillUnavailable
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, 192<<20))
	if err != nil {
		return contentbundle.Skill{}, ErrSkillUnavailable
	}
	var bundle contentbundle.Skill
	if err := json.Unmarshal(data, &bundle); err != nil {
		return contentbundle.Skill{}, ErrSkillUnavailable
	}
	ref := contentbundle.SkillRef{
		ID:          row.ID.String(),
		Source:      "company",
		Name:        row.Name,
		Description: row.Description,
		Hash:        row.ContentHash,
		SizeBytes:   row.SizeBytes,
		FileCount:   int(row.FileCount),
	}
	if err := contentbundle.ValidateSkill(ref, bundle); err != nil {
		return contentbundle.Skill{}, ErrSkillUnavailable
	}
	return bundle, nil
}

func skillObjectKey(row generated.SkillVersion) string {
	return "skills/" + row.CompanyID.String() + "/" + row.ID.String() + "/bundle.json"
}

func skillRowError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSkillNotFound
	}
	return err
}

// Cleanup retains any version selected by an Agent or pinned by a Run. Expired
// previews cannot be installed, so deleting their unreferenced bytes is safe.
func (s *DefaultSkillService) Cleanup(ctx context.Context) error {
	if s.objects == nil {
		return nil
	}
	rows, err := s.store.Queries().ListExpiredUnusedSkillVersions(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			// Serialize with confirmation of the same preview before touching storage.
			if _, err := q.LockSkillImportForCleanup(ctx, row.ImportID); err != nil {
				return err
			}
			if _, err := q.LockSkillVersionForCleanup(ctx, row.ID); err != nil {
				return err
			}
			unused, err := q.SkillVersionUnused(ctx, row.ID)
			if err != nil {
				return err
			}
			if !unused.Bool {
				return nil
			}
			if err := s.objects.DeleteObject(ctx, skillObjectKey(row)); err != nil {
				return err
			}
			return q.DeleteUnusedSkillVersion(ctx, row.ID)
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return s.store.WithinTx(
		ctx,
		pgx.TxOptions{},
		func(q *generated.Queries) error { return q.DeleteEmptyExpiredSkillImports(ctx) },
	)
}

// MessageSkill is a currently selectable installation, labelled with its
// source Agent. Selecting it grants procedures to a message, not permissions.
type MessageSkill struct {
	ID          uuid.UUID `json:"id"`
	AgentID     uuid.UUID `json:"agentId"`
	VersionID   uuid.UUID `json:"versionId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	AgentName   string    `json:"agentName"`
}

type MessageSkillScope struct {
	ConversationID uuid.UUID
	AgentID        uuid.UUID
}

func (s *DefaultSkillService) ListForMessage(ctx context.Context, principal auth.UserPrincipal, scope MessageSkillScope) ([]MessageSkill, error) {
	if err := authorizeMemberRead(ctx, s.store.Queries(), principal); err != nil {
		return nil, err
	}
	rows, err := s.store.Queries().ListMessageSkillCatalog(ctx, generated.ListMessageSkillCatalogParams{CompanyID: principal.CompanyID, ActorUserID: principal.UserID, ConversationID: scope.ConversationID, AgentID: scope.AgentID})
	if err != nil {
		return nil, err
	}
	result := make([]MessageSkill, 0, len(rows))
	for _, row := range rows {
		result = append(result, MessageSkill{ID: row.ID, AgentID: row.AgentID, VersionID: row.VersionID, Name: row.Name, Description: row.Description, AgentName: row.AgentName})
	}
	return result, nil
}
