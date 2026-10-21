//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestChatModelSettingsUpdatesCarryTheirAudience(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		kind       Kind
		visibility string
	}{
		{name: "restricted project", kind: KindTask, visibility: "restricted"},
		{name: "company project", kind: KindTask, visibility: "company"},
		{name: "direct chat", kind: KindDirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			pool, _ := dbtest.StartPostgres(t)
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
			s := newTestConversationService(t, store, bus)
			owner := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA, Role: auth.CompanyRoleOwner}
			var readerID uuid.UUID
			if err := pool.QueryRow(ctx, `INSERT INTO users (email, display_name)
				VALUES ('model-reader@example.com', 'Reader') RETURNING id`).Scan(&readerID); err != nil {
				t.Fatal(err)
			}
			dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members (company_id, user_id, role)
				VALUES ($1, $2, 'member'), ($1, $3, 'member')`, f.CompanyA, readerID, f.UserB)
			dbtest.GrantProject(
				t,
				ctx,
				pool,
				f.CompanyA,
				f.ProjectA,
				readerID,
				string(auth.ProjectPermissionRead),
			)

			id := f.ConversationA
			if tc.kind == KindDirect {
				id = f.DirectConversationA
			} else {
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`UPDATE projects SET visibility=$1 WHERE id=$2`,
					tc.visibility,
					f.ProjectA,
				)
			}
			settings, err := s.FetchModelSettings(ctx, owner, id)
			if err != nil {
				t.Fatal(err)
			}
			initialRevision := settings.Revision
			var published []events.Event
			var visible []ModelSettings
			var fetchErr error
			if err := bus.Subscribe(
				EventConversationUpdated,
				func(ctx context.Context, event events.Event) error {
					published = append(published, event)
					// A separate database read when the event arrives must already
					// see the saved settings and history: publish only after commit.
					committed, err := s.FetchModelSettings(ctx, owner, id)
					fetchErr = err
					visible = append(visible, committed)
					return err
				},
			); err != nil {
				t.Fatal(err)
			}
			model := "chat-model"
			// Both a model change and a thinking-only change must notify readers.
			for _, thinking := range []string{"medium", "high"} {
				published, visible = nil, nil
				saved, err := s.UpdateModelSettings(ctx, owner, id, ModelSettingsInput{
					AgentID:          f.AgentA,
					ExpectedRevision: settings.Revision,
					Model:            &model,
					ThinkingLevel:    &thinking,
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(published) != 1 || len(visible) != 1 || fetchErr != nil {
					t.Fatalf(
						"updates = %d, committed reads = %d, error = %v, want one successful update",
						len(published),
						len(visible),
						fetchErr,
					)
				}
				if !reflect.DeepEqual(visible[0], saved) || len(saved.Changes) != 1 {
					t.Fatalf(
						"settings visible at publish = %+v, want saved %+v with one model change",
						visible[0],
						saved,
					)
				}
				event := published[0]
				if event.CompanyID != f.CompanyA || event.ConversationID != id {
					t.Fatalf(
						"event scope = %s/%s, want %s/%s",
						event.CompanyID,
						event.ConversationID,
						f.CompanyA,
						id,
					)
				}
				payload, err := events.PayloadOf[Snapshot](event)
				if err != nil {
					t.Fatal(err)
				}
				if payload.Conversation.Kind != tc.kind ||
					payload.Conversation.Revision != saved.Revision {
					t.Fatalf(
						"conversation = %+v, want kind %s and revision %d",
						payload.Conversation,
						tc.kind,
						saved.Revision,
					)
				}
				audience := payload.ProjectAudience()
				switch {
				case tc.kind == KindDirect:
					if audience != nil || payload.Conversation.CreatedByUserID == nil ||
						*payload.Conversation.CreatedByUserID != f.UserA {
						t.Fatalf(
							"direct chat routing = %+v, want creator %s without a Project audience",
							payload,
							f.UserA,
						)
					}
				case tc.visibility == "company":
					if audience == nil || !audience.CompanyWide {
						t.Fatalf("audience = %+v, want the Company", audience)
					}
				default:
					if audience == nil || audience.CompanyWide {
						t.Fatalf("audience = %+v, want only the Project's readers", audience)
					}
					want := []uuid.UUID{f.UserA, readerID}
					if len(audience.UserIDs) != len(want) {
						t.Fatalf("readers = %v, want %v", audience.UserIDs, want)
					}
					for _, userID := range want {
						if !slices.Contains(audience.UserIDs, userID) {
							t.Fatalf(
								"readers = %v, want %v without member %s",
								audience.UserIDs,
								want,
								f.UserB,
							)
						}
					}
				}
				settings = saved
			}

			published = nil
			input := ModelSettingsInput{AgentID: f.AgentA, ExpectedRevision: initialRevision, Model: &model}
			if _, err := s.UpdateModelSettings(ctx, owner, id, input); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale update error = %v, want conflict", err)
			}
			reader := auth.UserPrincipal{
				UserID:    readerID,
				CompanyID: f.CompanyA,
				Role:      auth.CompanyRoleMember,
			}
			input.ExpectedRevision = settings.Revision
			wantErr := ErrModelSettingsForbidden
			if tc.kind == KindDirect {
				wantErr = ErrNotFound
			}
			if _, err := s.UpdateModelSettings(ctx, reader, id, input); !errors.Is(err, wantErr) {
				t.Fatalf("reader update error = %v, want %v", err, wantErr)
			}
			if len(published) != 0 {
				t.Fatalf("failed updates published %d events, want none", len(published))
			}
		})
	}
}

func TestChatModelChangeHistory(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := newTestConversationService(t, store, bus)
	p := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET model='old-model' WHERE id=$1`, f.AgentA)
	chat, err := s.CreateDirect(ctx, p, CreateDirectInput{AgentID: f.AgentA})
	if err != nil {
		t.Fatal(err)
	}
	id := chat.Conversation.ID
	type change struct {
		ID            uuid.UUID `json:"id"`
		AgentID       uuid.UUID `json:"agentId"`
		PreviousModel *string   `json:"previousModel"`
		Model         *string   `json:"model"`
		AfterSeq      int64     `json:"afterSeq"`
		CreatedAt     time.Time `json:"createdAt"`
	}
	readChanges := func(settings ModelSettings) []change {
		t.Helper()
		encoded, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Changes []change `json:"changes"`
		}
		if err := json.Unmarshal(encoded, &response); err != nil {
			t.Fatal(err)
		}
		if response.Changes == nil {
			t.Fatalf("model settings = %s, want changes array", encoded)
		}
		return response.Changes
	}
	settings, err := s.FetchModelSettings(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := readChanges(settings); len(got) != 0 {
		t.Fatalf("initial changes = %+v, want empty", got)
	}
	model, thinking := "new-model", "high"
	input := ModelSettingsInput{
		AgentID:          f.AgentA,
		ExpectedRevision: settings.Revision,
		Model:            &model,
		ThinkingLevel:    &thinking,
	}
	saved, err := s.UpdateModelSettings(ctx, p, id, input)
	if err != nil {
		t.Fatal(err)
	}
	changes := readChanges(saved)
	if len(changes) != 1 || changes[0].ID == uuid.Nil || changes[0].AgentID != f.AgentA ||
		changes[0].PreviousModel == nil || *changes[0].PreviousModel != "old-model" ||
		changes[0].Model == nil || *changes[0].Model != model || changes[0].AfterSeq != 0 || changes[0].CreatedAt.IsZero() {
		t.Fatalf("saved changes = %+v", changes)
	}
	if _, err := s.UpdateModelSettings(ctx, p, id, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v, want conflict", err)
	}
	for _, level := range []string{"high", "low"} {
		input.ExpectedRevision, input.ThinkingLevel = saved.Revision, &level
		saved, err = s.UpdateModelSettings(ctx, p, id, input)
		if err != nil || len(readChanges(saved)) != 1 {
			t.Fatalf("same model update = %+v, %v", saved, err)
		}
	}
	sent, err := s.Send(
		ctx,
		p,
		MessageTarget{Kind: TargetConversation, ID: id},
		SendInput{RequestKey: uuid.New(), Content: "hello"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var throughSeq int64
	if err := pool.QueryRow(ctx, `SELECT max(seq) FROM conversation_items WHERE conversation_id=$1`, id).
		Scan(&throughSeq); err != nil {
		t.Fatal(err)
	}
	if sent.RunID == nil || throughSeq == 0 {
		t.Fatal("send did not create a durable message")
	}
	_, err = s.UpdateModelSettings(
		ctx,
		p,
		id,
		ModelSettingsInput{AgentID: f.AgentA, ExpectedRevision: saved.Revision},
	)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := newTestConversationService(t, store, bus).FetchModelSettings(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	restored := readChanges(reloaded)
	if len(restored) != 2 || restored[0].ID != changes[0].ID || restored[1].PreviousModel == nil ||
		*restored[1].PreviousModel != model || restored[1].Model != nil || restored[1].AfterSeq != throughSeq {
		t.Fatalf("restored changes = %+v", restored)
	}
	other, err := s.FetchModelSettings(ctx, p, f.ConversationA)
	if err != nil || len(readChanges(other)) != 0 {
		t.Fatalf("other chat changes = %+v, %v", other, err)
	}
}

func TestChatModelSettingsDefaultsIsolationAndFrozenRuns(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := newTestConversationService(t, store, bus)
	p := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE agents SET model='default-model', thinking_level='medium' WHERE id=$1`,
		fixture.AgentA,
	)
	newChat := func() uuid.UUID {
		t.Helper()
		key := uuid.New()
		result, err := s.CreateDirect(ctx, p, CreateDirectInput{AgentID: fixture.AgentA, RequestKey: &key})
		if err != nil {
			t.Fatal(err)
		}
		return result.Conversation.ID
	}
	first, second := newChat(), newChat()
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE agents SET model='new-default', thinking_level='low' WHERE id=$1`,
		fixture.AgentA,
	)
	settings, err := s.FetchModelSettings(ctx, p, first)
	if err != nil || settings.Model == nil || *settings.Model != "default-model" ||
		*settings.ThinkingLevel != "medium" {
		t.Fatalf("settings = %+v, %v", settings, err)
	}
	model, thinking := "chat-model", "high"
	input := ModelSettingsInput{
		AgentID:          fixture.AgentA,
		ExpectedRevision: settings.Revision,
		Model:            &model,
		ThinkingLevel:    &thinking,
	}
	saved, err := s.UpdateModelSettings(ctx, p, first, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateModelSettings(ctx, p, first, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	other, err := s.FetchModelSettings(ctx, p, second)
	if err != nil || other.Model == nil || *other.Model != "default-model" {
		t.Fatalf("other chat = %+v, %v", other, err)
	}
	fresh, err := s.FetchModelSettings(ctx, p, newChat())
	if err != nil || fresh.Model == nil || *fresh.Model != "new-default" {
		t.Fatalf("new chat = %+v, %v", fresh, err)
	}
	result, err := s.Send(
		ctx,
		p,
		MessageTarget{Kind: TargetConversation, ID: first},
		SendInput{RequestKey: uuid.New(), Content: "hello"},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit null omits the provider override, never inheriting the Agent's defaults.
	_, err = s.UpdateModelSettings(
		ctx,
		p,
		first,
		ModelSettingsInput{AgentID: fixture.AgentA, ExpectedRevision: saved.Revision},
	)
	if err != nil {
		t.Fatal(err)
	}
	var frozenModel, frozenThinking string
	if err := pool.QueryRow(ctx, `SELECT context->'agent'->>'model', context->'agent'->>'thinkingLevel' FROM runs WHERE id=$1`, *result.RunID).
		Scan(&frozenModel, &frozenThinking); err != nil {
		t.Fatal(err)
	}
	if frozenModel != model || frozenThinking != thinking {
		t.Fatalf("frozen run = %s/%s", frozenModel, frozenThinking)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, *result.RunID)
	next, err := s.Send(
		ctx,
		p,
		MessageTarget{Kind: TargetConversation, ID: first},
		SendInput{RequestKey: uuid.New(), Content: "again"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var hasOverrides bool
	if err := pool.QueryRow(ctx, `SELECT (context->'agent') ? 'model' OR (context->'agent') ? 'thinkingLevel' FROM runs WHERE id=$1`, *next.RunID).
		Scan(&hasOverrides); err != nil {
		t.Fatal(err)
	}
	if hasOverrides {
		t.Fatal("provider defaults inherited Agent overrides")
	}
	// Another company member cannot read or change the creator's chat.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role,status) VALUES($1,$2,'member','active')`,
		fixture.CompanyA,
		fixture.UserB,
	)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB)
	p.UserID = fixture.UserB
	if _, err := s.FetchModelSettings(ctx, p, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign read = %v", err)
	}
	if _, err := s.UpdateModelSettings(ctx, p, first, input); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign update = %v", err)
	}
	taskSettings, err := s.FetchModelSettings(ctx, p, fixture.ConversationA)
	if err != nil || taskSettings.CanEdit {
		t.Fatalf("colleague Task settings = %+v, %v", taskSettings, err)
	}
	if _, err := s.UpdateModelSettings(
		ctx,
		p,
		fixture.ConversationA,
		ModelSettingsInput{AgentID: fixture.AgentA, ExpectedRevision: taskSettings.Revision, Model: &model},
	); !errors.Is(
		err,
		ErrModelSettingsForbidden,
	) {
		t.Fatalf("colleague update = %v", err)
	}
	p.UserID = fixture.UserA
	taskSettings, err = s.FetchModelSettings(ctx, p, fixture.ConversationA)
	if err != nil || !taskSettings.CanEdit {
		t.Fatalf("assignee Task settings = %+v, %v", taskSettings, err)
	}
	if _, err := s.UpdateModelSettings(
		ctx,
		p,
		fixture.ConversationA,
		ModelSettingsInput{AgentID: fixture.AgentB, ExpectedRevision: taskSettings.Revision, Model: &model},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale Agent update = %v", err)
	}
	if _, err := s.UpdateModelSettings(
		ctx,
		p,
		fixture.ConversationA,
		ModelSettingsInput{AgentID: fixture.AgentA, ExpectedRevision: taskSettings.Revision, Model: &model},
	); err != nil {
		t.Fatal(err)
	}
}
