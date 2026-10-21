//go:build integration

package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestHumanInputResolutionsRecheckMembershipAfterCompanyWait(t *testing.T) {
	for _, requestType := range []string{"Approval", "Question"} {
		t.Run(requestType, func(t *testing.T) {
			ctx, pool, store, bus, fixture := newHumanInputAuthorizationFixture(t)
			principal := auth.UserPrincipal{
				UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
			}
			origin := seedDirectRunningOrigin(t, ctx, pool, fixture)

			var resolve func() error
			var notFound error
			var table string
			var entityID uuid.UUID
			switch requestType {
			case "Approval":
				service, err := NewApprovalService(store, bus, &planActionParticipantStub{}, nil)
				if err != nil {
					t.Fatalf("NewApprovalService() error = %v", err)
				}
				value, _, err := service.RequestCompanyPlanApply(
					ctx,
					principal,
					origin,
					uuid.Must(uuid.NewV7()),
					json.RawMessage(`{"artifactVersion":1}`),
				)
				if err != nil {
					t.Fatalf("RequestCompanyPlanApply() error = %v", err)
				}
				resolve = func() error {
					_, resolveErr := service.Approve(
						ctx,
						principal,
						value.ID,
						ApprovalDecisionInput{
							ExpectedRevision: value.Revision,
						},
					)
					return resolveErr
				}
				notFound = ErrApprovalNotFound
				table = "approvals"
				entityID = value.ID
			case "Question":
				service, err := NewQuestionService(store, bus, nil)
				if err != nil {
					t.Fatalf("NewQuestionService() error = %v", err)
				}
				value, _, err := service.Ask(
					ctx,
					principal,
					origin,
					uuid.Must(uuid.NewV7()),
					testQuestionAskInput(),
				)
				if err != nil {
					t.Fatalf("Ask() error = %v", err)
				}
				resolve = func() error {
					_, resolveErr := service.Answer(ctx, principal, value.ID, QuestionAnswerInput{
						ExpectedRevision: value.Revision,
						Answers: []QuestionAnswer{{
							PromptID: "target", OptionIDs: []string{"a"},
						}},
					})
					return resolveErr
				}
				notFound = ErrQuestionNotFound
				table = "questions"
				entityID = value.ID
			default:
				t.Fatalf("unknown request type %q", requestType)
			}

			err := revokeMembershipAheadOfHumanInputMutation(
				t,
				ctx,
				pool,
				fixture.CompanyA,
				fixture.UserA,
				resolve,
			)
			if !errors.Is(err, notFound) {
				t.Fatalf("resolve after membership revocation error = %v, want %v", err, notFound)
			}
			var status string
			if err := pool.QueryRow(
				ctx,
				fmt.Sprintf(
					"SELECT status FROM %s WHERE id=$1",
					table,
				), //nolint:gosec // fixed test table names.
				entityID,
			).Scan(&status); err != nil {
				t.Fatalf("read unresolved %s: %v", requestType, err)
			}
			if status != "pending" {
				t.Fatalf("%s status = %q, want pending", requestType, status)
			}
		})
	}
}

func TestHumanInputRaisesRecheckLiveRunAfterCompanyWait(t *testing.T) {
	for _, requestType := range []string{"Approval", "Question"} {
		t.Run(requestType, func(t *testing.T) {
			ctx, pool, store, bus, fixture := newHumanInputAuthorizationFixture(t)
			principal := auth.UserPrincipal{
				UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
			}
			origin := seedDirectRunningOrigin(t, ctx, pool, fixture)
			requestKey := uuid.Must(uuid.NewV7())

			var raise func() error
			var table string
			switch requestType {
			case "Approval":
				participant := &planActionParticipantStub{}
				service, err := NewApprovalService(store, bus, participant, nil)
				if err != nil {
					t.Fatalf("NewApprovalService() error = %v", err)
				}
				raise = func() error {
					_, _, raiseErr := service.RequestCompanyPlanApply(
						ctx,
						principal,
						origin,
						requestKey,
						json.RawMessage(`{"artifactVersion":1}`),
					)
					return raiseErr
				}
				table = "approvals"
				t.Cleanup(func() {
					if participant.prepareCalls != 0 {
						t.Errorf(
							"PrepareCompanyPlanApply() calls = %d, want 0",
							participant.prepareCalls,
						)
					}
				})
			case "Question":
				service, err := NewQuestionService(store, bus, nil)
				if err != nil {
					t.Fatalf("NewQuestionService() error = %v", err)
				}
				raise = func() error {
					_, _, raiseErr := service.Ask(
						ctx,
						principal,
						origin,
						requestKey,
						testQuestionAskInput(),
					)
					return raiseErr
				}
				table = "questions"
			default:
				t.Fatalf("unknown request type %q", requestType)
			}

			err := cancelRunAheadOfHumanInputRaise(t, ctx, pool, origin, raise)
			if !errors.Is(err, execution.ErrRunProofInvalid) {
				t.Fatalf("raise after Run cancellation error = %v, want ErrRunProofInvalid", err)
			}
			var count int
			if err := pool.QueryRow(
				ctx,
				fmt.Sprintf(
					"SELECT count(*) FROM %s WHERE origin_run_id=$1",
					table,
				), //nolint:gosec // fixed test table names.
				origin.RunID,
			).Scan(&count); err != nil {
				t.Fatalf("count post-cancellation %s rows: %v", requestType, err)
			}
			if count != 0 {
				t.Fatalf("post-cancellation %s count = %d, want 0", requestType, count)
			}
		})
	}
}

