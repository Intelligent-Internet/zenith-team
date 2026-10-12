//go:build integration

package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/reearth/ygo/crdt"
	"golang.org/x/sync/errgroup"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

func boardFixture(t *testing.T) (*DefaultBoardService, *pgxpool.Pool, auth.UserPrincipal, auth.UserPrincipal) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	return NewBoardService(
			store,
			bus,
		), pool, auth.UserPrincipal{
			CompanyID: f.CompanyA,
			UserID:    f.UserA,
			Role:      auth.CompanyRoleOwner,
		}, auth.UserPrincipal{
			CompanyID: f.CompanyA,
			UserID:    f.UserB,
			Role:      auth.CompanyRoleMember,
		}
}

func createTestBoard(
	t *testing.T,
	s *DefaultBoardService,
	p auth.UserPrincipal,
	collaborators ...auth.UserPrincipal,
) Board {
	t.Helper()
	b, e := s.Create(
		t.Context(),
		p,
		CreateBoardInput{RequestKey: uuid.New(), Title: "Ideas", Purpose: "Decide explicitly"},
	)
	if e != nil {
		t.Fatal(e)
	}
	for _, collaborator := range collaborators {
		if err := s.store.Queries().
			SetBoardMemberAccess(t.Context(), generated.SetBoardMemberAccessParams{CompanyID: p.CompanyID, BoardID: b.ID, UserID: collaborator.UserID, Access: "edit"}); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func grantTestBoardAccess(
	t *testing.T,
	s *DefaultBoardService,
	p auth.UserPrincipal,
	id uuid.UUID,
	actor protocol.ActorRef,
	access string,
) error {
	t.Helper()
	current, err := s.Access(t.Context(), p, id)
	if err != nil {
		return err
	}
	return s.SetAccess(
		t.Context(),
		p,
		id,
		BoardAccessInput{ExpectedRevision: current.Revision, Actor: actor, Access: access},
	)
}

func stickyCommand(draft, text string) BoardCommand {
	return BoardCommand{
		Type:       "create_item",
		DraftID:    draft,
		ItemType:   "sticky",
		Geometry:   &BoardGeometry{X: 10, Y: 20, Width: 220, Height: 150},
		Properties: &BoardProperties{Color: "#fef08a"},
		Text:       text,
	}
}

func boardTestRun(t *testing.T, pool *pgxpool.Pool, p auth.UserPrincipal) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	var agent, conversation, run uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM agents WHERE company_id=$1 LIMIT 1`, p.CompanyID).
		Scan(&agent); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM conversations WHERE company_id=$1 AND kind='direct' AND agent_id=$2 ORDER BY created_at DESC LIMIT 1`, p.CompanyID, agent).
		Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,execution_authorized_by_user_id,run_type) SELECT $1,$2,$3,id,'running',$4,'direct_chat' FROM runtimes WHERE company_id=$1 LIMIT 1 RETURNING id`, p.CompanyID, agent, conversation, p.UserID).
		Scan(&run); err != nil {
		t.Fatal(err)
	}
	return agent, run
}

func TestBoardHumanAndAgentCollaboration(t *testing.T) {
	t.Parallel()
	s, pool, p, other := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p, other)
	agent, run := boardTestRun(t, pool, p)
	scope := BoardScope{BoardID: board.ID, RunID: run}
	batch := BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("agent", "Hello")}}
	if _, err := s.Mutate(ctx, p, scope, batch); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("uninvited agent: %v", err)
	}
	if err := grantTestBoardAccess(
		t,
		s,
		p,
		board.ID,
		protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent},
		"edit",
	); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListForRun(ctx, p, run, 0)
	if err != nil || len(listed.Boards) != 1 || !listed.Boards[0].CanEdit || listed.Boards[0].CanManage {
		t.Fatalf("invited Board library: %+v, %v", listed, err)
	}
	page, err := s.ListForRun(ctx, p, run, 100)
	if err != nil || len(page.Boards) != 0 || page.HasMore {
		t.Fatalf("invitation pagination: %+v, %v", page, err)
	}
	created, err := s.Mutate(ctx, p, scope, batch)
	if err != nil {
		t.Fatal(err)
	}
	item := created.Items[0]
	if item.CreatedBy.Kind != "agent" || item.CreatedBy.ID != agent || item.CreatedByRunID == nil ||
		*item.CreatedByRunID != run {
		t.Fatalf("agent attribution: %+v", item)
	}
	if _, err := s.Mutate(ctx, other, scope, batch); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("wrong member: %v", err)
	}
	// Two live Runs must not share receipts, even on the same member's binding.
	var secondRun uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,execution_authorized_by_user_id,run_type) SELECT company_id,agent_id,conversation_id,runtime_id,'running',execution_authorized_by_user_id,run_type FROM runs WHERE id=$1 RETURNING id`, run).
		Scan(&secondRun); err != nil {
		t.Fatal(err)
	}
	second, err := s.Mutate(ctx, p, BoardScope{BoardID: board.ID, RunID: secondRun}, batch)
	if err != nil || second.IDs["agent"] == item.ID {
		t.Fatalf("independent Run receipt: %+v, %v", second, err)
	}
	// A human has a buffered CRDT edit while the Agent edits the same note.
	hydrated, err := s.Hydrate(
		ctx,
		other,
		BoardScope{BoardID: board.ID},
		BoardHydrateInput{ItemIDs: []uuid.UUID{item.ID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err = doc.ApplyUpdate(hydrated.Items[0].State); err != nil {
		t.Fatal(err)
	}
	paragraph := doc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement)
	body := paragraph.Children()[0].(*crdt.YXmlText)
	doc.Transact(func(tx *crdt.Transaction) { body.Insert(tx, 0, "Human: ", nil) })
	text := BoardBatch{
		RequestKey: uuid.New(),
		Commands: []BoardCommand{
			{
				Type:                 "text",
				ID:                   &item.ID,
				ExpectedRevision:     item.LifecycleRevision,
				ExpectedTextRevision: item.TextRevision,
				Text:                 "Hello humans",
			},
		},
	}
	if _, err := s.Mutate(ctx, p, scope, text); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(
		ctx,
		p,
		scope,
		BoardBatch{RequestKey: uuid.New(), Commands: text.Commands},
	); !errors.Is(
		err,
		ErrBoardConflict,
	) {
		t.Fatalf("stale text: %v", err)
	}
	merged, err := s.Text(
		ctx,
		other,
		BoardScope{BoardID: board.ID},
		BoardTextInput{
			RequestKey:        uuid.New(),
			ItemID:            item.ID,
			LifecycleRevision: item.LifecycleRevision,
			Update:            doc.EncodeStateAsUpdate(),
		},
	)
	if err != nil || !strings.Contains(merged.Text, "Human: ") || !strings.Contains(merged.Text, "Hello humans") {
		t.Fatalf("human/Agent CRDT merge: %+v, %v", merged, err)
	}
	geometry := BoardGeometry{X: 400, Y: 300, Width: 220, Height: 150}
	moved, err := s.Mutate(
		ctx,
		other,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands: []BoardCommand{
				{
					Type:             "geometry",
					ID:               &item.ID,
					ExpectedRevision: item.GeometryRevision,
					Geometry:         &geometry,
				},
			},
		},
	)
	if err != nil || moved.Items[0].TextRevision != merged.TextRevision ||
		moved.Items[0].UpdatedBy.ID != other.UserID ||
		moved.Items[0].UpdatedByRunID != nil ||
		moved.Items[0].CreatedBy.ID != agent {
		t.Fatalf("independent geometry and actual attribution: %+v, %v", moved, err)
	}
	if err := grantTestBoardAccess(
		t,
		s,
		p,
		board.ID,
		protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent},
		"read",
	); err != nil {
		t.Fatal(err)
	}
	read, err := s.Context(ctx, p, scope, BoardContextInput{})
	if err != nil || read.Board.CanEdit || read.Board.CanManage {
		t.Fatalf("read-only context authority: %+v, %v", read.Board, err)
	}
	if _, err := s.Mutate(ctx, p, scope, batch); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("read-only replay: %v", err)
	}
	if err := grantTestBoardAccess(
		t,
		s,
		p,
		board.ID,
		protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent},
		"none",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Context(ctx, p, scope, BoardContextInput{}); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("revoked read: %v", err)
	}
	if err := grantTestBoardAccess(
		t,
		s,
		p,
		board.ID,
		protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent},
		"edit",
	); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='cancelled' WHERE id=$1`, run)
	if _, err := s.Mutate(ctx, p, scope, batch); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("terminal replay: %v", err)
	}
	var taskRun, taskID uuid.UUID
	if err := pool.QueryRow(ctx, `UPDATE runs r SET status='running',execution_authorized_by_user_id=$2,task_execution_generation=t.execution_generation FROM tasks t WHERE r.task_id=t.id AND r.company_id=$1 RETURNING r.id,t.id`, p.CompanyID, p.UserID).
		Scan(&taskRun, &taskID); err != nil {
		t.Fatal(err)
	}
	taskScope := BoardScope{BoardID: board.ID, RunID: taskRun}
	if _, err := s.Mutate(ctx, p, taskScope, batch); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE tasks SET execution_generation=execution_generation+1 WHERE id=$1`,
		taskID,
	)
	if _, err := s.Mutate(ctx, p, taskScope, batch); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("outdated Task generation replay: %v", err)
	}
}

