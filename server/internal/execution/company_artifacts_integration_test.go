//go:build integration

package execution

import (
	"slices"
	"testing"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"

	"github.com/google/uuid"
)

func resultIDs(results []ArtifactRunResult) []uuid.UUID {
	ids := make([]uuid.UUID, len(results))
	for i, result := range results {
		ids[i] = result.Artifact.ID
	}
	return ids
}

func TestCompanyArtifactListAndResolveNeverWidenAccess(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA, f.UserB)
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	member := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}

	seed := func(title, visibility string, owner uuid.UUID) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility,presentation_number, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,$3,'Summary',gen_random_uuid()::text,$4,$5,
				(SELECT COALESCE(max(presentation_number),0)+1 FROM outputs WHERE run_id=$2), origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`,
			f.CompanyA, f.RunA, title, owner, visibility).
			Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Newest last: one Artifact per source of access, plus one the member
	// cannot open, so each candidate branch of the list is exercised.
	private := seed("Salary benchmark", "private", f.UserA)
	company := seed("Q3 churn report", "company", f.UserA)
	granted := seed("Launch video_final", "shared", f.UserA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id)
		VALUES($1,$2,$3,'view',$4)`,
		f.CompanyA,
		granted,
		f.UserB,
		f.UserA,
	)
	channel := dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, f.UserB, nil)
	inChannel := seed("Board minutes", "shared", f.UserA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_conversation_grants(company_id,output_id,conversation_id,granted_by_user_id)
		VALUES($1,$2,$3,$4)`,
		f.CompanyA,
		inChannel,
		channel,
		f.UserA,
	)
	own := seed("Member draft", "private", f.UserB)

	list := func(p auth.UserPrincipal, search string, limit int32, before *Cursor) ArtifactPage {
		t.Helper()
		page, err := service.ListArtifacts(ctx, p, CompanyArtifactListOptions{
			ArtifactListOptions: ArtifactListOptions{Limit: limit, Before: before}, Search: search,
		})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	if got := resultIDs(
		list(owner, "", 50, nil).Items,
	); !slices.Equal(
		got,
		[]uuid.UUID{inChannel, granted, company, private},
	) {
		t.Fatalf("owner list = %v", got)
	}
	// RunA works in Task A's chat, in ProjectA, which is restricted: its
	// Company-visible Artifact reaches only the Project's readers (ADR 0017).
	if got := resultIDs(list(member, "", 50, nil).Items); !slices.Equal(got, []uuid.UUID{own, inChannel, granted}) {
		t.Fatalf("member list before reading the Project = %v", got)
	}
	masked, err := service.ResolveArtifacts(ctx, member, []uuid.UUID{granted})
	if err != nil || len(masked) != 1 || masked[0].Library == nil ||
		masked[0].Library.Source.Type != "restricted" ||
		masked[0].Library.Source.Title != "" ||
		masked[0].Library.CanShare {
		t.Fatalf("masked source = %+v, %v", masked, err)
	}
	filtered, err := service.ListArtifacts(ctx, member, CompanyArtifactListOptions{
		ArtifactListOptions: ArtifactListOptions{Limit: 1}, Ownership: "owned",
	})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Artifact.ID != own || filtered.Next != nil {
		t.Fatalf("owned page = %+v, %v", filtered, err)
	}
	filtered, err = service.ListArtifacts(ctx, member, CompanyArtifactListOptions{
		ArtifactListOptions: ArtifactListOptions{Limit: 1}, Source: "task",
	})
	if err != nil || len(filtered.Items) != 0 {
		t.Fatalf("source filter disclosed hidden Project = %+v, %v", filtered, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO project_grants(company_id,project_id,user_id,permission,granted_by_user_id)
		VALUES($1,$2,$3,'project.read',$4)`,
		f.CompanyA,
		f.ProjectA,
		f.UserB,
		f.UserA,
	)
	filtered, err = service.ListArtifacts(ctx, member, CompanyArtifactListOptions{
		ArtifactListOptions: ArtifactListOptions{Limit: 1}, Source: "task", Ownership: "shared",
	})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Artifact.ID != inChannel ||
		filtered.Next == nil ||
		filtered.Items[0].Library.Source.Type != "task" ||
		filtered.Items[0].Library.Source.Title == "" {
		t.Fatalf("shared task page = %+v, %v", filtered, err)
	}
	next, err := service.ListArtifacts(ctx, member, CompanyArtifactListOptions{
		ArtifactListOptions: ArtifactListOptions{
			Limit:  1,
			Before: filtered.Next,
		},
		Source:    "task",
		Ownership: "shared",
	})
	if err != nil || len(next.Items) != 1 || next.Items[0].Artifact.ID != granted {
		t.Fatalf("shared task next page = %+v, %v", next, err)
	}
	memberSees := []uuid.UUID{own, inChannel, granted, company}
	if got := resultIDs(list(member, "", 50, nil).Items); !slices.Equal(got, memberSees) {
		t.Fatalf("member list = %v", got)
	}
	// `_` is literal, not a wildcard.
	if got := resultIDs(list(member, "VIDEO_", 50, nil).Items); !slices.Equal(got, []uuid.UUID{granted}) {
		t.Fatalf("search = %v", got)
	}
	if got := resultIDs(list(member, "ch_r", 50, nil).Items); len(got) != 0 {
		t.Fatalf("underscore wildcard matched = %v", got)
	}
	// One-row pages walk the same order across every source of access.
	var paged []uuid.UUID
	var cursor *Cursor
	for range memberSees {
		page := list(member, "", 1, cursor)
		paged = append(paged, resultIDs(page.Items)...)
		if cursor = page.Next; cursor == nil {
			break
		}
	}
	if !slices.Equal(paged, memberSees) || cursor != nil {
		t.Fatalf("paged = %v, trailing cursor %v", paged, cursor)
	}

	resolved, err := service.ResolveArtifacts(ctx, member, []uuid.UUID{private, company, uuid.New(), granted})
	if err != nil {
		t.Fatal(err)
	}
	if got := resultIDs(resolved); !slices.Equal(got, []uuid.UUID{granted, company}) {
		t.Fatalf("member resolve = %v", got)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA, f.UserB)
	if resolved, err = service.ResolveArtifacts(
		ctx,
		member,
		[]uuid.UUID{company, granted},
	); err != nil ||
		len(resolved) != 0 {
		t.Fatalf("inactive member resolve = %v, %v", resultIDs(resolved), err)
	}
}