func newHumanInputAuthorizationFixture(
	t *testing.T,
) (context.Context, *pgxpool.Pool, *db.Store, *events.Bus, dbtest.Fixture) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	return ctx, pool, store, bus, dbtest.SeedFixture(t, ctx, pool)
}

func testQuestionAskInput() QuestionAskInput {
	return QuestionAskInput{Title: "Choose a target", Prompts: []QuestionPrompt{{
		ID: "target", Prompt: "Which target?", SelectionMode: QuestionSelectionSingle,
		Required: true,
		Options: []QuestionOption{
			{ID: "a", Label: "Target A"},
			{ID: "b", Label: "Target B"},
		},
	}}}
}

func revokeMembershipAheadOfHumanInputMutation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, userID uuid.UUID,
	mutation func() error,
) error {
	t.Helper()
	holder := lockHumanInputTestCompany(t, ctx, pool, companyID)
	defer func() { _ = holder.Rollback(context.Background()) }()

	revoked := make(chan error, 1)
	go func() {
		revoked <- revokeHumanInputTestMembership(ctx, pool, companyID, userID)
	}()
	waitForHumanInputCompanyLockers(t, ctx, pool, 1)
	mutated := make(chan error, 1)
	go func() { mutated <- mutation() }()
	waitForHumanInputCompanyLockers(t, ctx, pool, 2)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release Company lock: %v", err)
	}
	if err := receiveHumanInputRaceResult(t, revoked, "membership revocation"); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}
	return receiveHumanInputRaceResult(t, mutated, "human-input mutation")
}

func cancelRunAheadOfHumanInputRaise(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	origin execution.RunOrigin,
	raise func() error,
) error {
	t.Helper()
	holder := lockHumanInputTestCompany(t, ctx, pool, origin.CompanyID)
	defer func() { _ = holder.Rollback(context.Background()) }()

	cancelled := make(chan error, 1)
	go func() { cancelled <- cancelHumanInputTestRun(ctx, pool, origin) }()
	waitForHumanInputCompanyLockers(t, ctx, pool, 1)
	raised := make(chan error, 1)
	go func() { raised <- raise() }()
	waitForHumanInputCompanyLockers(t, ctx, pool, 2)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release Company lock: %v", err)
	}
	if err := receiveHumanInputRaceResult(t, cancelled, "Run cancellation"); err != nil {
		t.Fatalf("cancel Run: %v", err)
	}
	return receiveHumanInputRaceResult(t, raised, "human-input raise")
}

func lockHumanInputTestCompany(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Company lock holder: %v", err)
	}
	if _, err := generated.New(tx).LockCompanyForMembershipMutation(ctx, companyID); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("hold Company lock: %v", err)
	}
	return tx
}

func revokeHumanInputTestMembership(
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, userID uuid.UUID,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := generated.New(tx)
	if _, err := q.LockCompanyForMembershipMutation(ctx, companyID); err != nil {
		return err
	}
	membership, err := q.GetCompanyMemberForUpdate(
		ctx,
		generated.GetCompanyMemberForUpdateParams{CompanyID: companyID, UserID: userID},
	)
	if err != nil {
		return err
	}
	if _, err := q.RevokeCompanyMember(ctx, generated.RevokeCompanyMemberParams{
		ActorUserID: userID, CompanyID: companyID, UserID: userID, ExpectedVersion: membership.Version,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cancelHumanInputTestRun(
	ctx context.Context,
	pool *pgxpool.Pool,
	origin execution.RunOrigin,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	q := generated.New(tx)
	if _, err := q.LockCompanyForMembershipMutation(ctx, origin.CompanyID); err != nil {
		return err
	}
	if _, err := q.LockHumanInputOriginConversation(
		ctx,
		generated.LockHumanInputOriginConversationParams{
			ConversationID: origin.ConversationID, CompanyID: origin.CompanyID,
			ConversationKind: origin.ConversationKind, TaskID: util.NullUUIDFromPtr(origin.TaskID),
			AgentID: origin.AgentID, OriginUserID: util.NullUUIDFromPtr(origin.OriginUserID),
		},
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE runs
		SET status='cancelled', updated_at=clock_timestamp()
		WHERE id=$1 AND status='running'`, origin.RunID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func waitForHumanInputCompanyLockers(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	want int,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		var got int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname=current_database()
			  AND pid<>pg_backend_pid()
			  AND wait_event_type='Lock'
			  AND query LIKE '%LockCompanyForMembershipMutation%'`).Scan(&got); err != nil {
			t.Fatalf("count Company lock waiters: %v", err)
		}
		if got >= want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %d Company lockers: %v", want, ctx.Err())
		case <-timeout.C:
			t.Fatalf("Company lock waiters = %d, want at least %d", got, want)
		case <-ticker.C:
		}
	}
}

func receiveHumanInputRaceResult(t *testing.T, result <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish", operation)
		return nil
	}
}
