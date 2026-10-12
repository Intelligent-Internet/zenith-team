package company

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// Groups (Access TECH Part D, D31). A Group is a named, flat set of Team
// members in one Company: people only, never an Agent or another Group. It
// never acts and never starts a Run; it only holds Project grants, which
// every member holds too. A person's access is the union of their own grant,
// each of their Groups, the Company-visible floor and the management bridge,
// so leaving a Group never takes what their own grant gives.
//
// Owners and admins (members.manage) create, rename, fill, empty and delete
// Groups; every active member reads them. A change of members, or a delete,
// moves access on every Project the Group reaches, exactly as a person grant
// change would: under the Company, member, Group and Project locks (in that
// order) it resolves everyone it touches on every Project before and after
// (one read each, auth.ResolvePeopleProjectAccess), advances each Project's
// access revision, records the change once per Project, tells the Project's
// grant managers once when it opens a restricted Project to someone, and ends
// the Runs and queued items of anyone who lost read, write or execute. The
// trail is per Project, not per person, so a change of many people stays a
// few rows and notices per Project (Access TECH, the trail, 2026-10-07).

var (
	ErrGroupInvalidInput = errors.New("invalid Group input")
	// ErrGroupNotFound answers a Group that is not in the Company, or a
	// malformed id.
	ErrGroupNotFound = errors.New("Group not found")
	// ErrGroupConflict refuses a change made against a stale Group revision,
	// or a name another Group of the Company already has.
	ErrGroupConflict = errors.New("Group revision or name conflict")
	// ErrGroupMemberRequired refuses adding someone who is not an active Team
	// member with an enabled account: an Agent, a former member, a stranger.
	// It names nobody.
	ErrGroupMemberRequired = errors.New("Group members must be active Team members")
	// ErrGroupLimitReached is a cap: Groups per Company, members per Group,
	// or Group grants per Project.
	ErrGroupLimitReached = errors.New("Group limit reached")
	// ErrGroupGrantPermissionRequired is "no laundering": a membership change
	// or a delete would give or take access on a Project where its author
	// could not give or take that access directly. It names no Project.
	ErrGroupGrantPermissionRequired = errors.New("Group change moves access its author cannot change")
)

// The Group caps (IA-FR34): placeholders until D7 measures a change at the
// caps under the Company lock.
const (
	maxCompanyGroups      = 100
	maxGroupMembers       = 500
	maxProjectGroupGrants = 50
	// maxGroupChangeIDs bounds the people one membership change or preview
	// names, added and removed together.
	maxGroupChangeIDs = 500
)

// Activity actions of group and project_grant rows a Group change writes
// (Access TECH, the trail).
const (
	groupCreated                   = "group.created"
	groupUpdated                   = "group.updated"
	groupDeleted                   = "group.deleted"
	groupMemberAdded               = "group.member_added"
	groupMemberRemoved             = "group.member_removed"
	projectGrantGroupChanged       = "project_grant.group_changed"
	projectGrantGroupMemberAdded   = "project_grant.group_member_added"
	projectGrantGroupMemberRemoved = "project_grant.group_member_removed"
)

// Group is a Group as the Team page lists it, with what it holds on each
// Project the viewer can see.
type Group struct {
	ID          uuid.UUID      `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	MemberCount int64          `json:"memberCount"`
	Revision    int64          `json:"revision"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
	Projects    []GroupProject `json:"projects"`
}

// GroupDetail is one Group with its members.
type GroupDetail struct {
	Group
	Members []GroupMember `json:"members"`
}

// GroupMember is one member of a Group: who added them, and when.
type GroupMember struct {
	UserID  uuid.UUID  `json:"userId"`
	AddedBy *uuid.UUID `json:"addedBy"`
	AddedAt time.Time  `json:"addedAt"`
}

// GroupProject is what a Group holds on one Project.
type GroupProject struct {
	ProjectID   uuid.UUID                `json:"projectId"`
	Permissions []auth.ProjectPermission `json:"permissions"`
}

// GroupInput creates a Group.
type GroupInput struct {
	Name        string
	Description string
}

// GroupUpdateInput renames or redescribes a Group; nil leaves a field as it
// is. ExpectedRevision is the Group's revision the caller read.
type GroupUpdateInput struct {
	Name             *string
	Description      *string
	ExpectedRevision int64
}

// GroupMembersInput is the one way to change a Group's members: who to add,
// who to take out, against the revision the caller previewed. Adding someone
// already in, or removing someone not in, changes nothing for them.
type GroupMembersInput struct {
	AddUserIDs       []uuid.UUID
	RemoveUserIDs    []uuid.UUID
	ExpectedRevision int64
}

// GroupChange is the change a preview answers: the same people as a
// GroupMembersInput, or the Group's deletion.
type GroupChange struct {
	AddUserIDs    []uuid.UUID
	RemoveUserIDs []uuid.UUID
	Delete        bool
}

// GroupPreview is what a Group change would do, read without writing or
// locking: the Projects and people whose access would move (before and
// after, the management bridge left out), how many people and Runs that is,
// and the restricted Projects it would open to the caller. Revision is what
// the save must send: the Group's revision for a membership change or a
// delete, the Project's access revision for a Group grant. Projects the
// caller cannot see are left out with their people and Runs, and
// ProjectsHidden says so.
type GroupPreview struct {
	Revision        int64                 `json:"revision"`
	Projects        []GroupPreviewProject `json:"projects"`
	PeopleAffected  int                   `json:"peopleAffected"`
	RunsStopping    int                   `json:"runsStopping"`
	OpenedForCaller []uuid.UUID           `json:"openedForCaller"`
	ProjectsHidden  bool                  `json:"projectsHidden"`
}

// GroupPreviewProject is one Project where access would move, and who moves.
type GroupPreviewProject struct {
	ProjectID uuid.UUID            `json:"projectId"`
	People    []GroupPreviewPerson `json:"people"`
}

// GroupPreviewPerson is one person whose access on a Project would move:
// what they hold there before and after, and how many of their Runs there
// would stop.
type GroupPreviewPerson struct {
	UserID       uuid.UUID                `json:"userId"`
	Before       []auth.ProjectPermission `json:"before"`
	After        []auth.ProjectPermission `json:"after"`
	RunsStopping int                      `json:"runsStopping"`
}

