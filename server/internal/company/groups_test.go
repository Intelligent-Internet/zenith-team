package company

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// No laundering: a membership change or a delete never moves
// access its author could not move directly on a Project the Group reaches.
func TestGroupChangePermittedIsNoLaundering(t *testing.T) {
	t.Parallel()
	read, grants := auth.ProjectPermissionRead, auth.ProjectPermissionGrantsManage
	approve := auth.ProjectPermissionApprovalsResolve
	set := func(permissions ...auth.ProjectPermission) []auth.ProjectPermission { return permissions }
	// Leo manages Payroll's access through a grant of {read, grants.manage}.
	leo := auth.ProjectAccess{
		Via:         auth.ProjectAccessViaGrant,
		Permissions: set(grants, read),
		Granted:     set(grants, read),
	}
	reader := auth.ProjectAccess{Via: auth.ProjectAccessViaGrant, Permissions: set(read), Granted: set(read)}
	admin := auth.ProjectAccess{Management: true, Permissions: auth.ManagementProjectPermissions()}
	for _, test := range []struct {
		name     string
		author   auth.ProjectAccess
		groupSet []auth.ProjectPermission
		want     bool
	}{
		{"an admin, through the management bridge", admin, auth.ProjectPermissions(), true},
		{"a grant manager whose own grant covers the Group's set", leo, set(read), true},
		{"a grant manager whose own grant is smaller than the Group's set", leo, set(approve, read), false},
		{"a reader who cannot manage access", reader, set(read), false},
		{"someone who cannot see the Project", auth.ProjectAccess{}, set(read), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := groupChangePermitted(test.author, test.groupSet); got != test.want {
				t.Fatalf("groupChangePermitted() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestGroupNamesAreTrimmedAndBounded(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, groupName, description string
		wantName                     string
		wantErr                      bool
	}{
		{name: "trimmed", groupName: "  Finance \n", description: " Payroll ", wantName: "Finance"},
		{name: "80 characters", groupName: strings.Repeat("é", 80), wantName: strings.Repeat("é", 80)},
		{name: "81 characters", groupName: strings.Repeat("a", 81), wantErr: true},
		{name: "only spaces", groupName: "   ", wantErr: true},
		{name: "a 500-character description", groupName: "Finance", description: strings.Repeat("d", 500), wantName: "Finance"},
		{name: "a 501-character description", groupName: "Finance", description: strings.Repeat("d", 501), wantErr: true},
		{name: "not UTF-8", groupName: "Fin\xffance", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			name, _, err := normalizeGroupText(test.groupName, test.description)
			if test.wantErr {
				if !errors.Is(err, ErrGroupInvalidInput) {
					t.Fatalf("err = %v, want ErrGroupInvalidInput", err)
				}
				return
			}
			if err != nil || name != test.wantName {
				t.Fatalf("name = %q, err = %v; want %q", name, err, test.wantName)
			}
		})
	}
}

func TestGroupMemberChangeIsSortedAndBounded(t *testing.T) {
	t.Parallel()
	a, b := uuid.MustParse(
		"00000000-0000-0000-0000-00000000000a",
	), uuid.MustParse(
		"00000000-0000-0000-0000-00000000000b",
	)
	add, remove, err := normalizeGroupMemberChange([]uuid.UUID{b, a, b}, nil)
	if err != nil || !slices.Equal(add, []uuid.UUID{a, b}) || len(remove) != 0 {
		t.Fatalf("add = %v, remove = %v, err = %v", add, remove, err)
	}
	for name, change := range map[string][2][]uuid.UUID{
		"added and removed at once": {{a}, {a}},
		"the nil id":                {{uuid.Nil}, nil},
		"more than the cap": {
			make([]uuid.UUID, 0), func() []uuid.UUID {
				ids := make([]uuid.UUID, maxGroupChangeIDs+1)
				for i := range ids {
					ids[i] = uuid.New()
				}
				return ids
			}(),
		},
	} {
		if _, _, err := normalizeGroupMemberChange(
			change[0],
			change[1],
		); !errors.Is(
			err,
			ErrGroupInvalidInput,
		) {
			t.Errorf("%s: err = %v, want ErrGroupInvalidInput", name, err)
		}
	}
}

// The preview states access with the rule the views apply, from a person's
// stored paths: access adds up, so leaving a Group keeps an own grant.
func TestSimulatedAccessAddsUpEverySource(t *testing.T) {
	t.Parallel()
	read, write, approve := auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite, auth.ProjectPermissionApprovalsResolve
	finance, design := uuid.New(), uuid.New()
	own := func(permission auth.ProjectPermission) accessPath {
		return accessPath{via: auth.ProjectAccessViaGrant, permission: permission}
	}
	group := func(id uuid.UUID, permission auth.ProjectPermission) accessPath {
		return accessPath{via: auth.ProjectAccessViaGroup, groupID: id, permission: permission}
	}
	restricted, open := auth.ProjectVisibilityRestricted, auth.ProjectVisibilityCompany
	for _, test := range []struct {
		name       string
		visibility auth.ProjectVisibility
		paths      []accessPath
		dropped    uuid.UUID
		added      []auth.ProjectPermission
		want       []auth.ProjectPermission
		via        auth.ProjectAccessVia
	}{
		{
			"own and Finance", restricted,
			[]accessPath{own(read), own(approve), group(finance, read), group(finance, write)},
			uuid.Nil, nil,
			[]auth.ProjectPermission{approve, read, write},
			auth.ProjectAccessViaGrant,
		},
		{
			"leaving Finance keeps the own grant", restricted,
			[]accessPath{own(read), own(approve), group(finance, read)},
			finance, nil,
			[]auth.ProjectPermission{approve, read},
			auth.ProjectAccessViaGrant,
		},
		{
			"Finance was the only source", restricted,
			[]accessPath{group(finance, read), group(finance, write)},
			finance, nil,
			[]auth.ProjectPermission{},
			"",
		},
		{
			"another Group still gives read", restricted,
			[]accessPath{group(finance, read), group(design, read)},
			finance, nil,
			[]auth.ProjectPermission{read},
			auth.ProjectAccessViaGroup,
		},
		{
			"joining Finance", restricted, nil, finance,
			[]auth.ProjectPermission{read, write},
			[]auth.ProjectPermission{read, write},
			auth.ProjectAccessViaGroup,
		},
		{
			"the Company floor stays", open,
			[]accessPath{group(finance, read), group(finance, approve)},
			finance, nil,
			auth.CompanyProjectDefaultPermissions(), auth.ProjectAccessViaCompany,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := simulatedAccess(test.visibility, test.paths, test.dropped, test.added)
			if !slices.Equal(got.Permissions, test.want) || got.Via != test.via || got.Management {
				t.Fatalf("access = %+v, want %v via %q", got, test.want, test.via)
			}
		})
	}
}

// The preview keeps the Projects and people whose access moves, counts them
// and their Runs, leaves out what the caller cannot see, and names the
// restricted Projects the change opens to the caller.
func TestFoldGroupPreviewCountsWhatMoves(t *testing.T) {
	t.Parallel()
	caller, mia, leo := uuid.New(), uuid.New(), uuid.New()
	payroll, website, hidden, quiet := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	readers := auth.ProjectAccess{
		Via:         auth.ProjectAccessViaGroup,
		Permissions: []auth.ProjectPermission{auth.ProjectPermissionRead},
	}
	outsider := auth.ProjectAccess{Permissions: []auth.ProjectPermission{}}
	outcomes := []previewOutcome{
		{projectID: payroll, visible: true, people: []previewPersonOutcome{
			{userID: caller, before: outsider, after: readers},
			{userID: mia, before: outsider, after: readers},
			{userID: leo, before: readers, after: outsider, runs: 1},
		}},
		{projectID: website, visible: true, people: []previewPersonOutcome{
			{userID: mia, before: outsider, after: readers},
			{userID: leo, before: readers, after: readers},
		}},
		{projectID: hidden, visible: false, people: []previewPersonOutcome{
			{userID: caller, before: outsider, after: readers},
			{userID: leo, before: readers, after: outsider, runs: 4},
		}},
		{
			projectID: quiet,
			visible:   true,
			people:    []previewPersonOutcome{{userID: leo, before: readers, after: readers}},
		},
	}
	preview := foldGroupPreview(9, caller, outcomes)
	if preview.Revision != 9 || len(preview.Projects) != 2 || preview.Projects[0].ProjectID != payroll ||
		preview.Projects[1].ProjectID != website {
		t.Fatalf("projects = %+v", preview.Projects)
	}
	if len(preview.Projects[0].People) != 3 || len(preview.Projects[1].People) != 1 {
		t.Fatalf("people = %+v", preview.Projects)
	}
	if preview.PeopleAffected != 3 || preview.RunsStopping != 1 || !preview.ProjectsHidden {
		t.Fatalf("summary = %+v", preview)
	}
	if !slices.Equal(preview.OpenedForCaller, []uuid.UUID{payroll}) {
		t.Fatalf("openedForCaller = %v, want only Payroll", preview.OpenedForCaller)
	}
	empty := foldGroupPreview(1, caller, nil)
	if empty.Projects == nil || empty.OpenedForCaller == nil || empty.PeopleAffected != 0 || empty.ProjectsHidden {
		t.Fatalf("empty preview = %+v", empty)
	}
}

// Each listed person's sources, own grant first, then each Group, the
// Company floor and the management bridge; granted stays their own rows.
func TestFoldProjectGrantsStatesEverySource(t *testing.T) {
	t.Parallel()
	mia, leo, olivia := uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse(
			"00000000-0000-0000-0000-000000000002",
		), uuid.MustParse("00000000-0000-0000-0000-000000000003")
	finance := uuid.New()
	at := func(minute int) pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: time.Date(2026, 10, 6, 9, minute, 0, 0, time.UTC), Valid: true}
	}
	author := uuid.New()
	rows := []generated.ListProjectAccessPathsRow{
		{
			UserID:          mia,
			Via:             "grant",
			Permissions:     []string{"project.approvals.resolve", "project.read"},
			UpdatedAt:       at(1),
			GrantedByUserID: uuid.NullUUID{UUID: author, Valid: true},
		},
		{
			UserID:      mia,
			Via:         "group",
			GroupID:     finance,
			Permissions: []string{"project.read", "project.tasks.write"},
			UpdatedAt:   at(5),
		},
		{UserID: leo, Via: "group", GroupID: finance, Permissions: []string{"project.read"}, UpdatedAt: at(2)},
		{
			UserID:      olivia,
			Via:         "grant",
			Permissions: []string{"project.read"},
			UpdatedAt:   at(3),
			Management:  true,
		},
	}
	grants := foldProjectGrants(rows, auth.ProjectVisibilityRestricted)
	if len(grants) != 3 {
		t.Fatalf("grants = %+v", grants)
	}
	if got := grants[0]; got.UserID != mia || !slices.Equal(auth.ProjectPermissionStrings(got.Granted),
		[]string{"project.approvals.resolve", "project.read"}) ||
		!slices.Equal(auth.ProjectPermissionStrings(got.Effective),
			[]string{"project.approvals.resolve", "project.read", "project.tasks.write"}) ||
		got.GrantedBy == nil || *got.GrantedBy != author || !got.UpdatedAt.Equal(at(5).Time) ||
		len(got.Sources) != 2 || got.Sources[0].Via != ProjectGrantSourceGrant ||
		got.Sources[1].Via != ProjectGrantSourceGroup || *got.Sources[1].GroupID != finance {
		t.Fatalf("Mia = %+v", got)
	}
	if got := grants[1]; got.UserID != leo || len(got.Granted) != 0 || got.GrantedBy != nil ||
		!slices.Equal(auth.ProjectPermissionStrings(got.Effective), []string{"project.read"}) ||
		len(got.Sources) != 1 || got.Sources[0].Via != ProjectGrantSourceGroup {
		t.Fatalf("Leo, through Finance only = %+v", got)
	}
	if got := grants[2]; got.UserID != olivia || len(got.Sources) != 2 ||
		got.Sources[1].Via != ProjectGrantSourceManagement || !slices.Contains(got.Effective, auth.ProjectPermissionGrantsManage) {
		t.Fatalf("Olivia, an admin = %+v", got)
	}
	open := foldProjectGrants(rows[2:3], auth.ProjectVisibilityCompany)
	if len(open[0].Sources) != 2 || open[0].Sources[1].Via != ProjectGrantSourceCompany ||
		!slices.Contains(open[0].Effective, auth.ProjectPermissionTasksExecute) {
		t.Fatalf("Leo on a Project open to the Company = %+v", open[0])
	}
	if empty := foldProjectGrants(nil, auth.ProjectVisibilityRestricted); empty == nil || len(empty) != 0 {
		t.Fatalf("no grants = %#v", empty)
	}
}

