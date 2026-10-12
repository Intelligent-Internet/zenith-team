//go:build integration

package task

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestTaskMetadataPersistsAcrossProjectionsAndRevisionFences(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number=2 WHERE id=$1`, fixture.CompanyA)
	bus := newTestBus(t)
	var published []Response
	if err := bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		if event.Name == EventCreated || event.Name == EventUpdated {
			value, err := events.PayloadOf[Response](event)
			if err != nil {
				return err
			}
			published = append(published, value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	start, due := "2026-09-11", "2026-09-30"
	createdResult, err := service.Create(ctx,
		principal,
		CreateInput{
			ProjectID:     fixture.ProjectA,
			Key:           "metadata",
			Title:         "Metadata",
			MilestoneType: MilestoneTypeProduct,
			Assignee: ActorRef{
				Kind: ActorUser,
				ID:   fixture.UserA,
			},
			Labels: []string{
				" Design ",
				"design",
			},
			StartDate: &start,
			DueDate:   &due,
		})
	if err != nil {
		t.Fatal(err)
	}
	created := createdResult.Task
	assertMetadata := func(got Response, labels []string, startDate, dueDate *string) {
		t.Helper()
		if !reflect.DeepEqual(got.Labels, labels) || !sameDate(got.StartDate, startDate) ||
			!sameDate(got.DueDate, dueDate) {
			t.Fatalf("metadata = %+v", got)
		}
	}
	assertMetadata(created, []string{"Design"}, &start, &due)
	feed := activity.NewFeed(store.Queries())
	history := func() []activity.Entry {
		t.Helper()
		rows,
			_, err := feed.List(ctx,
			fixture.CompanyA,
			activity.ListOptions{
				UserID:     fixture.UserA,
				EntityType: "task",
				EntityID:   created.ID.String(),
				Limit:      10,
			})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	creation := history()
	if len(creation) != 1 ||
		creation[0].Action != "task.created" ||
		creation[0].Actor.Kind != "user" ||
		creation[0].Actor.ID == nil ||
		*creation[0].Actor.ID != fixture.UserA.String() {
		t.Fatalf("creation history = %+v", creation)
	}
	var creationDetails struct {
		Before json.RawMessage
		After  json.RawMessage
	}
	if err := json.Unmarshal(creation[0].Details, &creationDetails); err != nil {
		t.Fatal(err)
	}
	if string(creationDetails.Before) != "null" {
		t.Fatalf("create before = %s", creationDetails.Before)
	}
	assertTaskHistoryState(t, creationDetails.After, created)
	if len(published) != 1 {
		t.Fatalf("created events = %d", len(published))
	}
	assertMetadata(published[0], created.Labels, &start, &due)
	invalid := "2026-10-01"
	if _, err := service.Update(ctx,
		principal,
		created.ID,
		UpdateInput{
			ExpectedRevision: created.Revision,
			StartDate: DateChange{
				Present: true,
				Value:   &invalid,
			},
		}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reversed merged dates = %v", err)
	}
	title, priority := "Revised metadata", PriorityHigh
	updated, err := service.Update(ctx,
		principal,
		created.ID,
		UpdateInput{
			ExpectedRevision: created.Revision,
			Title:            &title,
			Priority:         &priority,
			Labels:           []string{},
			StartDate:        DateChange{Present: true},
		})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RunID != nil || updated.Task.Revision != created.Revision+1 {
		t.Fatalf("metadata update = %+v", updated)
	}
	assertMetadata(updated.Task, []string{}, nil, &due)
	changes := history()
	if len(changes) != 2 || changes[0].Revision == nil || *changes[0].Revision != updated.Task.Revision {
		t.Fatalf("change history = %+v", changes)
	}
	var changeDetails struct {
		Before json.RawMessage
		After  json.RawMessage
	}
	if err := json.Unmarshal(changes[0].Details, &changeDetails); err != nil {
		t.Fatal(err)
	}
	assertTaskHistoryState(t, changeDetails.Before, created)
	assertTaskHistoryState(t, changeDetails.After, updated.Task)
	if len(published) != 2 {
		t.Fatalf("updated events = %d", len(published))
	}
	assertMetadata(published[1], []string{}, nil, &due)
	if _, err := service.Update(ctx,
		principal,
		created.ID,
		UpdateInput{
			ExpectedRevision: created.Revision,
			DueDate:          DateChange{Present: true},
		}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	detail, err := service.Get(ctx, principal, created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertMetadata(detail, []string{}, nil, &due)
	page, err := service.List(ctx, principal, ListOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Tasks {
		if row.ID == created.ID {
			found = true
			assertMetadata(row, []string{}, nil, &due)
		}
	}
	if !found {
		t.Fatal("task absent from list")
	}
	if got := len(history()); got != 2 {
		t.Fatalf("history after stale replay = %d, want 2", got)
	}
	outsider := auth.UserPrincipal{UserID: fixture.UserB, CompanyID: fixture.CompanyB, Role: auth.CompanyRoleOwner}
	if _, err := service.Update(ctx,
		outsider,
		created.ID,
		UpdateInput{
			ExpectedRevision: detail.Revision,
			DueDate:          DateChange{Present: true},
		}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-company update = %v", err)
	}
}

// Compare the public safe history contract independently of the database view.
// Exact keys distinguish omitted values from explicit nulls and empty arrays.
func assertTaskHistoryState(t *testing.T, raw json.RawMessage, want Response) {
	t.Helper()
	expected := map[string]any{
		"id": want.ID, "companyId": want.CompanyID, "projectId": want.ProjectID, "key": want.Key, "number": want.Number,
		"title": want.Title, "description": want.Description, "status": want.Status,
		"priority": want.Priority, "labels": want.Labels, "startDate": want.StartDate, "dueDate": want.DueDate,
		"assigneeUserId": want.Assignee.ID, "executedByAgentId": nil,
		"creatorUserId": nil, "creatorAgentId": nil,
		"milestoneType":  want.MilestoneType,
		"conversationId": want.ConversationID, "completedAt": want.CompletedAt,
		"prerequisiteIds": []string{}, "revision": want.Revision,
		"createdAt": want.CreatedAt, "updatedAt": want.UpdatedAt,
	}
	if want.ExecutedBy.Kind == ActorAgent {
		expected["executedByAgentId"] = want.ExecutedBy.ID
	}
	if want.Creator.Kind == ActorUser {
		expected["creatorUserId"] = want.Creator.ID
	}
	if want.Creator.Kind == ActorAgent {
		expected["creatorAgentId"] = want.Creator.ID
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	var got, normalized map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"createdAt", "updatedAt", "completedAt"} {
		if normalized[key] == nil {
			continue
		}
		value, ok := got[key].(string)
		if !ok {
			t.Fatalf("snapshot %s missing timestamp: %s", key, raw)
		}
		gotTime, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		wantTime, err := time.Parse(time.RFC3339Nano, normalized[key].(string))
		if err != nil || !gotTime.Equal(wantTime) {
			t.Fatalf("snapshot %s = %v, want %v (%v)", key, gotTime, wantTime, err)
		}
		got[key] = normalized[key]
	}
	if !reflect.DeepEqual(got, normalized) {
		t.Fatalf("safe snapshot = %s, want %s", raw, encoded)
	}
}

func TestTaskHistorySerializesCompetingEditsAndSurvivesEventReplay(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := newTestBus(t)
	if err := activity.RegisterListeners(bus, store.Queries()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var announced []events.Event
	if err := bus.Subscribe(Topic, func(_ context.Context, event events.Event) error {
		mu.Lock()
		defer mu.Unlock()
		if event.Name == EventUpdated {
			announced = append(announced, event)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	original, err := service.Get(ctx, principal, fixture.TaskA, nil)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result MutationResult
		err    error
	}
	ready, start, outcomes := make(chan struct{}), make(chan struct{}), make(chan outcome)
	for _, title := range []string{"First competing edit", "Second competing edit"} {
		go func() {
			ready <- struct{}{}
			<-start
			result, err := service.Update(ctx,
				principal,
				fixture.TaskA,
				UpdateInput{
					ExpectedRevision: original.Revision,
					Title:            &title,
				})
			outcomes <- outcome{result, err}
		}()
	}
	<-ready
	<-ready
	close(start)
	var winner Response
	var wins, conflicts int
	for range 2 {
		got := <-outcomes
		switch {
		case got.err == nil:
			wins++
			winner = got.result.Task
		case errors.Is(got.err, ErrConflict):
			conflicts++
		default:
			t.Errorf("competing edit error = %v", got.err)
		}
	}
	if wins != 1 || conflicts != 1 || winner.Revision != original.Revision+1 {
		t.Fatalf("wins/conflicts = %d/%d, winner = %+v", wins, conflicts, winner)
	}
	// A fresh reader has no reference to the writer's service or event recorder.
	reader, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	read := func() []activity.Entry {
		t.Helper()
		rows,
			_, err := activity.NewFeed(reader.Queries()).List(ctx,
			fixture.CompanyA,
			activity.ListOptions{
				UserID:     fixture.UserA,
				EntityType: "task",
				EntityID:   fixture.TaskA.String(),
				Limit:      10,
			})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := read()
	if len(rows) != 1 ||
		rows[0].Actor.Kind != "user" ||
		rows[0].Actor.ID == nil ||
		*rows[0].Actor.ID != fixture.UserA.String() {
		t.Fatalf("winning history = %+v", rows)
	}
	var details struct {
		Before json.RawMessage
		After  json.RawMessage
	}
	if err := json.Unmarshal(rows[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	assertTaskHistoryState(t, details.Before, original)
	assertTaskHistoryState(t, details.After, winner)
	if len(announced) != 1 {
		t.Fatalf("task announcements = %d", len(announced))
	}
	if _, err := service.Update(ctx,
		principal,
		fixture.TaskA,
		UpdateInput{
			ExpectedRevision: original.Revision,
			Title:            &winner.Title,
		}); !errors.Is(err, ErrConflict) {
		t.Fatalf("lost-response replay = %v, want conflict", err)
	}
	bus.Publish(ctx, Topic, announced[0], announced[0])
	if got := read(); len(got) != 1 || got[0].ID != rows[0].ID ||
		!reflect.DeepEqual(got[0].Details, rows[0].Details) {
		t.Fatalf("replayed history = %+v", got)
	}
}

func TestTaskHistoryFailureRollsBackCreationAndAnnouncements(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number=2 WHERE id=$1`, fixture.CompanyA)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := newTestBus(t)
	var announced int
	if err := bus.SubscribeAll(func(context.Context, events.Event) error { announced++; return nil }); err != nil {
		t.Fatal(err)
	}
	service, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	counts := func() [5]int64 {
		t.Helper()
		var values [5]int64
		if err := pool.QueryRow(
			ctx,
			`SELECT (SELECT count(*) FROM tasks), (SELECT count(*) FROM conversations), (SELECT count(*) FROM activity_events), (SELECT count(*) FROM inbox_items), next_task_number FROM companies WHERE id=$1`,
			fixture.CompanyA,
		).Scan(&values[0],
			&values[1],
			&values[2],
			&values[3],
			&values[4]); err != nil {
			t.Fatal(err)
		}
		return values
	}
	before := counts()
	dbtest.MustExec(t,
		ctx,
		pool,
		`ALTER TABLE activity_events ADD CONSTRAINT reject_task_creation CHECK (action <> 'task.created')`)
	_,
		err = service.Create(ctx,
		principal,
		CreateInput{
			ProjectID:     fixture.ProjectA,
			Key:           "rollback",
			Title:         "Must roll back",
			MilestoneType: MilestoneTypeProduct,
			Assignee: ActorRef{
				Kind: ActorUser,
				ID:   fixture.UserA,
			},
		})
	if err == nil {
		t.Fatal("creation succeeded despite rejected history")
	}
	if got := counts(); got != before || announced != 0 {
		t.Fatalf("after failure counts = %v, want %v; announcements = %d", got, before, announced)
	}
}