func TestBoardAtomicCommandsReceiptsAndLifecycle(t *testing.T) {
	t.Parallel()
	s, pool, p, other := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p, other)
	request := BoardBatch{
		RequestKey: uuid.New(),
		Commands: []BoardCommand{
			stickyCommand("a", "Tiếng Việt\nline two"),
			stickyCommand("b", "Another"),
			{
				Type:      "create_edge",
				DraftID:   "edge",
				SourceRef: "a",
				TargetRef: "b",
				Label:     "supports",
				Relation:  "supports",
			},
		},
	}
	got, err := s.Mutate(ctx, p, BoardScope{BoardID: board.ID}, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.IDs) != 3 || got.Sequence != 1 {
		t.Fatalf("creation = %+v", got)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM board_changes WHERE board_id=$1`, board.ID)
	replay, err := s.Mutate(ctx, p, BoardScope{BoardID: board.ID}, request)
	if err != nil || replay.IDs["a"] != got.IDs["a"] || replay.Sequence != got.Sequence {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	request.Commands[0].Text = "different"
	if _, err = s.Mutate(ctx, p, BoardScope{BoardID: board.ID}, request); !errors.Is(err, ErrBoardRequestKeyReused) {
		t.Fatalf("key reuse = %v", err)
	}
	id := got.IDs["a"]
	bad := BoardBatch{
		RequestKey: uuid.New(),
		Commands: []BoardCommand{
			stickyCommand("rollback", "no"),
			{
				Type:             "geometry",
				ID:               &id,
				ExpectedRevision: 77,
				Geometry:         &BoardGeometry{Width: 220, Height: 150},
			},
		},
	}
	if _, err = s.Mutate(ctx, p, BoardScope{BoardID: board.ID}, bad); !errors.Is(err, ErrBoardConflict) {
		t.Fatalf("stale batch = %v", err)
	}
	snapshot, err := s.Snapshot(ctx, p, BoardScope{BoardID: board.ID}, 0)
	if err != nil || len(snapshot.Items) != 2 {
		t.Fatalf("atomic snapshot = %+v,%v", snapshot, err)
	}
	foreign := p
	foreign.CompanyID = uuid.New()
	if _, err = s.Snapshot(ctx, foreign, BoardScope{BoardID: board.ID}, 0); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("foreign read = %v", err)
	}
	archived := true
	if _, err = s.Update(
		ctx,
		other,
		board.ID,
		UpdateBoardInput{ExpectedRevision: 1, Archived: &archived},
	); !errors.Is(
		err,
		ErrBoardForbidden,
	) {
		t.Fatalf("member archive = %v", err)
	}
	saved, err := s.Update(ctx, p, board.ID, UpdateBoardInput{ExpectedRevision: 1, Archived: &archived})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("new", "no")}},
	); !errors.Is(
		err,
		ErrBoardArchived,
	) {
		t.Fatalf("archived mutate = %v", err)
	}
	archived = false
	if _, err = s.Update(
		ctx,
		p,
		board.ID,
		UpdateBoardInput{ExpectedRevision: saved.Revision, Archived: &archived},
	); err != nil {
		t.Fatal(err)
	}
	if err = s.Favorite(ctx, other, board.ID, true); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, other, BoardListInput{Search: "Tiếng Việt", Favorites: true})
	if err != nil || len(list.Boards) != 1 {
		t.Fatalf("search = %+v, %v", list, err)
	}
}

func TestBoardConcurrentTextGeometryUndoAndRevocation(t *testing.T) {
	t.Parallel()
	s, pool, p, b := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p, b)
	result, err := s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("note", "")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := result.IDs["note"]
	hydrated, err := s.Hydrate(ctx, p, BoardScope{BoardID: board.ID}, BoardHydrateInput{ItemIDs: []uuid.UUID{id}})
	if err != nil {
		t.Fatal(err)
	}
	updates := make([][]byte, 2)
	for i, text := range []string{"Xin chào\n", "Việt Nam\nMultiline paste"} {
		d := crdt.New()
		if err = crdt.ApplyUpdateV1(d, hydrated.Items[0].State, nil); err != nil {
			t.Fatal(err)
		}
		paragraph, _ := d.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement)
		body, _ := paragraph.Children()[0].(*crdt.YXmlText)
		d.Transact(func(tx *crdt.Transaction) { body.Insert(tx, 0, text, nil) })
		updates[i] = d.EncodeStateAsUpdate()
		d.Destroy()
	}
	group := new(errgroup.Group)
	for i, actor := range []auth.UserPrincipal{p, b} {
		group.Go(func() error {
			_, e := s.Text(
				ctx,
				actor,
				BoardScope{BoardID: board.ID},
				BoardTextInput{
					RequestKey:        uuid.New(),
					ItemID:            id,
					LifecycleRevision: 1,
					Update:            updates[i],
				},
			)
			return e
		})
	}
	group.Go(func() error {
		_, e := s.Mutate(
			ctx,
			p,
			BoardScope{BoardID: board.ID},
			BoardBatch{
				RequestKey: uuid.New(),
				Commands: []BoardCommand{
					{
						Type:             "geometry",
						ID:               &id,
						ExpectedRevision: 1,
						Geometry: &BoardGeometry{
							X:      500,
							Y:      500,
							Width:  220,
							Height: 150,
						},
					},
				},
			},
		)
		return e
	})
	if err = group.Wait(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(ctx, p, BoardScope{BoardID: board.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	item := snapshot.Items[0]
	if item.X != 500 || !strings.Contains(item.Text, "Xin chào") || !strings.Contains(item.Text, "Việt Nam") ||
		item.TextRevision != 3 {
		t.Fatalf("convergence = %+v", item)
	}
	if _, err = s.Mutate(
		ctx,
		b,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands: []BoardCommand{
				{
					Type:             "geometry",
					ID:               &id,
					ExpectedRevision: 2,
					Geometry:         &BoardGeometry{X: 600, Width: 220, Height: 150},
				},
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands: []BoardCommand{
				{
					Type:             "geometry",
					ID:               &id,
					ExpectedRevision: 2,
					Geometry:         &BoardGeometry{X: 10, Width: 220, Height: 150},
				},
			},
		},
	); !errors.Is(
		err,
		ErrBoardConflict,
	) {
		t.Fatalf("stale undo = %v", err)
	}
	if _, err = s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands:   []BoardCommand{{Type: "delete_item", ID: &id, ExpectedRevision: 1}},
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Text(
		ctx,
		b,
		BoardScope{BoardID: board.ID},
		BoardTextInput{RequestKey: uuid.New(), ItemID: id, LifecycleRevision: 1, Update: updates[0]},
	); !errors.Is(
		err,
		ErrBoardItemNotFound,
	) {
		t.Fatalf("deleted note edit = %v", err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM company_members WHERE company_id=$1 AND user_id=$2`,
		p.CompanyID,
		b.UserID,
	)
	if _, err = s.Snapshot(ctx, b, BoardScope{BoardID: board.ID}, 0); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("revoked snapshot = %v", err)
	}
}

