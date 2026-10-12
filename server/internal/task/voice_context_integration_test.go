//go:build integration

package task

import (
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

func TestVoiceTaskContextExcludesOtherCompaniesAndPrivateConversations(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	q := generated.New(pool)
	// The person who switched Company context on is the assistant's
	// Authorizer, and it reads with that person's access alone (D31).
	read := func(in voice.ContextRequest) (voice.ContextResult, error) {
		in.AuthorizerUserID = f.UserA
		return ReadVoiceContext(ctx, q, f.CompanyA, in)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET description=$2 WHERE id=$1`, f.TaskA, strings.Repeat("đ", 2100))
	got, err := read(voice.ContextRequest{Tool: "get_task", TaskID: f.TaskA})
	if err != nil || len(got.Items) != 1 || !got.Items[0].Truncated || len([]rune(got.Items[0].Description)) != 2000 {
		t.Fatalf("detail = %+v, %v", got, err)
	}
	for _, id := range []uuid.UUID{f.TaskB, uuid.New()} {
		got, err = read(voice.ContextRequest{Tool: "get_task", TaskID: id})
		if err != nil || len(got.Items) != 0 {
			t.Fatalf("foreign/missing = %+v, %v", got, err)
		}
	}
	got, err = read(voice.ContextRequest{Tool: "search_tasks", Query: "%"})
	if err != nil || len(got.Items) != 0 {
		t.Fatalf("wildcard treated as syntax = %+v, %v", got, err)
	}
	// With no Authorizer, or one who cannot read the Project, the Project's
	// Tasks are not read.
	got, err = ReadVoiceContext(ctx, q, f.CompanyA, voice.ContextRequest{Tool: "get_task", TaskID: f.TaskA})
	if err != nil || len(got.Items) != 0 {
		t.Fatalf("no Authorizer = %+v, %v", got, err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_grants WHERE company_id=$1 AND project_id=$2 AND user_id=$3`, f.CompanyA, f.ProjectA, f.UserA)
	for _, in := range []voice.ContextRequest{{Tool: "get_task", TaskID: f.TaskA}, {Tool: "search_tasks"}} {
		got, err = read(in)
		if err != nil || len(got.Items) != 0 {
			t.Fatalf("%s for an Authorizer without access = %+v, %v", in.Tool, got, err)
		}
	}
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserA)
	dbtest.MustExec(t, ctx, pool, `UPDATE conversations SET kind='direct',task_id=NULL,created_by_user_id=$2 WHERE id=$1`, f.ConversationA, f.UserA)
	got, err = read(voice.ContextRequest{Tool: "get_task", TaskID: f.TaskA})
	if err != nil || len(got.Items) != 0 {
		t.Fatalf("creator-private = %+v, %v", got, err)
	}
}