// The Group frames are recorded in the transaction (the evidence sink must
// not copy them) and carry the Group and its revision only.
func TestGroupFramesAreRecordedAndNameOnlyTheGroup(t *testing.T) {
	t.Parallel()
	companyID, groupID := uuid.New(), uuid.New()
	changed := groupChangedEvent(companyID, events.SystemActor(), groupID, 3)
	deleted := groupDeletedEvent(companyID, events.SystemActor(), groupID)
	if changed.EntityType != TopicGroup || changed.Name != EventGroupChanged || !changed.ActivityRecorded ||
		changed.Payload != (GroupChanged{GroupID: groupID, Revision: 3}) {
		t.Fatalf("group.changed = %+v", changed)
	}
	if deleted.EntityType != TopicGroup || deleted.Name != EventGroupDeleted || !deleted.ActivityRecorded ||
		deleted.Payload != (GroupDeleted{GroupID: groupID}) {
		t.Fatalf("group.deleted = %+v", deleted)
	}
}

func TestStoredProjectSetLeavesOutTheFloorAndTheBridge(t *testing.T) {
	t.Parallel()
	access := auth.ProjectAccessOf(auth.ProjectAccessFacts{
		Visibility: string(
			auth.ProjectVisibilityCompany,
		),
		Via:                string(auth.ProjectAccessViaGrant),
		Management:         true,
		GrantedPermissions: []string{"project.read", "project.approvals.resolve"},
		GroupPermissions:   []string{"project.read", "project.files.manage"},
	})
	if got := storedProjectSet(access); !slices.Equal(got,
		[]string{"project.approvals.resolve", "project.files.manage", "project.read"}) {
		t.Fatalf("stored set = %v", got)
	}
}