func TestBoardBoundedContextAndLazySnapshot(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	board := createTestBoard(t, s, p)
	ctx := context.Background()
	long := strings.Repeat("Tiếng Việt\n", 200)
	result, err := s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands: []BoardCommand{
				stickyCommand("a", long),
				stickyCommand("b", "Target"),
				{Type: "create_edge", DraftID: "e", SourceRef: "a", TargetRef: "b", Label: "supports"},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(ctx, p, BoardScope{BoardID: board.ID}, 0)
	truncated := false
	for _, item := range snapshot.Items {
		if item.ID == result.IDs["a"] {
			truncated = item.TextTruncated
		}
	}
	if err != nil || !truncated {
		t.Fatalf("lazy snapshot = %+v,%v", snapshot, err)
	}
	projection, err := s.Context(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardContextInput{ItemIDs: []uuid.UUID{result.IDs["a"]}, MaxItems: 1, MaxBytes: 16384},
	)
	if err != nil || len(projection.Items) != 1 || len(projection.OmittedIDs) != 1 ||
		!strings.Contains(projection.Markdown, "Tiếng Việt") {
		t.Fatalf("context = %+v,%v", projection, err)
	}
}

func TestBoardCreateRetryAndSnapshotBoundary(t *testing.T) {
	t.Parallel()
	s, pool, p, b := boardFixture(t)
	ctx := t.Context()
	request := CreateBoardInput{RequestKey: uuid.New(), Title: "One Board"}
	ids := make(chan uuid.UUID, 8)
	group := new(errgroup.Group)
	for range 8 {
		group.Go(func() error {
			board, err := s.Create(ctx, p, request)
			if err == nil {
				ids <- board.ID
			}
			return err
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatal(err)
	}
	close(ids)
	var first uuid.UUID
	for id := range ids {
		if first == uuid.Nil {
			first = id
		}
		if id != first {
			t.Fatal("concurrent create retry duplicated a Board")
		}
	}
	if err := grantTestBoardAccess(
		t,
		s,
		p,
		first,
		protocol.ActorRef{ID: b.UserID, Kind: protocol.ActorKindUser},
		"edit",
	); err != nil {
		t.Fatal(err)
	}
	result, err := s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: first},
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("a", "first")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Mutate(
		ctx,
		b,
		BoardScope{BoardID: first},
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("b", "second")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(ctx, p, BoardScope{BoardID: first}, result.Sequence)
	if err != nil || snapshot.Full || snapshot.Sequence != second.Sequence || len(snapshot.Items) != 1 ||
		snapshot.Items[0].ID != second.IDs["b"] {
		t.Fatalf("delta = %+v,%v", snapshot, err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM board_changes WHERE board_id=$1`, first)
	snapshot, err = s.Snapshot(ctx, p, BoardScope{BoardID: first}, result.Sequence)
	if err != nil || !snapshot.Full || len(snapshot.Items) != 2 {
		t.Fatalf("compacted fallback = %+v,%v", snapshot, err)
	}
	request.Title = "changed"
	if _, err = s.Create(ctx, p, request); !errors.Is(err, ErrBoardRequestKeyReused) {
		t.Fatalf("create key reuse = %v", err)
	}
}

func TestBoardFrameLocksTombstonesAndUndoGuards(t *testing.T) {
	t.Parallel()
	s, _, p, b := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p, b)
	frame := BoardCommand{
		Type:       "create_item",
		DraftID:    "frame",
		ItemType:   "frame",
		Geometry:   &BoardGeometry{Width: 1000, Height: 500},
		Properties: &BoardProperties{Title: "Ideas"},
	}
	child := stickyCommand("note", "Keep me")
	child.ParentRef = "frame"
	result, err := s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{frame, child}},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := result.IDs["note"]
	parent := result.IDs["frame"]
	locked := BoardProperties{Locked: true}
	if _, err = s.Mutate(
		ctx,
		b,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands: []BoardCommand{
				{Type: "content", ID: &id, ExpectedRevision: 1, Properties: &locked},
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands: []BoardCommand{
				{
					Type:             "geometry",
					ID:               &id,
					ExpectedRevision: 1,
					Geometry:         &BoardGeometry{Width: 220, Height: 150},
				},
			},
		},
	); !errors.Is(
		err,
		ErrBoardLocked,
	) {
		t.Fatalf("move locked = %v", err)
	}
	if _, err = s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands: []BoardCommand{
				{
					Type:                     "delete_item",
					ID:                       &id,
					ExpectedRevision:         1,
					ExpectedGeometryRevision: 1,
					ExpectedTextRevision:     1,
				},
			},
		},
	); !errors.Is(
		err,
		ErrBoardConflict,
	) {
		t.Fatalf("undo other's content = %v", err)
	}
	if _, err = s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands:   []BoardCommand{{Type: "delete_item", ID: &parent, ExpectedRevision: 1}},
		},
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(ctx, p, BoardScope{BoardID: board.ID}, 0)
	if err != nil || len(snapshot.Items) != 1 || snapshot.Items[0].ParentFrameID != nil ||
		snapshot.Items[0].GeometryRevision != 2 {
		t.Fatalf("frame deletion = %+v,%v", snapshot, err)
	}
	if _, err = s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{
			RequestKey: uuid.New(),
			Commands:   []BoardCommand{{Type: "restore_item", ID: &parent, ExpectedRevision: 2}},
		},
	); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.Snapshot(ctx, p, BoardScope{BoardID: board.ID}, 0)
	if err != nil || len(snapshot.Items) != 2 {
		t.Fatalf("restore = %+v,%v", snapshot, err)
	}
}

func TestBoardRestorationRejectsPreviousNoteSession(t *testing.T) {
	t.Parallel()
	s, _, p, b := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, p, b)
	created, err := s.Mutate(
		ctx,
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("note", "Committed")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := created.IDs["note"]
	hydrated, err := s.Hydrate(ctx, b, BoardScope{BoardID: board.ID}, BoardHydrateInput{ItemIDs: []uuid.UUID{id}})
	if err != nil {
		t.Fatal(err)
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err = crdt.ApplyUpdateV1(doc, hydrated.Items[0].State, nil); err != nil {
		t.Fatal(err)
	}
	paragraph, _ := doc.GetXmlFragment("content").Children()[0].(*crdt.YXmlElement)
	body, _ := paragraph.Children()[0].(*crdt.YXmlText)
	doc.Transact(func(tx *crdt.Transaction) { body.Insert(tx, 0, "Unsubmitted previous session: ", nil) })
	pending := BoardTextInput{
		RequestKey:        uuid.New(),
		ItemID:            id,
		LifecycleRevision: hydrated.Items[0].LifecycleRevision,
		Update:            doc.EncodeStateAsUpdate(),
	}
	for _, command := range []BoardCommand{{Type: "delete_item", ID: &id, ExpectedRevision: 1}, {Type: "restore_item", ID: &id, ExpectedRevision: 2}} {
		if _, err = s.Mutate(
			ctx,
			p,
			BoardScope{BoardID: board.ID},
			BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{command}},
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.Text(ctx, b, BoardScope{BoardID: board.ID}, pending); !errors.Is(err, ErrBoardConflict) {
		t.Fatalf("old session after restoration = %v, want conflict", err)
	}
	restored, err := s.Hydrate(ctx, b, BoardScope{BoardID: board.ID}, BoardHydrateInput{ItemIDs: []uuid.UUID{id}})
	if err != nil || restored.Items[0].LifecycleRevision == pending.LifecycleRevision ||
		restored.Items[0].Text != "Committed" {
		t.Fatalf("restored note = %+v,%v", restored, err)
	}
}

func TestBoardContextLimitsOmittedReferences(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	board := createTestBoard(t, s, p)
	commands := []BoardCommand{}
	for i := range 40 {
		commands = append(
			commands,
			BoardCommand{
				Type:     "create_item",
				DraftID:  fmt.Sprint(i),
				ItemType: "shape",
				Geometry: &BoardGeometry{Width: 100, Height: 100},
			},
		)
	}
	if _, err := s.Mutate(
		t.Context(),
		p,
		BoardScope{BoardID: board.ID},
		BoardBatch{RequestKey: uuid.New(), Commands: commands},
	); err != nil {
		t.Fatal(err)
	}
	got, err := s.Context(
		t.Context(),
		p,
		BoardScope{BoardID: board.ID},
		BoardContextInput{MaxItems: 1, MaxBytes: 4096},
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || got.OmittedCount != 39 || !got.OmittedIDsTruncated || len(raw) > 4096 {
		t.Fatalf(
			"bounded context = count%d, omitted%d, truncated%t, omittedTruncated%t, bytes%d",
			len(got.Items),
			got.OmittedCount,
			got.Truncated,
			got.OmittedIDsTruncated,
			len(raw),
		)
	}
}

func TestBoardRestorationRejectsPreviousGeometry(t *testing.T) {
	t.Parallel()
	s, _, p, peer := boardFixture(t)
	board := createTestBoard(t, s, p, peer)
	created, err := s.Mutate(t.Context(), p, BoardScope{BoardID: board.ID}, BoardBatch{
		RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("note", "Preserve restored work")},
	})
	if err != nil {
		t.Fatal(err)
	}
	item := created.Items[0]
	queuedGeometry := item.BoardGeometry
	queuedGeometry.X = 900
	for _, command := range []BoardCommand{
		{Type: "delete_item", ID: &item.ID, ExpectedRevision: 1},
		{Type: "restore_item", ID: &item.ID, ExpectedRevision: 2},
	} {
		if _, err = s.Mutate(t.Context(), p, BoardScope{BoardID: board.ID}, BoardBatch{
			RequestKey: uuid.New(), Commands: []BoardCommand{command},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Mutate(t.Context(), peer, BoardScope{BoardID: board.ID}, BoardBatch{
		RequestKey: uuid.New(), Commands: []BoardCommand{
			{
				Type:             "geometry",
				ID:               &item.ID,
				ExpectedRevision: item.GeometryRevision,
				Geometry:         &queuedGeometry,
			},
		},
	})
	if !errors.Is(err, ErrBoardConflict) {
		t.Fatalf("pre-deletion geometry after restoration = %v, want conflict", err)
	}
	saved, err := s.Snapshot(t.Context(), p, BoardScope{BoardID: board.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Items) != 1 || saved.Items[0].X != item.X {
		t.Fatalf("restored position was overwritten: %+v", saved.Items)
	}
}

// Connectors keep their notation: the defaults draw them as before, a
// relabel keeps a UML arrowhead, and only a message may return to its source.
func TestBoardConnectorNotationRoundTrips(t *testing.T) {
	t.Parallel()
	s, _, p, _ := boardFixture(t)
	board := createTestBoard(t, s, p)
	ctx := context.Background()
	scope := BoardScope{BoardID: board.ID}
	at, many := 60.0, "0..*"
	result, err := s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		stickyCommand("a", "Order"),
		stickyCommand("b", "Line"),
		{Type: "create_edge", DraftID: "plain", SourceRef: "a", TargetRef: "b"},
		{
			Type:        "create_edge",
			DraftID:     "uml",
			SourceRef:   "a",
			TargetRef:   "b",
			Label:       "has",
			Line:        BoardLineTypeDashed,
			StartMarker: BoardMarkerTypeDiamondFilled,
			EndMarker:   BoardMarkerTypeNone,
			Route:       BoardRouteTypeElbow,
			TargetLabel: &many,
		},
		{
			Type:      "create_edge",
			DraftID:   "self",
			SourceRef: "a",
			TargetRef: "a",
			Route:     BoardRouteTypeMessage,
			AnchorY:   &at,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	edges := func() map[uuid.UUID]BoardEdge {
		t.Helper()
		snapshot, err := s.Snapshot(ctx, p, scope, 0)
		if err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID]BoardEdge{}
		for _, edge := range snapshot.Edges {
			out[edge.ID] = edge
		}
		return out
	}
	got := edges()
	if plain := got[result.IDs["plain"]]; plain.Line != BoardLineTypeSolid ||
		plain.StartMarker != BoardMarkerTypeNone ||
		plain.EndMarker != BoardMarkerTypeArrow ||
		plain.Route != BoardRouteTypeFlow ||
		plain.AnchorY != nil {
		t.Fatalf("a plain connector = %+v, want the Board's usual arrow", plain)
	}
	uml := got[result.IDs["uml"]]
	if uml.Line != BoardLineTypeDashed || uml.StartMarker != BoardMarkerTypeDiamondFilled ||
		uml.EndMarker != BoardMarkerTypeNone || uml.Route != BoardRouteTypeElbow || uml.TargetLabel != "0..*" {
		t.Fatalf("a UML connector = %+v", uml)
	}
	if self := got[result.IDs["self"]]; self.Route != BoardRouteTypeMessage || self.AnchorY == nil ||
		*self.AnchorY != 60 {
		t.Fatalf("a self message = %+v", self)
	}
	if _, err := s.Mutate(ctx, p, scope, BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{{
		Type: "update_edge", ID: &uml.ID, ExpectedRevision: uml.Revision, Label: "owns",
	}}}); err != nil {
		t.Fatal(err)
	}
	if relabeled := edges()[uml.ID]; relabeled.Label != "owns" ||
		relabeled.StartMarker != BoardMarkerTypeDiamondFilled ||
		relabeled.Line != BoardLineTypeDashed ||
		relabeled.TargetLabel != "0..*" {
		t.Fatalf("relabeling dropped the notation: %+v", relabeled)
	}
	for name, c := range map[string]BoardCommand{
		"a self connector":        {Type: "create_edge", DraftID: "x", SourceRef: result.IDs["a"].String(), TargetRef: result.IDs["a"].String()},
		"a message without a row": {Type: "create_edge", DraftID: "x", SourceRef: result.IDs["a"].String(), TargetRef: result.IDs["b"].String(), Route: BoardRouteTypeMessage},
	} {
		if _, err := s.Mutate(
			ctx,
			p,
			scope,
			BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{c}},
		); !errors.Is(
			err,
			ErrBoardInvalid,
		) {
			t.Fatalf("%s = %v, want invalid", name, err)
		}
	}
}
