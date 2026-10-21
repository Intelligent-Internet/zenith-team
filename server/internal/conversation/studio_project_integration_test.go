//go:build integration

package conversation

import (
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// studioTestOrigin is the Studio API origin a server would configure; the
// daemon sends it with every Studio project request.
const studioTestOrigin = "https://studio.example"

func TestStudioProjectIsBoundToConversationAcrossRuns(t *testing.T) {
	t.Parallel()
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
	svc := NewStudioProjectService(store)
	principal := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA}
	seed := func(conversationID uuid.UUID) (uuid.UUID, string) {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,context,run_type,studio_enabled) VALUES($1,$2,$3,$4,'running','{}','direct_chat',true) RETURNING id`, f.CompanyA, f.AgentA, conversationID, f.RuntimeA).
			Scan(&id); err != nil {
			t.Fatal(err)
		}
		token := uuid.NewString()
		hash := sha256.Sum256([]byte(token))
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO run_tokens(run_id,token_hash,expires_at) VALUES($1,$2,now()+interval '1 hour')`,
			id,
			hash[:],
		)
		return id, token
	}
	runID, token := seed(f.DirectConversationA)
	request := protocol.StudioProjectRequest{
		APIOrigin: studioTestOrigin,
		Action:    "reserve",
		Title:     "New campaign",
		RunToken:  token,
	}
	access := StudioProjectAccess{RunID: runID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonA}
	var wg sync.WaitGroup
	results := make(chan protocol.StudioProject, 2)
	for range 2 {
		wg.Go(func() {
			value, err := svc.Resolve(ctx, principal, access, request)
			if err != nil {
				t.Error(err)
			}
			results <- value
		})
	}
	wg.Wait()
	close(results)
	var binding protocol.StudioProject
	creators := 0
	for value := range results {
		if value.CanCreate {
			creators++
		}
		binding = value
	}
	if creators != 1 || binding.ID == "" || binding.ProjectID != "" {
		t.Fatalf("reservation = %+v, creators = %d", binding, creators)
	}
	projectID := uuid.NewString()
	bound, err := svc.Resolve(
		ctx,
		principal,
		access,
		protocol.StudioProjectRequest{
			APIOrigin: studioTestOrigin,
			Action:    "bind",
			RunToken:  token,
			BindingID: binding.ID,
			ProjectID: projectID,
		},
	)
	if err != nil || bound.ProjectID != projectID {
		t.Fatalf("bind = %+v, %v", bound, err)
	}
	if _, err = svc.Resolve(
		ctx,
		principal,
		access,
		protocol.StudioProjectRequest{
			APIOrigin: studioTestOrigin,
			Action:    "bind",
			RunToken:  token,
			BindingID: binding.ID,
			ProjectID: uuid.NewString(),
		},
	); !errors.Is(
		err,
		ErrStudioProjectConflict,
	) {
		t.Fatalf("replace binding = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, runID)
	nextID, nextToken := seed(f.DirectConversationA)
	next, err := svc.Resolve(
		ctx,
		principal,
		StudioProjectAccess{RunID: nextID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonA},
		protocol.StudioProjectRequest{
			APIOrigin: studioTestOrigin,
			Action:    "reserve",
			Title:     "Different title",
			RunToken:  nextToken,
		},
	)
	if err != nil || next.ProjectID != projectID || next.CanCreate {
		t.Fatalf("continuation = %+v, %v", next, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE conversation_studio_projects SET api_origin='http://localhost:8000' WHERE id=$1`,
		binding.ID,
	)
	local, err := svc.Resolve(
		ctx,
		principal,
		StudioProjectAccess{RunID: nextID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonA},
		protocol.StudioProjectRequest{APIOrigin: studioTestOrigin, Action: "get", RunToken: nextToken},
	)
	if err != nil || local.ProjectID != projectID {
		t.Fatalf("local legacy origin = %+v, %v", local, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE conversation_studio_projects SET api_origin='https://studio.example' WHERE id=$1`,
		binding.ID,
	)
	var otherConversation uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO conversations(company_id,agent_id,created_by_user_id,kind,request_key) VALUES($1,$2,$3,'direct',gen_random_uuid()) RETURNING id`, f.CompanyA, f.AgentA, f.UserA).
		Scan(&otherConversation); err != nil {
		t.Fatal(err)
	}
	otherID, otherToken := seed(otherConversation)
	other, err := svc.Resolve(
		ctx,
		principal,
		StudioProjectAccess{RunID: otherID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonA},
		protocol.StudioProjectRequest{
			APIOrigin: studioTestOrigin,
			Action:    "reserve",
			Title:     "New campaign",
			RunToken:  otherToken,
		},
	)
	if err != nil || !other.CanCreate || other.ID == binding.ID || other.ProjectID != "" {
		t.Fatalf("new conversation = %+v, %v", other, err)
	}
	if _, err := svc.Resolve(
		ctx,
		principal,
		StudioProjectAccess{RunID: otherID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonA},
		protocol.StudioProjectRequest{
			APIOrigin: studioTestOrigin,
			Action:    "bind",
			RunToken:  otherToken,
			BindingID: other.ID,
			ProjectID: projectID,
		},
	); !errors.Is(err, ErrStudioProjectConflict) {
		t.Fatalf("another chat adopted the existing project: %v", err)
	}
	for _, test := range []struct {
		name   string
		actor  auth.UserPrincipal
		access StudioProjectAccess
		token  string
	}{
		{"expired run", principal, access, token},
		{"wrong proof", principal, StudioProjectAccess{RunID: nextID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonA}, otherToken},
		{"wrong daemon", principal, StudioProjectAccess{RunID: nextID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonB}, nextToken},
		{"wrong user", auth.UserPrincipal{UserID: f.UserB, CompanyID: f.CompanyA}, StudioProjectAccess{RunID: nextID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonA}, nextToken},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.Resolve(
				ctx,
				test.actor,
				test.access,
				protocol.StudioProjectRequest{
					APIOrigin: studioTestOrigin,
					Action:    "get",
					RunToken:  test.token,
				},
			)
			if !errors.Is(err, ErrStudioProjectNotFound) {
				t.Fatalf("access = %v", err)
			}
		})
	}
	// Changing current Agent settings does not revoke the Run's frozen selection.
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET settings='{}' WHERE id=$1`, f.AgentA)
	if _, err := svc.Resolve(
		ctx,
		principal,
		StudioProjectAccess{RunID: nextID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonA},
		protocol.StudioProjectRequest{APIOrigin: studioTestOrigin, Action: "get", RunToken: nextToken},
	); err != nil {
		t.Fatalf("frozen Studio selection: %v", err)
	}
	// The Studio lock reads the Run's frozen Studio access from its column.
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET studio_enabled=false WHERE id=$1`, nextID)
	if _, err := svc.Resolve(
		ctx,
		principal,
		StudioProjectAccess{RunID: nextID, RuntimeID: f.RuntimeA, DaemonID: f.DaemonA},
		protocol.StudioProjectRequest{APIOrigin: studioTestOrigin, Action: "get", RunToken: nextToken},
	); !errors.Is(
		err,
		ErrStudioProjectNotFound,
	) {
		t.Fatalf("disabled Studio accepted: %v", err)
	}
}

func TestStudioProjectForChannelAgentInvocation(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_build_object('capabilities',jsonb_build_object('enabledTools',jsonb_build_array('zenith-studio'))) WHERE id=$1`,
		f.seed.AgentA,
	)
	message := f.send(t, taggedSend(t, f.seed.AgentA))
	invocation := f.invocation(t, message.ID)
	run := f.run(t, invocation.ID)
	if !run.StudioEnabled || run.RunType != "channel_agent" {
		t.Fatalf("Studio enabled = %v, run type = %s, want true/channel_agent", run.StudioEnabled, run.RunType)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runs SET status='running' WHERE id=$1`, run.ID)
	token := uuid.NewString()
	hash := sha256.Sum256([]byte(token))
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO run_tokens(run_id,token_hash,expires_at) VALUES($1,$2,now()+interval '1 hour')`,
		run.ID,
		hash[:],
	)
	svc := NewStudioProjectService(f.store)
	access := StudioProjectAccess{RunID: run.ID, RuntimeID: f.seed.RuntimeA, DaemonID: f.seed.DaemonA}
	for _, origin := range []string{"https://studio-dev.example", "http://localhost:8000"} {
		project, err := svc.Resolve(
			f.ctx,
			f.a,
			access,
			protocol.StudioProjectRequest{Action: "get", RunToken: token, APIOrigin: origin},
		)
		if err != nil || project.ProjectID != "" {
			t.Fatalf("initial project at %s = %+v, %v, want empty project", origin, project, err)
		}
	}
	reserved, err := svc.Resolve(
		f.ctx,
		f.a,
		access,
		protocol.StudioProjectRequest{
			Action:    "reserve",
			Title:     "Channel artwork",
			RunToken:  token,
			APIOrigin: "http://localhost:8000",
		},
	)
	if err != nil || !reserved.CanCreate {
		t.Fatalf("reserve = %+v, %v, want creation allowed", reserved, err)
	}
	projectID := uuid.NewString()
	bound, err := svc.Resolve(
		f.ctx,
		f.a,
		access,
		protocol.StudioProjectRequest{
			Action:    "bind",
			BindingID: reserved.ID,
			ProjectID: projectID,
			RunToken:  token,
			APIOrigin: "http://localhost:8000",
		},
	)
	if err != nil || bound.ProjectID != projectID {
		t.Fatalf("bind = %+v, %v, want project %s", bound, err, projectID)
	}
	listed, err := svc.Resolve(
		f.ctx,
		f.a,
		access,
		protocol.StudioProjectRequest{Action: "get", RunToken: token, APIOrigin: "http://localhost:8000"},
	)
	if err != nil || listed.ProjectID != projectID {
		t.Fatalf("get = %+v, %v, want project %s", listed, err, projectID)
	}
	if _, err := svc.Resolve(
		f.ctx,
		f.b,
		access,
		protocol.StudioProjectRequest{Action: "get", RunToken: token, APIOrigin: "http://localhost:8000"},
	); !errors.Is(
		err,
		ErrStudioProjectNotFound,
	) {
		t.Fatalf("other member access = %v, want ErrStudioProjectNotFound", err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE runs SET status='completed' WHERE id=$1`, run.ID)
	if _, err := svc.Resolve(
		f.ctx,
		f.a,
		access,
		protocol.StudioProjectRequest{Action: "get", RunToken: token, APIOrigin: "http://localhost:8000"},
	); !errors.Is(
		err,
		ErrStudioProjectNotFound,
	) {
		t.Fatalf("completed Run access = %v, want ErrStudioProjectNotFound", err)
	}
}