// GroupService manages a Company's Groups (Access TECH D3). Group grants on
// a Project are the ProjectService's (D4).
type GroupService interface {
	List(context.Context, auth.UserPrincipal) ([]Group, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (GroupDetail, error)
	Create(context.Context, auth.UserPrincipal, GroupInput) (GroupDetail, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, GroupUpdateInput) (GroupDetail, error)
	Delete(context.Context, auth.UserPrincipal, uuid.UUID, int64) error
	ChangeMembers(context.Context, auth.UserPrincipal, uuid.UUID, GroupMembersInput) (GroupDetail, error)
	Preview(context.Context, auth.UserPrincipal, uuid.UUID, GroupChange) (GroupPreview, error)
}

var _ GroupService = (*DefaultGroupService)(nil)

type DefaultGroupService struct {
	store          *db.Store
	bus            *events.Bus
	inboxPresenter inbox.Presenter
}

// NewGroupService accepts a nil inboxPresenter, as NewProjectService does:
// the Inbox items a Group change writes still reach their readers through
// their frames.
func NewGroupService(store *db.Store, bus *events.Bus, inboxPresenter inbox.Presenter) (*DefaultGroupService, error) {
	if store == nil || bus == nil {
		return nil, errors.New("Group store and event bus are required")
	}
	return &DefaultGroupService{store: store, bus: bus, inboxPresenter: inboxPresenter}, nil
}

// List reads every Group of the Company, each with what it holds on the
// Projects the caller can see.
func (s *DefaultGroupService) List(ctx context.Context, principal auth.UserPrincipal) ([]Group, error) {
	q := s.store.Queries()
	rows, err := q.ListCompanyGroups(ctx, principal.CompanyID)
	if err != nil {
		return nil, fmt.Errorf("list Groups: %w", err)
	}
	projects, err := visibleGroupProjects(ctx, q, principal, uuid.NullUUID{})
	if err != nil {
		return nil, err
	}
	groups := make([]Group, len(rows))
	for i, row := range rows {
		groups[i] = groupOf(row, projects[row.ID])
	}
	return groups, nil
}

// Get reads one Group with its members.
func (s *DefaultGroupService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	groupID uuid.UUID,
) (GroupDetail, error) {
	if groupID == uuid.Nil {
		return GroupDetail{}, ErrGroupNotFound
	}
	return readGroupDetail(ctx, s.store.Queries(), principal, groupID)
}

// Create writes an empty Group. Nothing moves access until people join it
// and it holds a grant.
func (s *DefaultGroupService) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	input GroupInput,
) (GroupDetail, error) {
	name, description, err := normalizeGroupText(input.Name, input.Description)
	if err != nil {
		return GroupDetail{}, err
	}
	var pending []events.Event
	var value GroupDetail
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		if err := lockGroupMutation(ctx, q, principal); err != nil {
			return err
		}
		count, err := q.CountCompanyGroups(ctx, principal.CompanyID)
		if err != nil {
			return fmt.Errorf("count Groups: %w", err)
		}
		if count >= maxCompanyGroups {
			return ErrGroupLimitReached
		}
		row, err := q.InsertCompanyGroup(ctx, generated.InsertCompanyGroupParams{
			CompanyID:   principal.CompanyID,
			Name:        name,
			Description: description,
			ActorUserID: principal.UserID,
		})
		if err != nil {
			return groupWriteError(err)
		}
		if err := recordGroupInTx(ctx, q, &pending, principal, groupCreated, row, nil); err != nil {
			return err
		}
		pending = append(
			pending,
			groupChangedEvent(principal.CompanyID, userActor(principal), row.ID, row.Version),
		)
		value = GroupDetail{Group: groupOf(groupRowOf(row, 0), nil), Members: []GroupMember{}}
		return nil
	})
	if err != nil {
		return GroupDetail{}, err
	}
	s.publish(ctx, pending)
	return value, nil
}

// Update renames or redescribes a Group. Saving the same text again writes
// nothing.
func (s *DefaultGroupService) Update(
	ctx context.Context,
	principal auth.UserPrincipal,
	groupID uuid.UUID,
	input GroupUpdateInput,
) (GroupDetail, error) {
	if groupID == uuid.Nil {
		return GroupDetail{}, ErrGroupNotFound
	}
	if (input.Name == nil && input.Description == nil) || input.ExpectedRevision < 1 {
		return GroupDetail{}, ErrGroupInvalidInput
	}
	var pending []events.Event
	var value GroupDetail
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		current, err := lockGroupForChange(ctx, q, principal, groupID)
		if err != nil {
			return err
		}
		if current.Version != input.ExpectedRevision {
			return ErrGroupConflict
		}
		name, description := current.Name, current.Description
		if input.Name != nil {
			name = *input.Name
		}
		if input.Description != nil {
			description = *input.Description
		}
		if name, description, err = normalizeGroupText(name, description); err != nil {
			return err
		}
		if name != current.Name || description != current.Description {
			row, err := q.UpdateCompanyGroup(ctx, generated.UpdateCompanyGroupParams{
				CompanyID: principal.CompanyID, GroupID: groupID, Name: name, Description: description,
				ExpectedVersion: input.ExpectedRevision,
			})
			if err != nil {
				return groupWriteError(err)
			}
			if err := recordGroupInTx(ctx, q, &pending, principal, groupUpdated, row, nil); err != nil {
				return err
			}
			pending = append(
				pending,
				groupChangedEvent(principal.CompanyID, userActor(principal), row.ID, row.Version),
			)
		}
		value, err = readGroupDetail(ctx, q, principal, groupID)
		return err
	})
	if err != nil {
		return GroupDetail{}, err
	}
	s.publish(ctx, pending)
	return value, nil
}

// Delete removes a Group. Every member loses what only this Group gave
// them, on every Project it held: each Project records the Group's grant
// going (project_grant.group_changed), and the Runs and queued items of
// anyone who lost read, write or execute end.
func (s *DefaultGroupService) Delete(
	ctx context.Context,
	principal auth.UserPrincipal,
	groupID uuid.UUID,
	expectedRevision int64,
) error {
	if groupID == uuid.Nil {
		return ErrGroupNotFound
	}
	if expectedRevision < 1 {
		return ErrGroupInvalidInput
	}
	var pending []events.Event
	var notices []inbox.CreateResult
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending, notices = nil, nil
		group, err := lockGroupForChange(ctx, q, principal, groupID)
		if err != nil {
			return err
		}
		if group.Version != expectedRevision {
			return ErrGroupConflict
		}
		reach, err := lockGroupReach(ctx, q, principal, groupID)
		if err != nil {
			return err
		}
		members, err := groupMemberIDs(ctx, q, principal.CompanyID, groupID)
		if err != nil {
			return err
		}
		change, err := beginGroupAccessChange(ctx, q, principal, reach, members)
		if err != nil {
			return err
		}
		if _, err := q.DeleteCompanyGroup(ctx, generated.DeleteCompanyGroupParams{
			CompanyID: principal.CompanyID, GroupID: groupID, ExpectedVersion: expectedRevision,
		}); errors.Is(err, pgx.ErrNoRows) {
			return ErrGroupConflict
		} else if err != nil {
			return fmt.Errorf("delete Group: %w", err)
		}
		if err := change.resolveAfter(ctx, q); err != nil {
			return err
		}
		if err := change.advance(ctx, q, &pending, func(project *groupProjectChange) error {
			return recordProjectGroupGrantInTx(
				ctx,
				q,
				&pending,
				principal,
				projectGrantGroupChanged,
				project.project.ID,
				projectGroupGrantChange{
					ProjectID: project.project.ID, ProjectTitle: project.project.Title,
					GroupID: groupID, GroupName: group.Name,
					Before: auth.ProjectPermissionStrings(project.set), After: []string{},
				},
			)
		}); err != nil {
			return err
		}
		if err := recordGroupInTx(ctx, q, &pending, principal, groupDeleted, group, nil); err != nil {
			return err
		}
		pending = append(pending, groupDeletedEvent(principal.CompanyID, userActor(principal), groupID))
		return change.sweep(ctx, q, &pending, &notices)
	})
	if err != nil {
		return err
	}
	s.publish(ctx, pending)
	s.present(notices)
	return nil
}

