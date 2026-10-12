package company

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/task"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

var (
	ErrProjectNotFound     = errors.New("Project not found")
	ErrProjectForbidden    = errors.New("Project mutation requires the projects.manage permission")
	ErrProjectInvalidInput = errors.New("invalid Project input")
	ErrProjectConflict     = errors.New("Project revision or key conflict")
	ErrProjectArchived     = errors.New("Project is archived")
	ErrProjectOpenTasks    = errors.New("Project has open Tasks")
	// ErrProjectMemberRequired refuses a grant for someone who is not an
	// active member with an enabled account. It names nobody.
	ErrProjectMemberRequired = errors.New("Project access needs an active Company member")
	// ErrProjectGrantRevisionConflict refuses an access change made against
	// a stale projects.access_version.
	ErrProjectGrantRevisionConflict = errors.New("Project access revision changed")
	// ErrProjectGrantExceedsOwn is the self-grant cap: someone who manages a
	// Project's access only through their own grant asked for a permission
	// they do not hold there, or tried to change a grant that holds one. It
	// names no permission.
	ErrProjectGrantExceedsOwn = errors.New("Project access can only be given or changed within your own")
	// ErrProjectVisibilityFloor refuses a grant change that would take a
	// person below the Company default set on a Company-visible Project:
	// make the Project restricted first.
	ErrProjectVisibilityFloor = errors.New("Company-visible Project keeps the default permissions for everyone")
	projectKeyPattern         = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// ProjectAccessView is the caller's own standing on a Project, as the server
// decided it (ADR 0017). Clients gate on permissions and never re-derive them
// from a role. Revision is the Project's access revision, the precondition of
// every grant, Join and visibility change.
type ProjectAccessView struct {
	Permissions []auth.ProjectPermission `json:"permissions"`
	Management  bool                     `json:"management"`
	Revision    int64                    `json:"revision"`
}

type ProjectResponse struct {
	// Files is absent when the caller only manages the Project through
	// projects.manage and cannot read it: management is not content.
	Files       *ProjectFiles          `json:"files,omitempty"`
	ID          uuid.UUID              `json:"id"`
	CompanyID   uuid.UUID              `json:"companyId"`
	Key         string                 `json:"key"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Visibility  auth.ProjectVisibility `json:"visibility"`
	Access      ProjectAccessView      `json:"access"`
	Revision    int64                  `json:"revision"`
	CreatedAt   time.Time              `json:"createdAt"`
	UpdatedAt   time.Time              `json:"updatedAt"`
	ArchivedAt  *time.Time             `json:"archivedAt"`
}

type ProjectCreateInput struct {
	Key, Title, Description string
	Files                   *ProjectFilesInput
}

// ProjectUpdateInput is a sparse PATCH. Title and description need
// project.settings.update; visibility needs project.visibility.update. Files
// are replaced through ReplaceFiles, never here. ExpectedRevision is the
// Project revision; a visibility change is an access change and also names
// the access revision it was decided on (ExpectedAccessRevision), which is
// given with Visibility and only then.
type ProjectUpdateInput struct {
	ExpectedRevision       int64
	ExpectedAccessRevision *int64
	Title                  *string
	Description            *string
	Visibility             *auth.ProjectVisibility
}

// ProjectService manages Company-owned Project identity, lifecycle, files
// and access (ADR 0017). Task membership and execution remain the Task
// domain's responsibility.
type ProjectService interface {
	// Authorize resolves the caller's standing on a Project they can see,
	// or answers auth.ErrProjectNotVisible. The route guards call it.
	Authorize(context.Context, auth.UserPrincipal, uuid.UUID) (auth.ProjectAccess, error)
	List(context.Context, auth.UserPrincipal, bool) ([]ProjectResponse, error)
	ListForManageView(context.Context, auth.UserPrincipal, bool) ([]ProjectResponse, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (ProjectResponse, error)
	// ListForRun and GetForRun serve a request that carries a Run's proof
	// (ADR 0017): readers only, bounded by the delegate rule (D31).
	ListForRun(context.Context, auth.UserPrincipal, bool, *task.RunScope) ([]ProjectResponse, error)
	GetForRun(context.Context, auth.UserPrincipal, uuid.UUID, *task.RunScope) (ProjectResponse, error)
	Create(context.Context, auth.UserPrincipal, ProjectCreateInput) (ProjectResponse, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, ProjectUpdateInput) (ProjectResponse, error)
	Archive(context.Context, auth.UserPrincipal, uuid.UUID, int64) (ProjectResponse, error)
	ReplaceFiles(context.Context, auth.UserPrincipal, uuid.UUID, ProjectFilesInput, int64) (ProjectResponse, error)
	ListGrants(context.Context, auth.UserPrincipal, uuid.UUID) (ProjectGrantList, error)
	SetGrants(context.Context, auth.UserPrincipal, uuid.UUID, ProjectGrantInput) (ProjectGrantResult, error)
	RemoveGrants(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) (ProjectGrantResult, error)
	// PreviewGrant answers what a person grant change or removal would do:
	// the Runs it stops and whether the person loses the Project.
	PreviewGrant(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, []string) (ProjectGrantPreview, error)
	// SetGroupGrant, RemoveGroupGrant and PreviewGroupGrant change, remove
	// and preview one Group's grant on a Project (Access TECH D4).
	SetGroupGrant(
		context.Context, auth.UserPrincipal, uuid.UUID, ProjectGroupGrantInput,
	) (ProjectGroupGrantResult, error)
	RemoveGroupGrant(
		context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64,
	) (ProjectGroupGrantResult, error)
	PreviewGroupGrant(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, []string) (GroupPreview, error)
	Join(context.Context, auth.UserPrincipal, uuid.UUID, int64) (ProjectResponse, error)
	// AccessReport lists, per restricted Project, the members outside work
	// they were doing there; Company projects.manage only.
	AccessReport(context.Context, auth.UserPrincipal) (ProjectAccessReport, error)
}

var _ ProjectService = (*DefaultProjectService)(nil)

type DefaultProjectService struct {
	store          *db.Store
	bus            *events.Bus
	inboxPresenter inbox.Presenter
}

// NewProjectService accepts a nil inboxPresenter, as NewTaskService does: the
// Inbox items an access change writes still reach their readers through
// their inbox.created frames, and only the after-commit presentation is
// skipped.
func NewProjectService(store *db.Store, bus *events.Bus, inboxPresenter inbox.Presenter) (*DefaultProjectService, error) {
	if store == nil || bus == nil {
		return nil, errors.New("Project store and event bus are required")
	}
	return &DefaultProjectService{store: store, bus: bus, inboxPresenter: inboxPresenter}, nil
}

// projectResponse projects a row with the caller's standing on it. Files are
// loaded separately, and only for a reader.
func projectResponse(row generated.Project, access auth.ProjectAccess) ProjectResponse {
	permissions := access.Permissions
	if permissions == nil {
		permissions = []auth.ProjectPermission{}
	}
	value := ProjectResponse{
		ID: row.ID, CompanyID: row.CompanyID, Key: row.Key, Title: row.Title, Description: row.Description,
		Visibility: auth.ProjectVisibility(row.Visibility),
		Access:     ProjectAccessView{Permissions: permissions, Management: access.Management, Revision: row.AccessVersion},
		Revision:   row.Version, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.ArchivedAt.Valid {
		v := row.ArchivedAt.Time
		value.ArchivedAt = &v
	}
	return value
}

// readableProjectResponse projects a row and, when the reader reads the
// Project, the files they may open.
func readableProjectResponse(
	ctx context.Context,
	q *generated.Queries,
	readerID uuid.UUID,
	row generated.Project,
	access auth.ProjectAccess,
) (ProjectResponse, error) {
	result := []ProjectResponse{projectResponse(row, access)}
	if !access.Can(auth.ProjectPermissionRead) {
		return result[0], nil
	}
	if err := loadProjectFiles(ctx, q, row.CompanyID, readerID, result); err != nil {
		return ProjectResponse{}, err
	}
	return result[0], nil
}

func (s *DefaultProjectService) Authorize(ctx context.Context, principal auth.UserPrincipal, id uuid.UUID) (auth.ProjectAccess, error) {
	return auth.RequireProjectVisible(ctx, s.store.Queries(), principal, id)
}

// List returns the Projects the caller reads, each with the caller's access.
func (s *DefaultProjectService) List(ctx context.Context, principal auth.UserPrincipal, includeArchived bool) ([]ProjectResponse, error) {
	rows, err := s.store.Queries().ListProjectsForActiveMember(ctx, generated.ListProjectsForActiveMemberParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID, IncludeArchived: includeArchived,
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
	})
	if err != nil {
		return nil, fmt.Errorf("list Projects: %w", err)
	}
	result := make([]ProjectResponse, len(rows))
	for i, row := range rows {
		result[i] = projectResponse(row.Project, auth.ProjectAccessOf(auth.ProjectAccessFacts{
			ProjectID: row.Project.ID, CompanyID: row.Project.CompanyID, Visibility: row.Project.Visibility,
			Via: row.Via, Management: row.Management, Revision: row.Project.AccessVersion,
			GrantedPermissions: row.GrantedPermissions, GroupPermissions: row.GroupPermissions,
		}))
	}
	if err := loadProjectFiles(ctx, s.store.Queries(), principal.CompanyID, principal.UserID, result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListForManageView returns the Projects the caller manages through Company
// projects.manage but cannot read: metadata and the three management
// permissions, no files. The view is Company-level: a caller
// whose role lacks projects.manage is refused, not shown an empty list.
func (s *DefaultProjectService) ListForManageView(
	ctx context.Context,
	principal auth.UserPrincipal,
	includeArchived bool,
) ([]ProjectResponse, error) {
	if !principal.Role.Can(auth.PermissionProjectsManage) {
		return nil, ErrProjectForbidden
	}
	rows, err := s.store.Queries().ListProjectsForManageView(ctx, generated.ListProjectsForManageViewParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID, IncludeArchived: includeArchived,
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
	})
	if err != nil {
		return nil, fmt.Errorf("list managed Projects: %w", err)
	}
	result := make([]ProjectResponse, len(rows))
	for i, row := range rows {
		result[i] = projectResponse(row, auth.ProjectAccessOf(auth.ProjectAccessFacts{
			ProjectID: row.ID, CompanyID: row.CompanyID, Visibility: row.Visibility,
			Management: true, Revision: row.AccessVersion,
		}))
	}
	return result, nil
}

// Get reads one Project the caller can see. A reader gets the whole
// projection; a projects.manage holder who cannot read gets metadata only.
func (s *DefaultProjectService) Get(ctx context.Context, principal auth.UserPrincipal, id uuid.UUID) (ProjectResponse, error) {
	q := s.store.Queries()
	row, err := q.GetProjectForActiveMember(ctx, generated.GetProjectForActiveMemberParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID, ProjectID: id,
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectResponse{}, ErrProjectNotFound
	}
	if err != nil {
		return ProjectResponse{}, fmt.Errorf("read Project: %w", err)
	}
	access := auth.ProjectAccessOf(auth.ProjectAccessFacts{
		ProjectID: row.Project.ID, CompanyID: row.Project.CompanyID, Visibility: row.Project.Visibility,
		Via: row.Via, Management: row.Management, Revision: row.Project.AccessVersion,
		GrantedPermissions: row.GrantedPermissions, GroupPermissions: row.GroupPermissions,
	})
	return readableProjectResponse(ctx, q, principal.UserID, row.Project, access)
}

// ListForRun is List for a request that carries a Run's proof (ADR 0017).
// The handler has checked that the session is the Run's authorizer, so the
// list is already the Projects that member reads. It lists only the
// Projects the delegate rule lets the Run read
// (task.RunScope.ReadableProjects, D31): a Task Run its own Project, and any
// other Run, in a direct chat or tagged in a channel or group DM, those its
// Authorizer reads.
func (s *DefaultProjectService) ListForRun(
	ctx context.Context,
	principal auth.UserPrincipal,
	includeArchived bool,
	run *task.RunScope,
) ([]ProjectResponse, error) {
	result, err := s.List(ctx, principal, includeArchived)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(result))
	for i, project := range result {
		ids[i] = project.ID
	}
	readable, err := run.ReadableProjects(ctx, s.store.Queries(), principal.CompanyID, ids)
	if err != nil {
		return nil, err
	}
	kept := make([]ProjectResponse, 0, len(result))
	for _, project := range result {
		if readable[project.ID] {
			kept = append(kept, project)
		}
	}
	return kept, nil
}

// GetForRun is Get for a request that carries a Run's proof (ADR 0017). A
// Run reads a Project only as a reader (runReadsProject), so a Project the
// member only manages answers like a missing one. A Project the delegate
// rule keeps the Run out of (D31) is refused with the Run-scope sentence
// that names nothing.
func (s *DefaultProjectService) GetForRun(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	run *task.RunScope,
) (ProjectResponse, error) {
	result, err := s.Get(ctx, principal, id)
	if err != nil {
		return ProjectResponse{}, err
	}
	if !runReadsProject(result) {
		return ProjectResponse{}, ErrProjectNotFound
	}
	if err := run.AdmitProject(ctx, s.store.Queries(), principal.CompanyID, id); err != nil {
		return ProjectResponse{}, err
	}
	return result, nil
}

// runReadsProject says whether a Run may read a Project the member sees: only
// as a reader. The metadata-only view a projects.manage holder gets of a
// Project they cannot read is management, not content, and a Run never acts
// as a manager.
func runReadsProject(value ProjectResponse) bool {
	return slices.Contains(value.Access.Permissions, auth.ProjectPermissionRead)
}

func normalizeProjectText(title, description string) (string, string, error) {
	title = protocol.TrimContractWhitespace(title)
	description = protocol.TrimContractWhitespace(description)
	if title == "" || !utf8.ValidString(title) || utf8.RuneCountInString(title) > 240 || !utf8.ValidString(description) || len(description) > 64*1024 {
		return "", "", ErrProjectInvalidInput
	}
	return title, description, nil
}

// lockProjectMutation takes the Company lock, then the acting member's row,
// and rechecks the Company permission beside it; an empty permission admits
// any active member. Lock order: company -> member -> project ->
// conversation -> run.
func lockProjectMutation(ctx context.Context, q *generated.Queries, principal auth.UserPrincipal, required auth.Permission) error {
	if _, err := q.LockCompanyForMembershipMutation(ctx, principal.CompanyID); errors.Is(err, pgx.ErrNoRows) {
		return ErrProjectNotFound
	} else if err != nil {
		return fmt.Errorf("lock Company for Project mutation: %w", err)
	}
	_, err := authorizeMembershipActor(ctx, q, principal.CompanyID, principal.UserID, required)
	switch {
	case errors.Is(err, ErrNotFound):
		return ErrProjectNotFound
	case errors.Is(err, ErrForbidden):
		return ErrProjectForbidden
	}
	return err
}

// lockProjectForChange holds the Project row FOR UPDATE, so every access
// reader (FOR SHARE) and every other change waits, and resolves the caller's
// standing under that lock. A Project the caller cannot see answers
// auth.ErrProjectNotVisible. The caller took lockProjectMutation first.
func lockProjectForChange(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	id uuid.UUID,
) (generated.Project, auth.ProjectAccess, error) {
	row, err := q.GetProjectForUpdate(ctx, generated.GetProjectForUpdateParams{CompanyID: principal.CompanyID, ProjectID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.Project{}, auth.ProjectAccess{}, auth.ErrProjectNotVisible
	}
	if err != nil {
		return generated.Project{}, auth.ProjectAccess{}, fmt.Errorf("lock Project: %w", err)
	}
	access, err := auth.ResolveProjectAccess(ctx, q, principal, id)
	if err != nil {
		return generated.Project{}, auth.ProjectAccess{}, err
	}
	if !access.Visible() {
		return generated.Project{}, auth.ProjectAccess{}, auth.ErrProjectNotVisible
	}
	return row, access, nil
}

func requireProjectPermissions(access auth.ProjectAccess, permissions ...auth.ProjectPermission) error {
	for _, permission := range permissions {
		if !access.Can(permission) {
			return auth.ErrProjectPermissionRequired
		}
	}
	return nil
}

func mapProjectError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProjectConflict
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrProjectConflict
	}
	return fmt.Errorf("mutate Project: %w", err)
}

// Create writes a restricted Project whose creator holds every Project
// permission (ADR 0017), recorded as the creator's own grant.
func (s *DefaultProjectService) Create(ctx context.Context, principal auth.UserPrincipal, input ProjectCreateInput) (ProjectResponse, error) {
	if !projectKeyPattern.MatchString(input.Key) {
		return ProjectResponse{}, ErrProjectInvalidInput
	}
	title, description, err := normalizeProjectText(input.Title, input.Description)
	if err != nil {
		return ProjectResponse{}, err
	}
	var pending []events.Event
	var value ProjectResponse
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		if err := lockProjectMutation(ctx, q, principal, auth.PermissionProjectsManage); err != nil {
			return err
		}
		row, err := q.CreateProject(ctx, generated.CreateProjectParams{CompanyID: principal.CompanyID, Key: input.Key, Title: title, Description: description})
		if err != nil {
			return mapProjectError(err)
		}
		if err := grantProjectCreatorInTx(ctx, q, &pending, principal, events.UserActor(principal.UserID.String()), row); err != nil {
			return err
		}
		if err := canvas.EnsureOverviewInTx(ctx, q, principal, row); err != nil {
			return err
		}
		if err := replaceProjectFiles(ctx, q, &pending, principal, row.ID, input.Files); err != nil {
			return err
		}
		access, err := auth.ResolveProjectAccess(ctx, q, principal, row.ID)
		if err != nil {
			return err
		}
		value, err = readableProjectResponse(ctx, q, principal.UserID, row, access)
		if err != nil {
			return err
		}
		return stageProjectEventInTx(ctx, q, &pending, principal, EventProjectCreated, value, nil)
	})
	if err != nil {
		return ProjectResponse{}, err
	}
	s.publish(ctx, pending)
	return value, nil
}

// Update edits title and description (project.settings.update) and
// visibility (project.visibility.update) in one PATCH. Both are checked
// before the revision, so a caller without the permission learns nothing
// about the Project's state.
func (s *DefaultProjectService) Update(ctx context.Context, principal auth.UserPrincipal, id uuid.UUID, input ProjectUpdateInput) (ProjectResponse, error) {
	if input.Title == nil && input.Description == nil && input.Visibility == nil {
		return ProjectResponse{}, ErrProjectInvalidInput
	}
	if id == uuid.Nil || input.ExpectedRevision < 1 {
		return ProjectResponse{}, ErrProjectInvalidInput
	}
	if input.Visibility != nil && *input.Visibility != auth.ProjectVisibilityCompany &&
		*input.Visibility != auth.ProjectVisibilityRestricted {
		return ProjectResponse{}, ErrProjectInvalidInput
	}
	if (input.Visibility == nil) != (input.ExpectedAccessRevision == nil) ||
		(input.ExpectedAccessRevision != nil && *input.ExpectedAccessRevision < 1) {
		return ProjectResponse{}, ErrProjectInvalidInput
	}
	required := []auth.ProjectPermission{}
	if input.Title != nil || input.Description != nil {
		required = append(required, auth.ProjectPermissionSettingsUpdate)
	}
	if input.Visibility != nil {
		required = append(required, auth.ProjectPermissionVisibilityUpdate)
	}
	var pending []events.Event
	var notices []inbox.CreateResult
	var value ProjectResponse
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending, notices = nil, nil
		if err := lockProjectMutation(ctx, q, principal, ""); err != nil {
			return err
		}
		current, access, err := lockProjectForChange(ctx, q, principal, id)
		if err != nil {
			return err
		}
		if err := requireProjectPermissions(access, required...); err != nil {
			return err
		}
		if current.Version != input.ExpectedRevision {
			return ErrProjectConflict
		}
		if input.ExpectedAccessRevision != nil && current.AccessVersion != *input.ExpectedAccessRevision {
			return ErrProjectGrantRevisionConflict
		}
		if current.ArchivedAt.Valid {
			return ErrProjectArchived
		}
		title, description := current.Title, current.Description
		if input.Title != nil {
			title = *input.Title
		}
		if input.Description != nil {
			description = *input.Description
		}
		title, description, err = normalizeProjectText(title, description)
		if err != nil {
			return err
		}
		row, err := q.UpdateProject(ctx, generated.UpdateProjectParams{CompanyID: principal.CompanyID, ID: id, Title: title, Description: description, ExpectedRevision: input.ExpectedRevision})
		if err != nil {
			return mapProjectError(err)
		}
		if input.Visibility != nil && *input.Visibility != auth.ProjectVisibility(current.Visibility) {
			if row, err = setProjectVisibilityInTx(ctx, q, &pending, &notices, principal, row, *input.Visibility); err != nil {
				return err
			}
		}
		access, err = auth.ResolveProjectAccess(ctx, q, principal, id)
		if err != nil {
			return err
		}
		value, err = readableProjectResponse(ctx, q, principal.UserID, row, access)
		if err != nil {
			return err
		}
		return stageProjectEventInTx(ctx, q, &pending, principal, EventProjectUpdated, value, projectChangedFields(current, row))
	})
	if err != nil {
		return ProjectResponse{}, fmt.Errorf("update Project: %w", err)
	}
	s.publish(ctx, pending)
	s.present(notices)
	return value, nil
}

// Archive needs project.archive, which projects.manage holders hold through
// the management bridge.
func (s *DefaultProjectService) Archive(ctx context.Context, principal auth.UserPrincipal, id uuid.UUID, revision int64) (ProjectResponse, error) {
	if id == uuid.Nil || revision < 1 {
		return ProjectResponse{}, ErrProjectInvalidInput
	}
	var pending []events.Event
	var value ProjectResponse
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		if err := lockProjectMutation(ctx, q, principal, ""); err != nil {
			return err
		}
		current, access, err := lockProjectForChange(ctx, q, principal, id)
		if err != nil {
			return err
		}
		if err := requireProjectPermissions(access, auth.ProjectPermissionArchive); err != nil {
			return err
		}
		if current.Version != revision {
			return ErrProjectConflict
		}
		if current.ArchivedAt.Valid {
			return ErrProjectArchived
		}
		open, err := q.ProjectHasOpenTasks(ctx, generated.ProjectHasOpenTasksParams{CompanyID: principal.CompanyID, ProjectID: id})
		if err != nil {
			return err
		}
		if open {
			return ErrProjectOpenTasks
		}
		row, err := q.ArchiveProject(ctx, generated.ArchiveProjectParams{CompanyID: principal.CompanyID, ID: id, ExpectedRevision: revision})
		if err != nil {
			return mapProjectError(err)
		}
		value, err = readableProjectResponse(ctx, q, principal.UserID, row, access)
		if err != nil {
			return err
		}
		return stageProjectEventInTx(ctx, q, &pending, principal, EventProjectArchived, value, nil)
	})
	if err != nil {
		return ProjectResponse{}, fmt.Errorf("archive Project: %w", err)
	}
	s.publish(ctx, pending)
	return value, nil
}

func (s *DefaultProjectService) publish(ctx context.Context, pending []events.Event) {
	for _, batch := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, batch.Topic, batch.Events...)
	}
}

// present shows, after commit, the Inbox items an access change wrote: the
// notices of Runs it stopped (ADR 0017).
func (s *DefaultProjectService) present(notices []inbox.CreateResult) {
	for _, notice := range notices {
		inbox.PresentCreated(s.inboxPresenter, notice)
	}
}

// projectReceipt is the Activity payload of a Project lifecycle row: which
// Project and which fields changed. It never carries the Project's files or
// anyone's access.
type projectReceipt struct {
	ID      uuid.UUID `json:"id"`
	Key     string    `json:"key"`
	Title   string    `json:"title"`
	Changed []string  `json:"changed"`
}

// stageProjectEventInTx records a Project lifecycle Activity row in the
// mutation's transaction and stages the lifecycle frame beside it. The row
// names its Project (activity_events.project_id), so the Activity read shows
// it only to the Project's readers (ADR 0017). The frame is marked recorded:
// its payload is ProjectLifecycle, which carries no files and no access (a
// client refetches the Project for its own), and the bus recorder must not
// copy it into Activity.
func stageProjectEventInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	name string,
	value ProjectResponse,
	changed []string,
) error {
	return stageProjectEventAsInTx(
		ctx, q, pending, principal.CompanyID, events.UserActor(principal.UserID.String()), name, value, changed,
	)
}

// stageProjectEventAsInTx is stageProjectEventInTx for a change the actor
// names in full: a Run applying its Company Plan names its Agent, the
// person it acts for and the Run (D31).
func stageProjectEventAsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	companyID uuid.UUID,
	actor events.Actor,
	name string,
	value ProjectResponse,
	changed []string,
) error {
	if changed == nil {
		changed = []string{}
	}
	if err := activity.RecordEvidenceInTx(ctx, q, pending, activity.EvidenceInput{
		CompanyID: companyID,
		EntityID:  value.ID,
		ProjectID: value.ID,
		Action:    name,
		Actor:     actor,
		Payload:   projectReceipt{ID: value.ID, Key: value.Key, Title: value.Title, Changed: changed},
	}); err != nil {
		return err
	}
	// The frame reaches the Project's readers and the projects.manage holders
	// (ADR 0017), and carries neither its files nor the actor's access.
	audience, err := auth.ProjectFrameAudienceInTx(ctx, q, companyID, value.ID, true)
	if err != nil {
		return err
	}
	*pending = append(*pending, events.Event{
		Name: name, EntityType: TopicProject, CompanyID: companyID, CreatedAt: value.UpdatedAt,
		Actor: actor, ActivityRecorded: true, Payload: ProjectLifecycle{
			ID: value.ID, CompanyID: value.CompanyID, Key: value.Key, Title: value.Title,
			Description: value.Description, Visibility: value.Visibility, Revision: value.Revision,
			CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, ArchivedAt: value.ArchivedAt,
			ProjectFrame: auth.ProjectFrame{Audience: audience},
		},
	})
	return nil
}

// projectChangedFields names the Project fields an update changed.
func projectChangedFields(before, after generated.Project) []string {
	changed := []string{}
	if before.Title != after.Title {
		changed = append(changed, "title")
	}
	if before.Description != after.Description {
		changed = append(changed, "description")
	}
	if before.Visibility != after.Visibility {
		changed = append(changed, "visibility")
	}
	return changed
}