// Company-visible Artifacts from a restricted Project the viewer can't read
// never fill a page: the list still reaches the older ones the viewer may
// open (ADR 0017). Without the Project check in the list's Company-visible
// branch, a two-row page here came back empty and ended the list.
func TestCompanyArtifactListPagesPastHiddenProjectArtifacts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA, f.UserB)
	member := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}

	// A direct-chat Run belongs to no Project, so its Company-visible
	// Artifact is open to every member.
	var directRun uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO runs
			(company_id, agent_id, conversation_id, runtime_id, status, updated_at, run_type, revision)
		VALUES ($1, $2, $3, $4, 'completed', now(), 'direct_chat', 1) RETURNING id`,
		f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA).Scan(&directRun); err != nil {
		t.Fatal(err)
	}
	seed := func(runID uuid.UUID, title string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility,presentation_number, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,$3,'Summary',gen_random_uuid()::text,$4,'company',
				(SELECT COALESCE(max(presentation_number),0)+1 FROM outputs WHERE run_id=$2), origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`,
			f.CompanyA, runID, title, f.UserA).
			Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	readable := seed(directRun, "Company handbook")
	for _, title := range []string{"Payroll one", "Payroll two", "Payroll three"} {
		seed(f.RunA, title) // RunA works in restricted ProjectA
	}

	page, err := service.ListArtifacts(ctx, member, CompanyArtifactListOptions{
		ArtifactListOptions: ArtifactListOptions{Limit: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resultIDs(page.Items); !slices.Equal(got, []uuid.UUID{readable}) || page.Next != nil {
		t.Fatalf("member page = %v, next %v; want only %v", got, page.Next, readable)
	}
}