// ChangeMembers adds and removes people in one change against one revision.
// Everyone the change touches is resolved on every Project the Group reaches
// before and after; each of those Projects advances its access revision and
// records one project_grant.group_member_added row naming every added person
// whose stored access there moved, and one _removed row for the removed
// ones. When an addition lets people read a restricted Project they did not
// read, every grant manager of that Project but the actor (and the person,
// when only one opened it) gets one project_access_granted Inbox item.
// Whoever lost read, write or execute has their Runs and queued items there
// ended, as a person grant change would.
func (s *DefaultGroupService) ChangeMembers(
	ctx context.Context,
	principal auth.UserPrincipal,
	groupID uuid.UUID,
	input GroupMembersInput,
) (GroupDetail, error) {
	if groupID == uuid.Nil {
		return GroupDetail{}, ErrGroupNotFound
	}
	add, remove, err := normalizeGroupMemberChange(input.AddUserIDs, input.RemoveUserIDs)
	if err != nil {
		return GroupDetail{}, err
	}
	if input.ExpectedRevision < 1 {
		return GroupDetail{}, ErrGroupInvalidInput
	}
	var pending []events.Event
	var notices []inbox.CreateResult
	var value GroupDetail
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending, notices = nil, nil
		group, err := lockGroupForChange(ctx, q, principal, groupID)
		if err != nil {
			return err
		}
		if group.Version != input.ExpectedRevision {
			return ErrGroupConflict
		}
		current, err := groupMemberIDs(ctx, q, principal.CompanyID, groupID)
		if err != nil {
			return err
		}
		added, removed, err := planGroupMemberChange(ctx, q, principal.CompanyID, current, add, remove)
		if err != nil {
			return err
		}
		if len(added) > 0 || len(removed) > 0 {
			if err := s.changeMembersInTx(
				ctx,
				q,
				&pending,
				&notices,
				principal,
				group,
				added,
				removed,
			); err != nil {
				return err
			}
		}
		value, err = readGroupDetail(ctx, q, principal, groupID)
		return err
	})
	if err != nil {
		return GroupDetail{}, err
	}
	s.publish(ctx, pending)
	s.present(notices)
	return value, nil
}

// changeMembersInTx writes a membership change that adds or removes someone.
// The caller holds the Company, member and Group locks.
func (s *DefaultGroupService) changeMembersInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	notices *[]inbox.CreateResult,
	principal auth.UserPrincipal,
	group generated.CompanyGroup,
	added, removed []uuid.UUID,
) error {
	reach, err := lockGroupReach(ctx, q, principal, group.ID)
	if err != nil {
		return err
	}
	change, err := beginGroupAccessChange(ctx, q, principal, reach, slices.Concat(added, removed))
	if err != nil {
		return err
	}
	if len(removed) > 0 {
		if _, err := q.DeleteCompanyGroupMembers(ctx, generated.DeleteCompanyGroupMembersParams{
			CompanyID: principal.CompanyID, GroupID: group.ID, UserIds: removed,
		}); err != nil {
			return fmt.Errorf("remove Group members: %w", err)
		}
	}
	if len(added) > 0 {
		inserted, err := q.InsertCompanyGroupMembers(ctx, generated.InsertCompanyGroupMembersParams{
			CompanyID:   principal.CompanyID,
			GroupID:     group.ID,
			UserIds:     added,
			ActorUserID: principal.UserID,
		})
		if err != nil {
			return fmt.Errorf("add Group members: %w", err)
		}
		if len(inserted) != len(added) {
			return ErrGroupMemberRequired
		}
	}
	if err := change.resolveAfter(ctx, q); err != nil {
		return err
	}
	names, err := groupNoticeNames(ctx, q, principal, added)
	if err != nil {
		return err
	}
	if err := change.advance(ctx, q, pending, func(project *groupProjectChange) error {
		if err := change.recordMemberTrail(ctx, q, pending, group, project, added, removed); err != nil {
			return err
		}
		return change.noticeOpened(ctx, q, pending, notices, group, project, added, names)
	}); err != nil {
		return err
	}
	for _, person := range added {
		if err := recordGroupInTx(ctx, q, pending, principal, groupMemberAdded, group, &person); err != nil {
			return err
		}
	}
	for _, person := range removed {
		if err := recordGroupInTx(ctx, q, pending, principal, groupMemberRemoved, group, &person); err != nil {
			return err
		}
	}
	revision, err := q.BumpCompanyGroupVersion(ctx, generated.BumpCompanyGroupVersionParams{
		CompanyID: principal.CompanyID, GroupID: group.ID,
	})
	if err != nil {
		return fmt.Errorf("advance Group revision: %w", err)
	}
	*pending = append(*pending, groupChangedEvent(principal.CompanyID, userActor(principal), group.ID, revision))
	return change.sweep(ctx, q, pending, notices)
}

// Preview answers what a membership change or a delete would do, as the
// save would do it, with the Group's current revision. It writes nothing and
// takes no lock, and it refuses what the save would refuse: a Group not in
// the Company, someone who may not join, a cap, or a change that moves access
// its author could not move directly.
func (s *DefaultGroupService) Preview(
	ctx context.Context,
	principal auth.UserPrincipal,
	groupID uuid.UUID,
	change GroupChange,
) (GroupPreview, error) {
	if groupID == uuid.Nil {
		return GroupPreview{}, ErrGroupNotFound
	}
	add, remove, err := normalizeGroupMemberChange(change.AddUserIDs, change.RemoveUserIDs)
	if err != nil {
		return GroupPreview{}, err
	}
	if change.Delete && (len(add) > 0 || len(remove) > 0) {
		return GroupPreview{}, ErrGroupInvalidInput
	}
	if !principal.Role.Can(auth.PermissionMembersManage) {
		return GroupPreview{}, ErrForbidden
	}
	q := s.store.Queries()
	group, err := q.GetCompanyGroup(
		ctx,
		generated.GetCompanyGroupParams{CompanyID: principal.CompanyID, GroupID: groupID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GroupPreview{}, ErrGroupNotFound
	}
	if err != nil {
		return GroupPreview{}, fmt.Errorf("read Group: %w", err)
	}
	current, err := groupMemberIDs(ctx, q, principal.CompanyID, groupID)
	if err != nil {
		return GroupPreview{}, err
	}
	added, removed := []uuid.UUID{}, current
	if !change.Delete {
		if added, removed, err = planGroupMemberChange(
			ctx,
			q,
			principal.CompanyID,
			current,
			add,
			remove,
		); err != nil {
			return GroupPreview{}, err
		}
	}
	reached, err := q.ListGroupProjects(
		ctx,
		generated.ListGroupProjectsParams{CompanyID: principal.CompanyID, GroupID: groupID},
	)
	if err != nil {
		return GroupPreview{}, fmt.Errorf("list Group Projects: %w", err)
	}
	projectIDs := make([]uuid.UUID, len(reached))
	for i, row := range reached {
		projectIDs[i] = row.ProjectID
	}
	authors, err := auth.ResolvePeopleProjectAccess(
		ctx,
		q,
		principal.CompanyID,
		projectIDs,
		[]uuid.UUID{principal.UserID},
	)
	if err != nil {
		return GroupPreview{}, err
	}
	targets := make([]groupPreviewTarget, 0, len(reached))
	for _, row := range reached {
		access := authors[row.ProjectID][principal.UserID]
		set := storedProjectPermissions(row.Permissions)
		if !groupChangePermitted(access, set) {
			return GroupPreview{}, ErrGroupGrantPermissionRequired
		}
		after := make(map[uuid.UUID][]auth.ProjectPermission, len(added))
		for _, person := range added {
			after[person] = set
		}
		targets = append(targets, groupPreviewTarget{
			projectID: row.ProjectID, visibility: access.Visibility, visible: access.Visible(),
			people: slices.Concat(added, removed), after: after,
		})
	}
	return previewGroupChange(ctx, q, principal, groupID, group.Version, targets)
}

func (s *DefaultGroupService) publish(ctx context.Context, pending []events.Event) {
	for _, batch := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, batch.Topic, batch.Events...)
	}
}

