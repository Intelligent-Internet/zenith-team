//go:build integration

package canvas

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestOutputReviewCollaborationWithoutProject(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	service := NewCanvasService(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	var run, id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type) VALUES($1,$2,$3,$4,'completed','direct_chat') RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA).
		Scan(&run); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Review','Summary','output-review',$3, origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, run, f.UserA).
		Scan(&id); err != nil {
		t.Fatal(err)
	}
	c := Command{RequestKey: uuid.New(), Type: "comment", Body: "Please check this."}
	first, err := service.MutateOutputReview(ctx, p, id, c)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.MutateOutputReview(ctx, p, id, c)
	if err != nil || replay != first {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	c.Body = "Different"
	if _, err = service.MutateOutputReview(ctx, p, id, c); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay = %v", err)
	}
	review, err := service.OutputReview(ctx, p, id)
	if err != nil || len(review.Items) != 1 || len(review.Items[0].Comments) != 1 {
		t.Fatalf("review = %+v, %v", review, err)
	}
	comment := review.Items[0].Comments[0]
	other := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	if _, err = service.OutputReview(ctx, other, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private review = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO output_grants(company_id,output_id,user_id,permission,granted_by_user_id) VALUES($1,$2,$3,'view',$4)`,
		f.CompanyA,
		id,
		f.UserB,
		f.UserA,
	)
	if _, err = service.MutateOutputReview(
		ctx,
		other,
		id,
		Command{RequestKey: uuid.New(), Type: "reply", ThreadID: &first.ID, Body: "Looks good."},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = service.MutateOutputReview(
		ctx,
		other,
		id,
		Command{
			RequestKey:       uuid.New(),
			Type:             "edit_comment",
			ThreadID:         &first.ID,
			ID:               &comment.ID,
			ExpectedRevision: 1,
			Body:             "Not mine",
		},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("edit another author = %v", err)
	}
	if _, err = service.MutateOutputReview(
		ctx,
		p,
		id,
		Command{
			RequestKey:       uuid.New(),
			Type:             "edit_comment",
			ThreadID:         &first.ID,
			ID:               &comment.ID,
			ExpectedRevision: 1,
			Body:             "Updated",
		},
	); err != nil {
		t.Fatal(err)
	}
	resolve := Command{
		RequestKey:       uuid.New(),
		Type:             "resolve",
		ThreadID:         &first.ID,
		ExpectedRevision: 1,
		Action:           "resolve",
	}
	if _, err = service.MutateOutputReview(ctx, other, id, resolve); err != nil {
		t.Fatal(err)
	}
	resolve.RequestKey = uuid.New()
	if _, err = service.MutateOutputReview(ctx, other, id, resolve); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale resolve = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM output_grants WHERE output_id=$1 AND user_id=$2`, id, f.UserB)
	if _, err = service.MutateOutputReview(
		ctx,
		other,
		id,
		Command{RequestKey: uuid.New(), Type: "reply", ThreadID: &first.ID, Body: "Revoked"},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("revoked reply = %v", err)
	}
	review, err = service.OutputReview(ctx, p, id)
	if err != nil || len(review.Items[0].Comments) != 2 || review.Items[0].Status != "resolved" ||
		review.Items[0].Comments[0].Body != "Updated" {
		t.Fatalf("saved review = %+v, %v", review, err)
	}
	foreign := p
	foreign.CompanyID = f.CompanyB
	if _, err = service.OutputReview(ctx, foreign, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-company = %v", err)
	}
}
