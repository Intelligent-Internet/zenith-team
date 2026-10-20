//go:build integration

package task

import (
	"errors"
	"sync"
	"testing"

	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"github.com/google/uuid"
)

func TestSubmitHuddleNotesCreatesOneHumanTaskWithoutVoice(t *testing.T) {
	ctx, pool, f, s, p := startTaskHarness(t)
	peer := seedTaskMember(t, ctx, pool, f, "notes-peer@example.com")
	chats, err := conversation.NewConversationService(conversation.ServiceOptions{Store: s.store, Bus: s.bus, TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := chats.CreateHumanDirect(ctx, p, conversation.CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{peer}})
	if err != nil {
		t.Fatal(err)
	}
	calls, err := conversation.NewHuddleService(conversation.HuddleServiceOptions{Store: s.store, Bus: s.bus, Media: proposalTestMedia{}, NotesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	started, err := calls.Start(ctx, p, direct.Conversation.ID, conversation.StartHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := calls.Join(ctx, p, started.Huddle.ID, conversation.JoinHuddleRequest{RequestKey: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = calls.ConfirmConnected(ctx, p.CompanyID, joined.Connection.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO huddle_notes(company_id,huddle_id,expires_at,state,recap_state,recap_revision,recap) VALUES($1,$2,now()+interval '30 days','ended','ready',1,'{"summary":"Review release","decisions":[],"actions":[{"text":"Review release","sourceIds":[]}],"questions":[]}')`, p.CompanyID, started.Huddle.ID)
	input := SubmitHuddleNotesInput{HuddleID: started.Huddle.ID, ActionIndex: 0, ExpectedRevision: 1, ProjectID: f.ProjectA, Title: "Review release", MilestoneType: MilestoneTypeProduct, Confirm: true}
	var results [2]HuddleNotesSubmission
	var errs [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], errs[i] = s.SubmitHuddleNotes(ctx, p, input) }()
	}
	wg.Wait()
	for _, err = range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0].TaskID == uuid.Nil || results[0] != results[1] {
		t.Fatalf("submission receipts = %+v", results)
	}
	task, err := s.Get(ctx, p, results[0].TaskID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if task.Assignee.ID != p.UserID || task.ExecutedBy.Kind != ActorUser || task.Status != StatusBacklog {
		t.Fatalf("created Task = %+v", task)
	}
	var runs int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id=$1`, task.ID).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("runs = %d, %v", runs, err)
	}
	changed := input
	changed.ProjectID = f.ProjectB
	if _, err = s.SubmitHuddleNotes(ctx, p, changed); !errors.Is(err, conversation.ErrHuddleNotesConflict) {
		t.Fatalf("changed replay = %v", err)
	}
}