// present shows, after commit, the Inbox items a Group change wrote: the
// notices that a Project opened to someone, and those of Runs it stopped.
func (s *DefaultGroupService) present(notices []inbox.CreateResult) {
	for _, notice := range notices {
		inbox.PresentCreated(s.inboxPresenter, notice)
	}
}

// normalizeGroupText trims the name and description and checks them as
// company_groups does: a name of 1 to 80 characters, a description of at
// most 500.
func normalizeGroupText(name, description string) (string, string, error) {
	name = protocol.TrimContractWhitespace(name)
	description = protocol.TrimContractWhitespace(description)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 80 ||
		!utf8.ValidString(description) || utf8.RuneCountInString(description) > 500 {
		return "", "", ErrGroupInvalidInput
	}
	return name, description, nil
}

// normalizeGroupMemberChange sorts the ids a membership change names and
// drops repeats. Nobody may be both added and removed, nobody may be the nil
// id, and one change names at most maxGroupChangeIDs people.
func normalizeGroupMemberChange(add, remove []uuid.UUID) ([]uuid.UUID, []uuid.UUID, error) {
	add, remove = sortedUserIDs(add), sortedUserIDs(remove)
	if len(add)+len(remove) > maxGroupChangeIDs {
		return nil, nil, ErrGroupInvalidInput
	}
	for _, id := range slices.Concat(add, remove) {
		if id == uuid.Nil {
			return nil, nil, ErrGroupInvalidInput
		}
	}
	for _, id := range add {
		if _, found := slices.BinarySearchFunc(remove, id, compareUUID); found {
			return nil, nil, ErrGroupInvalidInput
		}
	}
	return add, remove, nil
}

