//go:build integration

package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Whether a Task can start is read live off its assignee's setup of the
// executing Agent and the computer it names (ADR 0014), so a change there
// moves no Task row. The agent frames that record the change reach that
// member alone. Every reader of the Task's Project learns it from a
// task.updated for each open Task that member is assigned and a changed Agent
// executes or assists (ADR 0017). The frame keeps the Task's revision and
// names no computer.
func TestAssigneeReadinessChangesReachTheProjectReaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(context.Context, readinessEnv) error
		want   []string
		reason string
	}{
		{
			name: "the assignee takes the agent off their computer",
			change: func(ctx context.Context, env readinessEnv) error {
				bindings, err := agent.NewBindingService(agent.BindingServiceOptions{Store: env.store, Bus: env.bus})
				if err != nil {
					return err
				}
				return bindings.Unbind(ctx, env.owner, env.fixture.AgentA)
			},
			want:   []string{"task_a", "task_own_work"},
			reason: "not_bound",
		},
		{
			name: "the assignee's computer goes offline",
			change: func(ctx context.Context, env readinessEnv) error {
				runtimes, err := agent.NewRuntimeService(agent.RuntimeServiceOptions{
					Store: env.store, Bus: env.bus,
					ModelDiscoveries: agent.NewInMemoryModelDiscoveryStore(agent.ModelDiscoveryStoreOptions{}),
				})
				if err != nil {
					return err
				}
				return runtimes.DeregisterRuntimes(ctx, env.owner, agent.RuntimeDeregistration{
					DaemonID: env.fixture.DaemonA, RuntimeIDs: []string{env.fixture.RuntimeA.String()},
				})
			},
			want:   []string{"task_a", "task_other_agent", "task_own_work"},
			reason: "runtime_offline",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			env := newReadinessEnv(t, ctx)

			if err := tc.change(ctx, env); err != nil {
				t.Fatalf("change error = %v", err)
			}

			// The Tasks are in a restricted Project, so each frame reaches its
			// readers one by one and never the Company room or the member
			// who does not read it (ADR 0017).
			readers := map[string]bool{}
			ids, err := env.store.Queries().ListProjectReaderIDs(ctx, generated.ListProjectReaderIDsParams{
				CompanyID: env.fixture.CompanyA, ProjectID: env.fixture.ProjectA,
			})
			if err != nil {
				t.Fatalf("read the Project's readers: %v", err)
			}
			for _, reader := range ids {
				readers["company_user/"+env.fixture.CompanyA.String()+"/"+reader.String()] = true
			}
			if len(readers) == 0 ||
				readers["company_user/"+env.fixture.CompanyA.String()+"/"+env.fixture.UserB.String()] {
				t.Fatalf("readers = %v; want the owner and not the member", readers)
			}
			var got []string
			for index, frame := range env.recorder.frames {
				if frame.Type != task.EventUpdated {
					continue
				}
				if !readers[env.recorder.sent[index]] {
					t.Fatalf("task.updated went to %s, want only the Project's readers %v",
						env.recorder.sent[index], readers)
				}
				raw, err := json.Marshal(frame)
				if err != nil {
					t.Fatalf("encode frame: %v", err)
				}
				for _, machine := range []string{env.fixture.RuntimeA.String(), env.fixture.DaemonA, "Runtime A"} {
					if strings.Contains(string(raw), machine) {
						t.Fatalf("frame names the computer (%s): %s", machine, raw)
					}
				}
				value, ok := frame.Payload.(task.Response)
				if !ok {
					t.Fatalf("task.updated payload = %T, want task.Response", frame.Payload)
				}
				if value.Execution.AssigneeReady || value.Execution.UnreadyReason == nil ||
					*value.Execution.UnreadyReason != tc.reason {
					t.Fatalf(
						"%s execution = %+v, want not ready: %s",
						value.Key,
						value.Execution,
						tc.reason,
					)
				}
				if value.Revision != 1 {
					t.Fatalf(
						"%s revision = %d, want 1: no Task row moved",
						value.Key,
						value.Revision,
					)
				}
				if !slices.Contains(got, value.Key) {
					got = append(got, value.Key)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("task.updated for %v, want %v", got, tc.want)
			}
		})
	}
}

type readinessEnv struct {
	store    *db.Store
	bus      *events.Bus
	recorder *recordingBroadcaster
	fixture  dbtest.Fixture
	owner    auth.UserPrincipal
}

// newReadinessEnv is the fixture Company with the browser routing and the
// readiness listener on one bus. Its owner is assigned task_a, executed by
// Agent A, which they set up on Runtime A. That Agent also assists their
// human task_own_work. Agent C executes task_other_agent on Runtime A, so
// only a change of that computer reaches it. Other members' and completed
// Tasks do not receive either change.
func newReadinessEnv(t *testing.T, ctx context.Context) readinessEnv {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		f.CompanyA, f.UserB)
	agentC := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, ctx, pool, agentC, f.CompanyA, "Agent C", "codex", &f.AgentA, f.UserA, f.RuntimeA)
	for number, seed := range []struct {
		key      string
		status   string
		assignee any
		agent    any
	}{
		{key: "task_done", status: "done", assignee: f.UserA, agent: f.AgentA},
		{key: "task_other_agent", status: "todo", assignee: f.UserA, agent: agentC},
		{key: "task_other_member", status: "todo", assignee: f.UserB, agent: f.AgentA},
		{key: "task_own_work", status: "todo", assignee: f.UserA, agent: nil},
	} {
		taskID, conversationID := dbtest.NewUUID(t), dbtest.NewUUID(t)
		dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
			dbtest.MustExecTx(t, ctx, tx, `
				INSERT INTO conversations (id, company_id, kind, task_id, project_id, title)
				VALUES ($1, $2, 'task', $3, $4, $5)`,
				conversationID, f.CompanyA, taskID, f.ProjectA, seed.key)
			dbtest.MustExecTx(t, ctx, tx, `
				INSERT INTO tasks
					(id, company_id, conversation_id, project_id, milestone_type,
					 number, key, title, status, assignee_user_id,
					 executed_by_agent_id, creator_user_id)
				VALUES ($1, $2, $3, $4, 'product', $5, $6, $6, $7, $8, $9, $10)`,
				taskID, f.CompanyA, conversationID, f.ProjectA, number+2, seed.key,
				seed.status, seed.assignee, seed.agent, f.UserA)
		})
	}

	bus, recorder := routedBus(t)
	if err := registerTaskReadiness(bus, generated.New(pool)); err != nil {
		t.Fatalf("registerTaskReadiness() error = %v", err)
	}
	return readinessEnv{
		store:    store,
		bus:      bus,
		recorder: recorder,
		fixture:  f,
		owner:    auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA, Role: auth.CompanyRoleOwner},
	}
}