// The trail names, per Project, only the people whose stored set there
// moved, sorted; a person whose own grant already held what the Group gives
// is left out.
func TestStoredSetMovedNamesOnlyThePeopleWhoseSetMoved(t *testing.T) {
	t.Parallel()
	held := func(granted, group []string) auth.ProjectAccess {
		return auth.ProjectAccessOf(auth.ProjectAccessFacts{
			Visibility: string(auth.ProjectVisibilityRestricted), Via: string(auth.ProjectAccessViaGrant),
			GrantedPermissions: granted, GroupPermissions: group,
		})
	}
	none := auth.ProjectAccessOf(auth.ProjectAccessFacts{Visibility: string(auth.ProjectVisibilityRestricted)})
	view := []string{"project.read"}
	work := []string{"project.read", "project.tasks.execute", "project.tasks.write"}
	mia, leo, ana := uuid.New(), uuid.New(), uuid.New()
	project := groupProjectChange{
		before: map[uuid.UUID]auth.ProjectAccess{
			mia: none, leo: held(work, nil), ana: held(view, nil),
		},
		after: map[uuid.UUID]auth.ProjectAccess{
			mia: held(nil, view), leo: held(work, view), ana: held(view, work),
		},
	}
	got, want := project.storedSetMoved([]uuid.UUID{mia, leo, ana}), sortedUserIDs([]uuid.UUID{mia, ana})
	if !slices.Equal(got, want) {
		t.Fatalf("moved = %v, want %v", got, want)
	}
	if got := project.storedSetMoved([]uuid.UUID{leo}); got == nil || len(got) != 0 {
		t.Fatalf("moved for Leo alone = %v, want an empty list", got)
	}
}

// One person opening a Project is named and not told; several are counted.
func TestOpenedNoticeTitleNamesOneAndCountsSeveral(t *testing.T) {
	t.Parallel()
	mia, leo := uuid.New(), uuid.New()
	names := map[uuid.UUID]string{mia: "Mia", leo: "Leo"}
	title, told := openedNoticeTitle([]uuid.UUID{mia}, names, "Payroll")
	if title != "Mia can now see Payroll" || told != mia {
		t.Fatalf("one person = %q, %v", title, told)
	}
	title, told = openedNoticeTitle([]uuid.UUID{mia, leo}, names, "Payroll")
	if title != "2 people can now see Payroll" || told != uuid.Nil {
		t.Fatalf("two people = %q, %v", title, told)
	}
}
