package company

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ProjectFilesInput replaces the current file selection as one reviewed set.
type ProjectFilesInput struct {
	LogoID      *uuid.UUID  `json:"logoId"`
	DocumentIDs []uuid.UUID `json:"documentIds"`
}

type ProjectFile struct {
	ID          uuid.UUID `json:"id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"contentType"`
	SizeBytes   int64     `json:"sizeBytes"`
}

type ProjectFiles struct {
	Logo      *ProjectFile  `json:"logo"`
	Documents []ProjectFile `json:"documents"`
}

// loadProjectFiles attaches the active files the reader may open to Projects
// the reader reads.
func loadProjectFiles(ctx context.Context, q *generated.Queries, companyID, readerID uuid.UUID, projects []ProjectResponse) error {
	ids := make([]uuid.UUID, len(projects))
	for i := range projects {
		ids[i] = projects[i].ID
		projects[i].Files = &ProjectFiles{Documents: []ProjectFile{}}
	}
	rows, err := q.ListProjectFiles(ctx, generated.ListProjectFilesParams{
		CompanyID: companyID, ProjectIds: ids, ActorUserID: readerID,
	})
	if err != nil {
		return fmt.Errorf("list Project files: %w", err)
	}
	positions := make(map[uuid.UUID]int, len(projects))
	for i := range projects {
		positions[projects[i].ID] = i
	}
	for _, row := range rows {
		i := positions[row.ProjectID]
		file := ProjectFile{ID: row.ID, Filename: row.Filename, ContentType: row.ContentType, SizeBytes: row.SizeBytes}
		if row.FileType == "logo" {
			projects[i].Files.Logo = &file
		} else {
			projects[i].Files.Documents = append(projects[i].Files.Documents, file)
		}
	}
	return nil
}

// replaceProjectFiles makes the Project's active files exactly the selection.
// A file the replace adds or drops that is also on a chat message changes who
// sees it there (a Project file follows its Projects' readers, ADR 0017), so
// every such message gets an opaque message.updated and its readers refetch
// its attachments.
func replaceProjectFiles(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	projectID uuid.UUID,
	input *ProjectFilesInput,
) error {
	if input == nil {
		return nil
	}
	ids, err := projectFileIDs(*input)
	if err != nil {
		return err
	}
	// Lock every selected upload before retiring old grants, so cleanup cannot win between validation and registration.
	for _, id := range ids {
		row, err := q.LockProjectUpload(ctx, generated.LockProjectUploadParams{CompanyID: principal.CompanyID, AttachmentID: id, ActorUserID: principal.UserID, ProjectID: projectID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProjectInvalidInput
		}
		if err != nil {
			return fmt.Errorf("lock Project upload: %w", err)
		}
		logo := input.LogoID != nil && id == *input.LogoID
		if !validProjectFile(row.Filename, row.ContentType, row.SizeBytes, logo) {
			return ErrProjectInvalidInput
		}
	}
	retired, err := q.RetireProjectFiles(ctx, generated.RetireProjectFilesParams{CompanyID: principal.CompanyID, ProjectID: projectID})
	if err != nil {
		return err
	}
	for _, id := range ids {
		kind := "document"
		if input.LogoID != nil && id == *input.LogoID {
			kind = "logo"
		}
		if err := q.RegisterProjectFile(ctx, generated.RegisterProjectFileParams{CompanyID: principal.CompanyID, ProjectID: projectID, AttachmentID: id, FileType: kind}); err != nil {
			return err
		}
	}
	changed := changedProjectFiles(retired, ids)
	if len(changed) == 0 {
		return nil
	}
	links, err := q.ListProjectFileMessageLinks(ctx, generated.ListProjectFileMessageLinksParams{
		CompanyID: principal.CompanyID, AttachmentIds: changed,
	})
	if err != nil {
		return fmt.Errorf("list messages carrying Project files: %w", err)
	}
	staged := make([]events.Event, 0, len(links))
	for _, link := range links {
		staged = append(staged, conversation.NewMessageUpdatedEvent(
			principal.UserID, principal.CompanyID, link.ConversationID, link.MessageID,
			conversation.Kind(link.ConversationKind), util.NullUUIDPtr(link.ConversationCreatedByUserID),
		))
	}
	// A Task chat's hint reaches the readers of that chat's Project (ADR 0017).
	if err := auth.StampProjectAudiencesInTx(ctx, q, staged); err != nil {
		return err
	}
	*pending = append(*pending, staged...)
	return nil
}

// changedProjectFiles is the files a replace added or dropped: RetireProjectFiles
// retires every active row and RegisterProjectFile revives the kept ones, so
// a file in both lists did not change who sees it.
func changedProjectFiles(retired, selected []uuid.UUID) []uuid.UUID {
	kept := make(map[uuid.UUID]bool, len(retired))
	for _, id := range retired {
		kept[id] = false
	}
	var changed []uuid.UUID
	for _, id := range selected {
		if _, wasActive := kept[id]; wasActive {
			kept[id] = true
			continue
		}
		changed = append(changed, id)
	}
	for _, id := range retired {
		if !kept[id] {
			changed = append(changed, id)
		}
	}
	return changed
}

// projectFileIDs checks the shape of a file selection (at most ten
// documents, no nil or repeated id) and returns every id, documents first.
func projectFileIDs(input ProjectFilesInput) ([]uuid.UUID, error) {
	if len(input.DocumentIDs) > 10 {
		return nil, ErrProjectInvalidInput
	}
	ids := append([]uuid.UUID{}, input.DocumentIDs...)
	if input.LogoID != nil {
		ids = append(ids, *input.LogoID)
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			return nil, ErrProjectInvalidInput
		}
		seen[id] = true
	}
	return ids, nil
}