// planGroupMemberChange decides who a membership change really adds (named,
// not in yet) and removes (named, in). Everyone named to be added must be
// able to join, including someone already in; the Group must stay within
// maxGroupMembers.
func planGroupMemberChange(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	current, add, remove []uuid.UUID,
) (added, removed []uuid.UUID, err error) {
	if len(add) > 0 {
		eligible, err := q.ListGroupMemberCandidateIDs(ctx, generated.ListGroupMemberCandidateIDsParams{
			CompanyID: companyID, UserIds: add,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("check Group members: %w", err)
		}
		if len(eligible) != len(add) {
			return nil, nil, ErrGroupMemberRequired
		}
	}
	added, removed = []uuid.UUID{}, []uuid.UUID{}
	for _, id := range add {
		if !slices.Contains(current, id) {
			added = append(added, id)
		}
	}
	for _, id := range remove {
		if slices.Contains(current, id) {
			removed = append(removed, id)
		}
	}
	if len(current)+len(added)-len(removed) > maxGroupMembers {
		return nil, nil, ErrGroupLimitReached
	}
	return added, removed, nil
}

// groupChangePermitted is "no laundering" for one Project a Group reaches:
// the author of a membership change or a delete must manage the Project's
// access (project.grants.manage, the management bridge counts), and when
// they manage it only through a grant, the Group's set there must sit inside
// their own (the self-grant cap). Today every members.manage holder also
// holds projects.manage, so it always passes; it guards a later change of
// the roles.
func groupChangePermitted(author auth.ProjectAccess, groupSet []auth.ProjectPermission) bool {
	return author.Can(auth.ProjectPermissionGrantsManage) && grantWithinOwn(author, groupSet, nil)
}

// lockGroupMutation takes the Company lock, then the acting member's row,
// and rechecks members.manage beside it.
func lockGroupMutation(ctx context.Context, q *generated.Queries, principal auth.UserPrincipal) error {
	if _, err := q.LockCompanyForMembershipMutation(ctx, principal.CompanyID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock Company for Group change: %w", err)
	}
	_, err := authorizeMembershipActor(ctx, q, principal.CompanyID, principal.UserID, auth.PermissionMembersManage)
	return err
}

// lockGroupForChange takes lockGroupMutation, then the Group FOR UPDATE.
// Lock order: company -> member -> group -> project -> conversation -> run.
func lockGroupForChange(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	groupID uuid.UUID,
) (generated.CompanyGroup, error) {
	if err := lockGroupMutation(ctx, q, principal); err != nil {
		return generated.CompanyGroup{}, err
	}
	group, err := q.LockCompanyGroupForChange(ctx, generated.LockCompanyGroupForChangeParams{
		CompanyID: principal.CompanyID, GroupID: groupID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.CompanyGroup{}, ErrGroupNotFound
	}
	if err != nil {
		return generated.CompanyGroup{}, fmt.Errorf("lock Group: %w", err)
	}
	return group, nil
}

// groupReach is what a Group holds, Project by Project, locked for a change.
type groupReach struct {
	projects []generated.Project
	sets     map[uuid.UUID][]auth.ProjectPermission
}

// lockGroupReach locks every Project the Group holds a grant on, FOR UPDATE
// in id order, then reads the author's standing on all of them at once and
// checks "no laundering" on each. The caller holds the Group lock.
func lockGroupReach(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	groupID uuid.UUID,
) (groupReach, error) {
	rows, err := q.ListGroupProjects(
		ctx,
		generated.ListGroupProjectsParams{CompanyID: principal.CompanyID, GroupID: groupID},
	)
	if err != nil {
		return groupReach{}, fmt.Errorf("list Group Projects: %w", err)
	}
	reach := groupReach{sets: make(map[uuid.UUID][]auth.ProjectPermission, len(rows))}
	projectIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		project, err := q.GetProjectForUpdate(ctx, generated.GetProjectForUpdateParams{
			CompanyID: principal.CompanyID, ProjectID: row.ProjectID,
		})
		if err != nil {
			return groupReach{}, fmt.Errorf("lock Group Project: %w", err)
		}
		reach.projects = append(reach.projects, project)
		reach.sets[project.ID] = storedProjectPermissions(row.Permissions)
		projectIDs = append(projectIDs, project.ID)
	}
	authors, err := auth.ResolvePeopleProjectAccess(
		ctx,
		q,
		principal.CompanyID,
		projectIDs,
		[]uuid.UUID{principal.UserID},
	)
	if err != nil {
		return groupReach{}, err
	}
	for _, project := range reach.projects {
		if !groupChangePermitted(authors[project.ID][principal.UserID], reach.sets[project.ID]) {
			return groupReach{}, ErrGroupGrantPermissionRequired
		}
	}
	return reach, nil
}

// groupAccessChange is one Group change across the Projects it reaches: the
// standing of each person it touches there before and after the write, and
// each Project's access change (its audience and revision).
type groupAccessChange struct {
	principal auth.UserPrincipal
	people    []uuid.UUID
	projects  []groupProjectChange
}

// groupProjectChange is a Group change on one Project. set is what the Group
// held there before the change.
type groupProjectChange struct {
	project generated.Project
	set     []auth.ProjectPermission
	access  *projectAccessChange
	before  map[uuid.UUID]auth.ProjectAccess
	after   map[uuid.UUID]auth.ProjectAccess
}

// beginGroupAccessChange resolves everyone the change touches on every
// Project the Group reaches in one read, and each Project's readers, before
// the write.
func beginGroupAccessChange(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	reach groupReach,
	people []uuid.UUID,
) (*groupAccessChange, error) {
	change := &groupAccessChange{principal: principal, people: people}
	for _, project := range reach.projects {
		access, err := beginProjectAccessChange(ctx, q, principal, project)
		if err != nil {
			return nil, err
		}
		change.projects = append(change.projects, groupProjectChange{
			project: project, set: reach.sets[project.ID], access: access,
		})
	}
	before, err := change.resolve(ctx, q)
	if err != nil {
		return nil, err
	}
	for i := range change.projects {
		change.projects[i].before = before[change.projects[i].project.ID]
	}
	return change, nil
}

// resolveAfter resolves everyone again, in one read, once the change is
// written.
func (c *groupAccessChange) resolveAfter(ctx context.Context, q *generated.Queries) error {
	after, err := c.resolve(ctx, q)
	if err != nil {
		return err
	}
	for i := range c.projects {
		c.projects[i].after = after[c.projects[i].project.ID]
	}
	return nil
}

// resolve reads everyone the change touches on every Project it reaches.
func (c *groupAccessChange) resolve(
	ctx context.Context,
	q *generated.Queries,
) (map[uuid.UUID]map[uuid.UUID]auth.ProjectAccess, error) {
	projectIDs := make([]uuid.UUID, len(c.projects))
	for i, project := range c.projects {
		projectIDs[i] = project.project.ID
	}
	return auth.ResolvePeopleProjectAccess(ctx, q, c.principal.CompanyID, projectIDs, c.people)
}

// advance moves every reached Project's access revision on (so a Group grant
// editor holding the old revision is refused, even where nobody's access
// moved), writes the Project's trail with record, and stages
// project.access_changed for its readers before and after, the
// projects.manage holders and the actor.
func (c *groupAccessChange) advance(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	record func(*groupProjectChange) error,
) error {
	for i := range c.projects {
		project := &c.projects[i]
		if _, err := project.access.advance(ctx, q); err != nil {
			return err
		}
		if err := record(project); err != nil {
			return err
		}
		if err := project.access.stage(ctx, q, pending); err != nil {
			return err
		}
	}
	return nil
}

// recordMemberTrail writes the Project's trail of a membership change: one
// project_grant.group_member_added row naming every added person whose
// stored set on the Project moved, and one project_grant.group_member_removed
// row for the removed people whose set moved. A side where nobody's set moved
// (a person whose own grant already held everything the Group gives) writes
// no row.
func (c *groupAccessChange) recordMemberTrail(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	group generated.CompanyGroup,
	project *groupProjectChange,
	added, removed []uuid.UUID,
) error {
	for _, side := range []struct {
		action string
		people []uuid.UUID
	}{
		{projectGrantGroupMemberAdded, added},
		{projectGrantGroupMemberRemoved, removed},
	} {
		moved := project.storedSetMoved(side.people)
		if len(moved) == 0 {
			continue
		}
		if err := recordProjectGroupGrantInTx(
			ctx,
			q,
			pending,
			c.principal,
			side.action,
			project.project.ID,
			projectGroupMembersChange{
				ProjectID: project.project.ID, ProjectTitle: project.project.Title,
				GroupID: group.ID, GroupName: group.Name, UserIDs: moved, Count: len(moved),
			},
		); err != nil {
			return err
		}
	}
	return nil
}

// storedSetMoved lists, sorted, the people whose stored set on the Project
// (their own rows and their Groups' rows) differs before and after.
func (p *groupProjectChange) storedSetMoved(people []uuid.UUID) []uuid.UUID {
	moved := []uuid.UUID{}
	for _, person := range people {
		if !slices.Equal(storedProjectSet(p.before[person]), storedProjectSet(p.after[person])) {
			moved = append(moved, person)
		}
	}
	return sortedUserIDs(moved)
}

// noticeOpened tells every grant manager of a restricted Project, once, that
// an addition let people read it who did not (project_access_granted), an
// admin adding themself included. The actor gets none, and neither does the
// person when only one opened the Project. The Project's access revision has
// advanced, so it keys the dedupe.
func (c *groupAccessChange) noticeOpened(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	notices *[]inbox.CreateResult,
	group generated.CompanyGroup,
	project *groupProjectChange,
	added []uuid.UUID,
	names map[uuid.UUID]string,
) error {
	if auth.ProjectVisibility(project.project.Visibility) == auth.ProjectVisibilityCompany {
		return nil
	}
	var opened []uuid.UUID
	for _, person := range added {
		if project.before[person].Via == "" && project.after[person].Via != "" {
			opened = append(opened, person)
		}
	}
	if len(opened) == 0 {
		return nil
	}
	sets := auth.ProjectPermissionHeldSets()
	managers, err := q.ListProjectGrantManagerIDs(ctx, generated.ListProjectGrantManagerIDsParams{
		CompanyID: c.principal.CompanyID, ProjectID: project.project.ID,
		Permission:                string(auth.ProjectPermissionGrantsManage),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
	})
	if err != nil {
		return fmt.Errorf("list Project grant managers: %w", err)
	}
	actor := c.principal.UserID
	title, told := openedNoticeTitle(opened, names, project.project.Title)
	link := "/team?view=projects&project=" + project.project.ID.String()
	for _, manager := range managers {
		if manager == actor || manager == told {
			continue
		}
		notice, err := inbox.CreateInTx(ctx, q, pending, inbox.CreateInput{
			CompanyID:       c.principal.CompanyID,
			RecipientUserID: manager,
			Type:            inbox.TypeProjectAccessGranted,
			Entity:          inbox.Entity{Type: protocol.EntityProject, ID: project.project.ID.String()},
			Title:           groupNoticeLabel(title, 160),
			Body:            groupNoticeLabel("Added to "+group.Name+" by "+names[actor]+".", 2048),
			DeepLink:        &link,
			DedupeKey: fmt.Sprintf("project:%s:group:%s:%d",
				project.project.ID, group.ID, project.access.revision),
			Actor: inbox.Actor{Kind: "user", ID: &actor},
		})
		if err != nil {
			return fmt.Errorf("create Project access Inbox item: %w", err)
		}
		if notice.Created {
			*notices = append(*notices, notice)
		}
	}
	return nil
}

// openedNoticeTitle is a project_access_granted title: "{Person} can now see
// {Project}" when one person opened the Project, and that person, who is not
// told; "{N} people can now see {Project}" when several did, and nobody left
// out.
func openedNoticeTitle(opened []uuid.UUID, names map[uuid.UUID]string, project string) (string, uuid.UUID) {
	if len(opened) == 1 {
		return names[opened[0]] + " can now see " + project, opened[0]
	}
	return fmt.Sprintf("%d people can now see %s", len(opened), project), uuid.Nil
}

// sweep ends what each person lost, Project by Project, once every Project's
// trail is written: for each person who lost read, write or execute there,
// queued items without read or write, then Runs without read or execute
// (applyProjectAccessLossInTx). One read first finds the pairs that have a
// queued item or a live Run there at all (ListProjectAccessSweepPairs);
// nobody else is visited, since they would find nothing to end. It takes
// Conversation and Run locks, so nothing after it takes a Group or Project
// lock.
func (c *groupAccessChange) sweep(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	notices *[]inbox.CreateResult,
) error {
	type pair struct{ project, person uuid.UUID }
	lost := map[pair]bool{}
	var projectIDs, people []uuid.UUID
	for _, project := range c.projects {
		for _, person := range c.people {
			before, after := project.before[person], project.after[person]
			if projectPostingLost(before, after) || projectWorkLost(before, after) {
				lost[pair{project.project.ID, person}] = true
				projectIDs = append(projectIDs, project.project.ID)
				people = append(people, person)
			}
		}
	}
	if len(lost) == 0 {
		return nil
	}
	rows, err := q.ListProjectAccessSweepPairs(ctx, generated.ListProjectAccessSweepPairsParams{
		CompanyID:  c.principal.CompanyID,
		UserIds:    sortedUserIDs(people),
		ProjectIds: sortedUserIDs(projectIDs),
	})
	if err != nil {
		return fmt.Errorf("list what a Group change ends: %w", err)
	}
	held := make(map[pair]bool, len(rows))
	for _, row := range rows {
		held[pair{row.ProjectID, row.UserID}] = true
	}
	for _, project := range c.projects {
		for _, person := range c.people {
			key := pair{project.project.ID, person}
			if !lost[key] || !held[key] {
				continue
			}
			before, after := project.before[person], project.after[person]
			if err := applyProjectAccessLossInTx(
				ctx,
				q,
				pending,
				notices,
				c.principal.CompanyID,
				project.project.ID,
				person,
				before,
				after,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// storedProjectSet is what a person holds on a Project from every stored
// source, their own rows and their Groups' rows, as the Activity row records
// it: no Company floor and no management bridge.
func storedProjectSet(access auth.ProjectAccess) []string {
	permissions := slices.Concat(access.Granted, access.GroupGranted)
	slices.Sort(permissions)
	return auth.ProjectPermissionStrings(slices.Compact(permissions))
}

// groupMemberIDs lists the Group's members, sorted by id.
func groupMemberIDs(ctx context.Context, q *generated.Queries, companyID, groupID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.ListCompanyGroupMembers(
		ctx,
		generated.ListCompanyGroupMembersParams{CompanyID: companyID, GroupID: groupID},
	)
	if err != nil {
		return nil, fmt.Errorf("list Group members: %w", err)
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.UserID
	}
	return sortedUserIDs(ids), nil
}

// groupNoticeNames reads the names a project_access_granted item shows: the
// actor's and each added person's. A missing name reads "Someone".
func groupNoticeNames(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	added []uuid.UUID,
) (map[uuid.UUID]string, error) {
	names := make(map[uuid.UUID]string, len(added)+1)
	if len(added) == 0 {
		return names, nil
	}
	rows, err := q.ListCompanyMemberNames(ctx, generated.ListCompanyMemberNamesParams{
		CompanyID: principal.CompanyID, UserIds: append([]uuid.UUID{principal.UserID}, added...),
	})
	if err != nil {
		return nil, fmt.Errorf("read Group notice names: %w", err)
	}
	for _, id := range append([]uuid.UUID{principal.UserID}, added...) {
		names[id] = "Someone"
	}
	for _, row := range rows {
		if name := strings.Join(strings.Fields(row.DisplayName), " "); name != "" {
			names[row.UserID] = name
		}
	}
	return names, nil
}

// groupNoticeLabel fits text into an Inbox field of maximum characters, on
// one line.
func groupNoticeLabel(value string, maximum int) string {
	label := []rune(strings.Join(strings.Fields(value), " "))
	if len(label) > maximum {
		return string(label[:maximum-1]) + "…"
	}
	return string(label)
}

// leaveCompanyGroupsInTx ends every Group membership of a person leaving the
// Company (D5), advancing each Group's revision and staging group.changed.
// A rejoin restores none. The caller holds the Company and member locks and
// has taken no Project lock.
func leaveCompanyGroupsInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	author projectGrantAuthor,
	userID uuid.UUID,
) error {
	groups, err := q.DeleteCompanyGroupMembershipsForUser(ctx, generated.DeleteCompanyGroupMembershipsForUserParams{
		CompanyID: author.companyID, UserID: userID,
	})
	if err != nil {
		return fmt.Errorf("end departing member's Groups: %w", err)
	}
	for _, groupID := range sortedUserIDs(groups) {
		revision, err := q.BumpCompanyGroupVersion(ctx, generated.BumpCompanyGroupVersionParams{
			CompanyID: author.companyID, GroupID: groupID,
		})
		if err != nil {
			return fmt.Errorf("advance Group revision: %w", err)
		}
		*pending = append(*pending, groupChangedEvent(author.companyID, author.actor, groupID, revision))
	}
	return nil
}

// readGroupDetail reads one Group, its members and what it holds on the
// Projects the caller can see.
func readGroupDetail(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	groupID uuid.UUID,
) (GroupDetail, error) {
	row, err := q.GetCompanyGroup(
		ctx,
		generated.GetCompanyGroupParams{CompanyID: principal.CompanyID, GroupID: groupID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GroupDetail{}, ErrGroupNotFound
	}
	if err != nil {
		return GroupDetail{}, fmt.Errorf("read Group: %w", err)
	}
	members, err := q.ListCompanyGroupMembers(ctx, generated.ListCompanyGroupMembersParams{
		CompanyID: principal.CompanyID, GroupID: groupID,
	})
	if err != nil {
		return GroupDetail{}, fmt.Errorf("list Group members: %w", err)
	}
	projects, err := visibleGroupProjects(ctx, q, principal, uuid.NullUUID{UUID: groupID, Valid: true})
	if err != nil {
		return GroupDetail{}, err
	}
	detail := GroupDetail{
		Group: groupOf(
			generated.ListCompanyGroupsRow(row),
			projects[groupID],
		),
		Members: make([]GroupMember, len(members)),
	}
	for i, member := range members {
		detail.Members[i] = GroupMember{UserID: member.UserID, AddedAt: member.CreatedAt.Time}
		if member.AddedByUserID.Valid {
			addedBy := member.AddedByUserID.UUID
			detail.Members[i].AddedBy = &addedBy
		}
	}
	return detail, nil
}

// visibleGroupProjects reads what each Group (or the one named) holds on the
// Projects the caller can see, by Group.
func visibleGroupProjects(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	groupID uuid.NullUUID,
) (map[uuid.UUID][]GroupProject, error) {
	rows, err := q.ListVisibleGroupProjects(ctx, generated.ListVisibleGroupProjectsParams{
		CompanyID: principal.CompanyID, GroupID: groupID, ViewerUserID: principal.UserID,
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
	})
	if err != nil {
		return nil, fmt.Errorf("list Group Projects: %w", err)
	}
	projects := make(map[uuid.UUID][]GroupProject)
	for _, row := range rows {
		projects[row.GroupID] = append(projects[row.GroupID], GroupProject{
			ProjectID: row.ProjectID, Permissions: storedProjectPermissions(row.Permissions),
		})
	}
	return projects, nil
}

// groupOf projects a Group row. Projects is never nil.
func groupOf(row generated.ListCompanyGroupsRow, projects []GroupProject) Group {
	if projects == nil {
		projects = []GroupProject{}
	}
	return Group{
		ID: row.ID, Name: row.Name, Description: row.Description, MemberCount: row.MemberCount,
		Revision: row.Version, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, Projects: projects,
	}
}

// groupRowOf adds a member count to a written Group row.
func groupRowOf(row generated.CompanyGroup, members int64) generated.ListCompanyGroupsRow {
	return generated.ListCompanyGroupsRow{
		ID: row.ID, CompanyID: row.CompanyID, Name: row.Name, Description: row.Description,
		CreatedByUserID: row.CreatedByUserID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Version: row.Version, MemberCount: members,
	}
}

// groupWriteError maps a Group write that matched no row (a stale revision)
// or hit company_groups_name_key (a name in use) to group_conflict.
func groupWriteError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGroupConflict
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrGroupConflict
	}
	return fmt.Errorf("write Group: %w", err)
}

// groupReceipt is the Activity payload of a group row. UserID is the person
// added or removed, absent on a create, rename or delete.
type groupReceipt struct {
	GroupID uuid.UUID  `json:"groupId"`
	Name    string     `json:"name"`
	UserID  *uuid.UUID `json:"userId,omitempty"`
}

// recordGroupInTx writes a group row. It names no Project and no addressee,
// so every active member reads it, as they read the Team page.
func recordGroupInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	action string,
	group generated.CompanyGroup,
	userID *uuid.UUID,
) error {
	return activity.RecordEvidenceInTx(ctx, q, pending, activity.EvidenceInput{
		CompanyID: principal.CompanyID,
		EntityID:  group.ID,
		Action:    action,
		Actor:     userActor(principal),
		Payload:   groupReceipt{GroupID: group.ID, Name: group.Name, UserID: userID},
	})
}

// projectGroupGrantChange is the Activity payload of a
// project_grant.group_changed row: the Group's stored set on the Project
// before and after. ProjectTitle and GroupName are as they were when the
// change was made, so the row still reads after the Group is deleted or the
// Project archived; rows written before them carry neither.
type projectGroupGrantChange struct {
	ProjectID    uuid.UUID `json:"projectId"`
	ProjectTitle string    `json:"projectTitle"`
	GroupID      uuid.UUID `json:"groupId"`
	GroupName    string    `json:"groupName"`
	Before       []string  `json:"before"`
	After        []string  `json:"after"`
}

// projectGroupMembersChange is the Activity payload of a
// project_grant.group_member_added or _removed row: one row per Project and
// change, naming every person (sorted) whose stored set there moved, and how
// many they are. What each holds is read from the Project's access, not the
// row (Access TECH, the trail, 2026-10-07). ProjectTitle and GroupName are
// as they were when the change was made.
type projectGroupMembersChange struct {
	ProjectID    uuid.UUID   `json:"projectId"`
	ProjectTitle string      `json:"projectTitle"`
	GroupID      uuid.UUID   `json:"groupId"`
	GroupName    string      `json:"groupName"`
	UserIDs      []uuid.UUID `json:"userIds"`
	Count        int         `json:"count"`
}

// recordProjectGroupGrantInTx writes a project_grant row about a Group. Like
// every project_grant row it names its Project and is addressed to the
// actor, and the Project's readers and projects.manage holders read it.
func recordProjectGroupGrantInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	action string,
	projectID uuid.UUID,
	payload any,
) error {
	return activity.RecordEvidenceInTx(ctx, q, pending, activity.EvidenceInput{
		CompanyID:       principal.CompanyID,
		EntityID:        projectID,
		ProjectID:       projectID,
		Action:          action,
		Actor:           userActor(principal),
		RecipientUserID: principal.UserID,
		Payload:         payload,
	})
}

func userActor(principal auth.UserPrincipal) events.Actor {
	return events.UserActor(principal.UserID.String())
}

// groupChangedEvent stages group.changed: the Group's members, name or
// grants moved. The Activity row, when there is one, is written in the
// transaction, so the evidence sink must not copy the frame.
func groupChangedEvent(companyID uuid.UUID, actor events.Actor, groupID uuid.UUID, revision int64) events.Event {
	return events.Event{
		EntityType: TopicGroup, Name: EventGroupChanged, CompanyID: companyID, CreatedAt: time.Now().UTC(),
		Actor: actor, ActivityRecorded: true, Payload: GroupChanged{GroupID: groupID, Revision: revision},
	}
}

// groupDeletedEvent stages group.deleted.
func groupDeletedEvent(companyID uuid.UUID, actor events.Actor, groupID uuid.UUID) events.Event {
	return events.Event{
		EntityType: TopicGroup, Name: EventGroupDeleted, CompanyID: companyID, CreatedAt: time.Now().UTC(),
		Actor: actor, ActivityRecorded: true, Payload: GroupDeleted{GroupID: groupID},
	}
}

func compareUUID(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }

// sortedUserIDs sorts ids and drops repeats, never nil.
func sortedUserIDs(ids []uuid.UUID) []uuid.UUID {
	sorted := append([]uuid.UUID{}, ids...)
	slices.SortFunc(sorted, compareUUID)
	return slices.Compact(sorted)
}

// groupPreviewTarget is one Project a Group change reaches, as its preview
// reads it: the people the change touches there, and what the Group gives
// each of them after the change (absent when they leave the Group, or the
// Group loses the Project).
type groupPreviewTarget struct {
	projectID  uuid.UUID
	visibility auth.ProjectVisibility
	visible    bool
	people     []uuid.UUID
	after      map[uuid.UUID][]auth.ProjectPermission
}

// accessPath is one stored path by which a person holds one permission on a
// Project: their own grant, or a Group.
type accessPath struct {
	via        auth.ProjectAccessVia
	groupID    uuid.UUID
	permission auth.ProjectPermission
}

// previewGroupChange answers what a Group change would do on each target
// Project, from every touched person's stored paths, without writing: before
// is what they hold now, after is the same without this Group's rows and
// with what the Group gives them after the change. Runs are counted in one
// read, with the sweep's own predicate, for whoever would lose read or
// execute (countPreviewRuns).
func previewGroupChange(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	groupID uuid.UUID,
	revision int64,
	targets []groupPreviewTarget,
) (GroupPreview, error) {
	projectIDs := make([]uuid.UUID, 0, len(targets))
	var people []uuid.UUID
	for _, target := range targets {
		projectIDs = append(projectIDs, target.projectID)
		people = append(people, target.people...)
	}
	people = sortedUserIDs(people)
	paths := map[uuid.UUID]map[uuid.UUID][]accessPath{}
	if len(projectIDs) > 0 && len(people) > 0 {
		rows, err := q.ListPeopleProjectAccessPaths(ctx, generated.ListPeopleProjectAccessPathsParams{
			CompanyID: principal.CompanyID, ProjectIds: projectIDs, UserIds: people,
		})
		if err != nil {
			return GroupPreview{}, fmt.Errorf("list Group preview access paths: %w", err)
		}
		for _, row := range rows {
			if paths[row.ProjectID] == nil {
				paths[row.ProjectID] = map[uuid.UUID][]accessPath{}
			}
			paths[row.ProjectID][row.UserID] = append(paths[row.ProjectID][row.UserID], accessPath{
				via: auth.ProjectAccessVia(
					row.Via,
				),
				groupID:    row.GroupID,
				permission: auth.ProjectPermission(row.Permission),
			})
		}
	}
	outcomes := make([]previewOutcome, 0, len(targets))
	var losing []uuid.UUID
	for _, target := range targets {
		outcome := previewOutcome{projectID: target.projectID, visible: target.visible}
		for _, person := range target.people {
			held := paths[target.projectID][person]
			result := previewPersonOutcome{
				userID: person,
				before: simulatedAccess(target.visibility, held, uuid.Nil, nil),
				after:  simulatedAccess(target.visibility, held, groupID, target.after[person]),
			}
			if target.visible && projectWorkLost(result.before, result.after) {
				losing = append(losing, person)
			}
			outcome.people = append(outcome.people, result)
		}
		outcomes = append(outcomes, outcome)
	}
	if len(losing) > 0 {
		losing = sortedUserIDs(losing)
		if err := countPreviewRuns(ctx, q, principal.CompanyID, projectIDs, losing, outcomes); err != nil {
			return GroupPreview{}, err
		}
	}
	return foldGroupPreview(revision, principal.UserID, outcomes), nil
}

// countPreviewRuns counts, in one read, the Runs the sweep would end for each
// person who would lose read or execute on a visible Project, with the
// sweep's own predicate (CountActiveRunsForProjectAuthorizers, the count of
// ListActiveRunsForProjectAuthorizer).
func countPreviewRuns(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	projectIDs, people []uuid.UUID,
	outcomes []previewOutcome,
) error {
	rows, err := q.CountActiveRunsForProjectAuthorizers(ctx, generated.CountActiveRunsForProjectAuthorizersParams{
		CompanyID: companyID, ProjectIds: projectIDs, UserIds: people,
	})
	if err != nil {
		return fmt.Errorf("count Runs a Group change stops: %w", err)
	}
	type pair struct{ project, person uuid.UUID }
	runs := make(map[pair]int, len(rows))
	for _, row := range rows {
		runs[pair{row.ProjectID, row.UserID}] = int(row.Runs)
	}
	for i := range outcomes {
		if !outcomes[i].visible {
			continue
		}
		for j := range outcomes[i].people {
			person := &outcomes[i].people[j]
			if projectWorkLost(person.before, person.after) {
				person.runs = runs[pair{outcomes[i].projectID, person.userID}]
			}
		}
	}
	return nil
}

// simulatedAccess states what a person holds on a Project from their stored
// paths, without the rows of the dropped Group and with added (what that
// Group gives them after a change), the management bridge left out. It
// applies the rule project_access and ProjectAccessOf apply: read comes from
// an own row, then a Group row, then the Company-visible floor.
func simulatedAccess(
	visibility auth.ProjectVisibility,
	paths []accessPath,
	dropped uuid.UUID,
	added []auth.ProjectPermission,
) auth.ProjectAccess {
	facts := auth.ProjectAccessFacts{Visibility: string(visibility)}
	ownRead, groupRead := false, false
	for _, path := range paths {
		switch {
		case path.via == auth.ProjectAccessViaGrant:
			facts.GrantedPermissions = append(facts.GrantedPermissions, string(path.permission))
			ownRead = ownRead || path.permission == auth.ProjectPermissionRead
		case path.groupID != dropped:
			facts.GroupPermissions = append(facts.GroupPermissions, string(path.permission))
			groupRead = groupRead || path.permission == auth.ProjectPermissionRead
		}
	}
	for _, permission := range added {
		facts.GroupPermissions = append(facts.GroupPermissions, string(permission))
		groupRead = groupRead || permission == auth.ProjectPermissionRead
	}
	switch {
	case ownRead:
		facts.Via = string(auth.ProjectAccessViaGrant)
	case groupRead:
		facts.Via = string(auth.ProjectAccessViaGroup)
	case visibility == auth.ProjectVisibilityCompany:
		facts.Via = string(auth.ProjectAccessViaCompany)
	}
	return auth.ProjectAccessOf(facts)
}

// previewOutcome is one target Project's people, before and after.
type previewOutcome struct {
	projectID uuid.UUID
	visible   bool
	people    []previewPersonOutcome
}

type previewPersonOutcome struct {
	userID        uuid.UUID
	before, after auth.ProjectAccess
	runs          int
}

// foldGroupPreview keeps the Projects where someone's access moves and the
// people whose access moves there. A Project the caller cannot see is left
// out with its people and Runs, and only marks the preview as hiding some.
// A restricted Project the change opens to the caller is listed in
// OpenedForCaller.
func foldGroupPreview(revision int64, caller uuid.UUID, outcomes []previewOutcome) GroupPreview {
	preview := GroupPreview{Revision: revision, Projects: []GroupPreviewProject{}, OpenedForCaller: []uuid.UUID{}}
	affected := map[uuid.UUID]bool{}
	for _, outcome := range outcomes {
		project := GroupPreviewProject{ProjectID: outcome.projectID, People: []GroupPreviewPerson{}}
		opened := false
		for _, person := range outcome.people {
			if slices.Equal(person.before.Permissions, person.after.Permissions) {
				continue
			}
			project.People = append(project.People, GroupPreviewPerson{
				UserID:       person.userID,
				Before:       person.before.Permissions,
				After:        person.after.Permissions,
				RunsStopping: person.runs,
			})
			opened = opened ||
				(person.userID == caller && person.before.Via == "" && person.after.Via != "")
		}
		if len(project.People) == 0 {
			continue
		}
		if !outcome.visible {
			preview.ProjectsHidden = true
			continue
		}
		for _, person := range project.People {
			affected[person.UserID] = true
			preview.RunsStopping += person.RunsStopping
		}
		if opened {
			preview.OpenedForCaller = append(preview.OpenedForCaller, outcome.projectID)
		}
		preview.Projects = append(preview.Projects, project)
	}
	preview.PeopleAffected = len(affected)
	return preview
}