// sameProjectFiles reports whether the selection names exactly the files
// the Project already has: the same logo and the same documents.
func sameProjectFiles(current ProjectFiles, input ProjectFilesInput) bool {
	if (current.Logo == nil) != (input.LogoID == nil) ||
		(current.Logo != nil && current.Logo.ID != *input.LogoID) ||
		len(current.Documents) != len(input.DocumentIDs) {
		return false
	}
	have := make(map[uuid.UUID]bool, len(current.Documents))
	for _, file := range current.Documents {
		have[file.ID] = true
	}
	for _, id := range input.DocumentIDs {
		if !have[id] {
			return false
		}
	}
	return true
}

// ReplaceFiles makes the Project's files exactly the selection. It needs
// project.files.manage, which no Company role grants: management is not
// content. expectedRevision is the Project revision the caller read, so two
// editors cannot overwrite each other's selection. An unchanged selection
// writes nothing and answers the Project.
func (s *DefaultProjectService) ReplaceFiles(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	input ProjectFilesInput,
	expectedRevision int64,
) (ProjectResponse, error) {
	if id == uuid.Nil || expectedRevision < 1 {
		return ProjectResponse{}, ErrProjectInvalidInput
	}
	if _, err := projectFileIDs(input); err != nil {
		return ProjectResponse{}, err
	}
	var pending []events.Event
	var value ProjectResponse
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		if err := lockProjectMutation(ctx, q, principal, ""); err != nil {
			return err
		}
		row, access, err := lockProjectForChange(ctx, q, principal, id)
		if err != nil {
			return err
		}
		if err := requireProjectPermissions(access, auth.ProjectPermissionFilesManage); err != nil {
			return err
		}
		if row.Version != expectedRevision {
			return ErrProjectConflict
		}
		if row.ArchivedAt.Valid {
			return ErrProjectArchived
		}
		current, err := readableProjectResponse(ctx, q, principal.UserID, row, access)
		if err != nil {
			return err
		}
		if sameProjectFiles(*current.Files, input) {
			value = current
			return nil
		}
		if err := replaceProjectFiles(ctx, q, &pending, principal, id, &input); err != nil {
			return err
		}
		row, err = q.AdvanceProjectRevision(ctx, generated.AdvanceProjectRevisionParams{CompanyID: principal.CompanyID, ProjectID: id})
		if err != nil {
			return mapProjectError(err)
		}
		value, err = readableProjectResponse(ctx, q, principal.UserID, row, access)
		if err != nil {
			return err
		}
		return stageProjectEventInTx(ctx, q, &pending, principal, EventProjectUpdated, value, []string{"files"})
	})
	if err != nil {
		return ProjectResponse{}, fmt.Errorf("replace Project files: %w", err)
	}
	s.publish(ctx, pending)
	return value, nil
}

func validProjectFile(name, contentType string, size int64, logo bool) bool {
	if size <= 0 {
		return false
	}
	if logo {
		return validProfileImage(contentType, size)
	}
	if size > 20<<20 {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt":
		return contentType == "text/plain"
	case ".md", ".markdown":
		return contentType == "text/markdown" || contentType == "text/plain" || contentType == "application/octet-stream"
	case ".pdf":
		return contentType == "application/pdf"
	case ".docx":
		return contentType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	}
	return false
}
