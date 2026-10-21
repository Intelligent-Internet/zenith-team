package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/schedule"
)

func TestSchedulePreviewRouteReturnsFrontendWireContract(t *testing.T) {
	t.Parallel()

	fixture := newScheduleHandlerFixture()
	sourceID := uuid.Must(uuid.NewV7())
	expression := "0 8 * * *"
	timeZone := "Asia/Ho_Chi_Minh"
	firstOccurrence := time.Date(2026, time.August, 18, 1, 0, 0, 0, time.UTC)
	service := &scheduleServiceStub{
		preview: func(
			_ context.Context,
			principal auth.UserPrincipal,
			input schedule.PreviewInput,
		) (schedule.PreviewResult, error) {
			if principal != fixture.principal || input.AgentID != fixture.agentID ||
				input.Name != "Daily Brief" || input.Instructions != "Summarize the last 24 hours." ||
				input.PresetKey == nil || *input.PresetKey != "daily_brief_v1" ||
				input.Trigger.Type != schedule.TriggerTypeCron || input.Trigger.Expression == nil ||
				*input.Trigger.Expression != expression || input.Trigger.TimeZone == nil ||
				*input.Trigger.TimeZone != timeZone || len(input.ConnectorIDs) != 1 ||
				input.ConnectorIDs[0] != sourceID {
				t.Fatalf("Preview() principal/input = %+v / %+v", principal, input)
			}
			return schedule.PreviewResult{
				AgentID: fixture.agentID, ReviewedProvider: "codex", Name: input.Name,
				Instructions: input.Instructions, PresetKey: input.PresetKey, Trigger: input.Trigger,
				Sources: []schedule.Source{{
					ConnectorID: sourceID, Provider: "codex", Source: "codex_app",
					NativeID: "gmail", ReviewedContractDigest: strings.Repeat("b", 64),
				}},
				Occurrences: []time.Time{firstOccurrence}, ReviewDigest: strings.Repeat("a", 64),
			}, nil
		},
	}
	body := scheduleDraftBody(t, fixture.agentID, []uuid.UUID{sourceID})
	response := serveBearerJSON(
		scheduleHTTPHandler(fixture, service),
		http.MethodPost,
		fixture.collectionPath()+"/preview",
		body,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
	var got struct {
		NormalizedTrigger scheduleCronTriggerResponse `json:"normalizedTrigger"`
		HumanSchedule     string                      `json:"humanSchedule"`
		NextOccurrences   []string                    `json:"nextOccurrences"`
		Provider          string                      `json:"provider"`
		ReviewDigest      string                      `json:"reviewDigest"`
		Sources           []scheduleSourceResponse    `json:"sources"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode Schedule preview: %v", err)
	}
	if got.Provider != "codex" || got.ReviewDigest != strings.Repeat("a", 64) ||
		got.NormalizedTrigger.Expression != expression ||
		got.HumanSchedule != "Every day at 08:00 · Asia/Ho_Chi_Minh" ||
		len(got.NextOccurrences) != 1 || got.NextOccurrences[0] != "2026-08-18T01:00:00Z" ||
		len(got.Sources) != 1 || got.Sources[0].ID != sourceID || got.Sources[0].Name != "gmail" {
		t.Fatalf("Schedule preview response = %+v", got)
	}
}

func TestHumanScheduleWordsCommonCronShapesAndFallsBackToTheExpression(t *testing.T) {
	t.Parallel()

	runAt := time.Date(2026, time.September, 29, 2, 0, 0, 0, time.UTC)
	cron := func(expression string) schedule.Trigger {
		timeZone := "Asia/Ho_Chi_Minh"
		return schedule.Trigger{
			Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &timeZone,
		}
	}
	for _, test := range []struct {
		trigger schedule.Trigger
		want    string
	}{
		{cron("0 9 * * *"), "Every day at 09:00 · Asia/Ho_Chi_Minh"},
		{cron("30 17 * * *"), "Every day at 17:30 · Asia/Ho_Chi_Minh"},
		{cron("0 9 * * 1-5"), "Every weekday at 09:00 · Asia/Ho_Chi_Minh"},
		{cron("0 9 * * 1"), "Every Monday at 09:00 · Asia/Ho_Chi_Minh"},
		{cron("0 9 * * 0,6"), "Every Sunday and Saturday at 09:00 · Asia/Ho_Chi_Minh"},
		{cron("0 9 * * 1,3,5"), "Every Monday, Wednesday and Friday at 09:00 · Asia/Ho_Chi_Minh"},
		{cron("0 9 1 * *"), "Every month on day 1 at 09:00 · Asia/Ho_Chi_Minh"},
		{cron("0 * * * *"), "Every hour · Asia/Ho_Chi_Minh"},
		{cron("15 * * * *"), "Every hour at :15 · Asia/Ho_Chi_Minh"},
		{cron("0 */2 * * *"), "Every 2 hours · Asia/Ho_Chi_Minh"},
		{cron("*/15 * * * *"), "Every 15 minutes · Asia/Ho_Chi_Minh"},
		{cron("*/5 * * * *"), "Every 5 minutes · Asia/Ho_Chi_Minh"},
		// Shapes with no plain wording keep the cron text.
		{cron("*/25 * * * *"), "*/25 * * * * · Asia/Ho_Chi_Minh"},
		{cron("0 9 * 1 *"), "0 9 * 1 * · Asia/Ho_Chi_Minh"},
		{cron("0 9 1 * 1"), "0 9 1 * 1 · Asia/Ho_Chi_Minh"},
		{cron("0 9 31 * *"), "0 9 31 * * · Asia/Ho_Chi_Minh"},
		{cron("0 9 * * 1,1"), "0 9 * * 1,1 · Asia/Ho_Chi_Minh"},
		{cron("0 9-17 * * *"), "0 9-17 * * * · Asia/Ho_Chi_Minh"},
		{cron("0,30 9 * * *"), "0,30 9 * * * · Asia/Ho_Chi_Minh"},
		{schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &runAt}, "Once at 2026-09-29T02:00:00Z"},
	} {
		if got := humanSchedule(test.trigger); got != test.want {
			t.Errorf("humanSchedule(%+v) = %q, want %q", test.trigger, got, test.want)
		}
	}
}

func TestScheduleCreateRouteForwardsReviewedDefinitionAndSupportsExactReplay(t *testing.T) {
	t.Parallel()

	fixture := newScheduleHandlerFixture()
	sourceID := uuid.Must(uuid.NewV7())
	scheduleID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	requestKey := uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.August, 17, 4, 5, 6, 123000000, time.UTC)
	created := true
	service := &scheduleServiceStub{
		create: func(
			_ context.Context,
			principal auth.UserPrincipal,
			input schedule.CreateInput,
		) (schedule.CreateResult, error) {
			if principal != fixture.principal || input.RequestKey != requestKey ||
				input.AgentID != fixture.agentID ||
				input.Status != schedule.StatusActive || input.ReviewDigest != strings.Repeat("a", 64) ||
				len(input.ConnectorIDs) != 1 || input.ConnectorIDs[0] != sourceID {
				t.Fatalf("Create() principal/input = %+v / %+v", principal, input)
			}
			return schedule.CreateResult{
				Created: created,
				Schedule: scheduleResponseFixture(
					fixture,
					scheduleID,
					conversationID,
					[]schedule.Source{
						{
							ConnectorID: sourceID,
							Provider:    "codex",
							Source:      "codex_app",
							NativeID:    "gmail",
						},
					},
					schedule.StatusActive,
					now,
				),
			}, nil
		},
	}
	body := scheduleCreateBody(t, fixture.agentID, requestKey, []uuid.UUID{sourceID})
	handler := scheduleHTTPHandler(fixture, service)

	response := serveBearerJSON(handler, http.MethodPost, fixture.collectionPath(), body)
	if response.Code != http.StatusCreated {
		t.Fatalf("created status = %d, want 201; body = %s", response.Code, response.Body.String())
	}
	assertScheduleFrontendShape(t, response, scheduleID, sourceID)

	created = false
	response = serveBearerJSON(handler, http.MethodPost, fixture.collectionPath(), body)
	if response.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
}

func TestScheduleListRouteForwardsStrictFiltersAndReturnsCursor(t *testing.T) {
	t.Parallel()

	fixture := newScheduleHandlerFixture()
	scheduleID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	requestCursorAt := time.Date(2026, time.August, 17, 3, 4, 5, 123456000, time.UTC)
	requestCursorID := uuid.Must(uuid.NewV7())
	requestCursor, err := formatScheduleCursor(requestCursorAt, requestCursorID)
	if err != nil {
		t.Fatal(err)
	}
	nextCursorAt := requestCursorAt.Add(-time.Minute)
	service := &scheduleServiceStub{
		list: func(
			_ context.Context,
			principal auth.UserPrincipal,
			options schedule.ListOptions,
		) (schedule.Page, error) {
			if principal != fixture.principal || options.Limit != 25 || options.Status == nil ||
				*options.Status != schedule.StatusActive || options.PresetKey == nil ||
				*options.PresetKey != "daily_brief_v1" || options.After == nil ||
				options.After.CreatedAt != requestCursorAt || options.After.ID != requestCursorID {
				t.Fatalf("List() principal/options = %+v / %+v", principal, options)
			}
			return schedule.Page{
				Schedules: []schedule.Response{scheduleResponseFixture(
					fixture, scheduleID, conversationID, nil, schedule.StatusActive, nextCursorAt,
				)},
				NextCursor: &schedule.ListCursor{CreatedAt: nextCursorAt, ID: scheduleID},
			}, nil
		},
	}
	path := fixture.collectionPath() + "?cursor=" + requestCursor +
		"&limit=25&preset_key=daily_brief_v1&status=active"
	response := serveBearerRequest(scheduleHTTPHandler(fixture, service), http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var got schedulePageResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode Schedule page: %v", err)
	}
	if len(got.Schedules) != 1 || got.Schedules[0].ID != scheduleID || got.NextCursor == nil {
		t.Fatalf("Schedule page = %+v", got)
	}
	decodedAt, decodedID, err := parseScheduleCursor(*got.NextCursor)
	if err != nil || decodedAt != nextCursorAt || decodedID != scheduleID {
		t.Fatalf("next cursor = %s => %s / %s / %v", *got.NextCursor, decodedAt, decodedID, err)
	}
}

func TestScheduleListRouteAppliesDefaultLimitAndNonNilEmptyPage(t *testing.T) {
	t.Parallel()

	fixture := newScheduleHandlerFixture()
	service := &scheduleServiceStub{
		list: func(
			_ context.Context,
			_ auth.UserPrincipal,
			options schedule.ListOptions,
		) (schedule.Page, error) {
			if options.Limit != 50 {
				t.Fatalf("List() limit = %d, want 50", options.Limit)
			}
			return schedule.Page{}, nil
		},
	}
	response := serveBearerRequest(
		scheduleHTTPHandler(fixture, service), http.MethodGet, fixture.collectionPath(), nil,
	)
	if response.Code != http.StatusOK || response.Body.String() != `{"schedules":[],"nextCursor":null}` {
		t.Fatalf("empty Schedule page = %d %s", response.Code, response.Body.String())
	}
}

func TestScheduleUpdateRouteAcceptsFrontendFullDefinition(t *testing.T) {
	t.Parallel()

	fixture := newScheduleHandlerFixture()
	scheduleID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	sourceID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.August, 17, 6, 7, 0, 0, time.UTC)
	service := &scheduleServiceStub{
		update: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotScheduleID uuid.UUID,
			input schedule.UpdateInput,
		) (schedule.MutationResult, error) {
			if principal != fixture.principal || gotScheduleID != scheduleID ||
				input.ExpectedRevision != 7 || input.AgentID == nil || *input.AgentID != fixture.agentID ||
				input.Name == nil || *input.Name != "Daily Brief" || input.Instructions == nil ||
				*input.Instructions != "Summarize the last 24 hours." || !input.PresetKey.Present ||
				input.PresetKey.Value == nil || *input.PresetKey.Value != "daily_brief_v1" ||
				input.Trigger == nil || input.Trigger.Expression == nil ||
				*input.Trigger.Expression != "0 8 * * *" || !input.Sources.Present ||
				len(input.Sources.ConnectorIDs) != 1 || input.Sources.ConnectorIDs[0] != sourceID ||
				input.ReviewDigest != strings.Repeat("d", 64) {
				t.Fatalf(
					"Update() principal/Schedule/input = %+v / %s / %+v",
					principal,
					gotScheduleID,
					input,
				)
			}
			return schedule.MutationResult{Schedule: scheduleResponseFixture(
				fixture,
				scheduleID,
				conversationID,
				[]schedule.Source{
					{
						ConnectorID: sourceID,
						Provider:    "codex",
						Source:      "codex_app",
						NativeID:    "gmail",
					},
				},
				schedule.StatusPaused,
				now,
			)}, nil
		},
	}
	var body map[string]any
	if err := json.Unmarshal(scheduleDraftBody(t, fixture.agentID, []uuid.UUID{sourceID}), &body); err != nil {
		t.Fatalf("decode Schedule update fixture: %v", err)
	}
	body["expectedRevision"] = 7
	body["reviewDigest"] = strings.Repeat("d", 64)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal Schedule update fixture: %v", err)
	}
	response := serveBearerJSON(
		scheduleHTTPHandler(fixture, service),
		http.MethodPatch,
		fixture.collectionPath()+"/"+scheduleID.String(),
		encoded,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var got scheduleResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode Schedule update response: %v", err)
	}
	if got.ID != scheduleID || got.Status != schedule.StatusPaused || len(got.Sources) != 1 {
		t.Fatalf("Schedule update response = %+v", got)
	}
}

func TestScheduleLifecycleAndRunNowRoutesForwardVersionedInputs(t *testing.T) {
	t.Parallel()

	fixture := newScheduleHandlerFixture()
	scheduleID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	firingID := uuid.Must(uuid.NewV7())
	messageID := uuid.Must(uuid.NewV7())
	runID := uuid.MustParse("00000000-0000-4000-8000-000000000042")
	requestKey := uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.August, 17, 5, 6, 0, 0, time.UTC)
	runStatus := "succeeded"
	output := "Everything important is summarized."
	service := &scheduleServiceStub{
		pause: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotScheduleID uuid.UUID,
			input schedule.RevisionInput,
		) (schedule.MutationResult, error) {
			if principal != fixture.principal || gotScheduleID != scheduleID ||
				input.ExpectedRevision != 2 {
				t.Fatalf("Pause() input = %+v / %s / %+v", principal, gotScheduleID, input)
			}
			return schedule.MutationResult{Schedule: scheduleResponseFixture(
				fixture, scheduleID, conversationID, nil, schedule.StatusPaused, now,
			)}, nil
		},
		resume: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotScheduleID uuid.UUID,
			input schedule.ReviewRevisionInput,
		) (schedule.MutationResult, error) {
			if principal != fixture.principal || gotScheduleID != scheduleID ||
				input.ExpectedRevision != 3 || input.ReviewDigest != strings.Repeat("c", 64) {
				t.Fatalf("Resume() input = %+v / %s / %+v", principal, gotScheduleID, input)
			}
			return schedule.MutationResult{Schedule: scheduleResponseFixture(
				fixture, scheduleID, conversationID, nil, schedule.StatusActive, now,
			)}, nil
		},
		cancel: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotScheduleID uuid.UUID,
			input schedule.RevisionInput,
		) (schedule.MutationResult, error) {
			if principal != fixture.principal || gotScheduleID != scheduleID ||
				input.ExpectedRevision != 4 {
				t.Fatalf("Cancel() input = %+v / %s / %+v", principal, gotScheduleID, input)
			}
			return schedule.MutationResult{Schedule: scheduleResponseFixture(
				fixture, scheduleID, conversationID, nil, schedule.StatusCancelled, now,
			)}, nil
		},
		runNow: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotScheduleID uuid.UUID,
			input schedule.RunNowInput,
		) (schedule.RunNowResult, error) {
			if principal != fixture.principal || gotScheduleID != scheduleID ||
				input.RequestKey != requestKey {
				t.Fatalf("RunNow() input = %+v / %s / %+v", principal, gotScheduleID, input)
			}
			return schedule.RunNowResult{Created: true, Firing: schedule.Firing{
				ID: firingID, CompanyID: fixture.companyID, ScheduleID: scheduleID,
				ConversationID: conversationID, MessageID: &messageID, RunID: &runID,
				OccurrenceAt: now, Trigger: schedule.FiringTriggerManual,
				Disposition: schedule.FiringDispositionMaterialized, CreatedAt: now,
			}}, nil
		},
		listFirings: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotScheduleID uuid.UUID,
			options schedule.FiringListOptions,
		) (schedule.FiringPage, error) {
			if principal != fixture.principal || gotScheduleID != scheduleID ||
				options.Limit != 1 || options.After != nil {
				t.Fatalf("ListFirings() input = %+v / %s / %+v", principal, gotScheduleID, options)
			}
			return schedule.FiringPage{Firings: []schedule.Firing{{
				ID: firingID, CompanyID: fixture.companyID, ScheduleID: scheduleID,
				ConversationID: conversationID, MessageID: &messageID, RunID: &runID,
				OccurrenceAt: now, Trigger: schedule.FiringTriggerManual,
				Disposition: schedule.FiringDispositionMaterialized, RunStatus: &runStatus,
				Output: &output, CreatedAt: now,
			}}}, nil
		},
	}
	handler := scheduleHTTPHandler(fixture, service)
	base := fixture.collectionPath() + "/" + scheduleID.String()
	requests := []struct {
		name string
		path string
		body string
		want schedule.Status
	}{
		{
			name: "pause",
			path: base + "/pause",
			body: `{"schemaVersion":1,"expectedRevision":2}`,
			want: schedule.StatusPaused,
		},
		{
			name: "resume",
			path: base + "/resume",
			body: `{"schemaVersion":1,"expectedRevision":3,"reviewDigest":"` + strings.Repeat(
				"c",
				64,
			) + `"}`,
			want: schedule.StatusActive,
		},
		{
			name: "cancel",
			path: base + "/cancel",
			body: `{"schemaVersion":1,"expectedRevision":4}`,
			want: schedule.StatusCancelled,
		},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			response := serveBearerJSON(handler, http.MethodPost, test.path, []byte(test.body))
			if response.Code != http.StatusOK {
				t.Fatalf(
					"%s status = %d, want 200; body = %s",
					test.name,
					response.Code,
					response.Body.String(),
				)
			}
			var got scheduleResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode %s response: %v", test.name, err)
			}
			if got.Status != test.want {
				t.Fatalf("%s status body = %s, want %s", test.name, got.Status, test.want)
			}
		})
	}

	runBody := []byte(`{"schemaVersion":1,"requestKey":"` + requestKey.String() + `"}`)
	response := serveBearerJSON(handler, http.MethodPost, base+"/runs", runBody)
	if response.Code != http.StatusCreated {
		t.Fatalf("Run now status = %d, want 201; body = %s", response.Code, response.Body.String())
	}
	for _, requiredNull := range []string{
		`"messageSeq":null`, `"reason":null`, `"runStatus":null`, `"output":null`,
	} {
		if !strings.Contains(response.Body.String(), requiredNull) {
			t.Fatalf(
				"Run now response omitted required nullable field %s: %s",
				requiredNull,
				response.Body.String(),
			)
		}
	}
	var got scheduleFiringResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode Run now response: %v", err)
	}
	if got.FiringID != firingID || got.RunID == nil || *got.RunID != runID ||
		got.MessageID == nil || *got.MessageID != messageID ||
		got.ConversationID != conversationID || got.Trigger != schedule.FiringTriggerManual {
		t.Fatalf("Run now response = %+v", got)
	}

	response = serveBearerRequest(handler, http.MethodGet, base+"/runs?limit=1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("Run history status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var history scheduleFiringPageResponse
	if err := json.Unmarshal(response.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode Run history response: %v", err)
	}
	if len(history.Runs) != 1 || history.Runs[0].FiringID != firingID ||
		history.Runs[0].RunStatus == nil || *history.Runs[0].RunStatus != runStatus ||
		history.Runs[0].Output == nil || *history.Runs[0].Output != output {
		t.Fatalf("Run history response = %+v", history)
	}
}

func TestScheduleRoutesRejectInvalidBodiesAndQueriesBeforeService(t *testing.T) {
	t.Parallel()

	fixture := newScheduleHandlerFixture()
	scheduleID := uuid.Must(uuid.NewV7())
	serviceCalls := 0
	service := &scheduleServiceStub{
		preview: func(context.Context, auth.UserPrincipal, schedule.PreviewInput) (schedule.PreviewResult, error) {
			serviceCalls++
			return schedule.PreviewResult{}, nil
		},
		list: func(context.Context, auth.UserPrincipal, schedule.ListOptions) (schedule.Page, error) {
			serviceCalls++
			return schedule.Page{}, nil
		},
		listFirings: func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.FiringListOptions) (schedule.FiringPage, error) {
			serviceCalls++
			return schedule.FiringPage{}, nil
		},
		resume: func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.ReviewRevisionInput) (schedule.MutationResult, error) {
			serviceCalls++
			return schedule.MutationResult{}, nil
		},
		runNow: func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.RunNowInput) (schedule.RunNowResult, error) {
			serviceCalls++
			return schedule.RunNowResult{}, nil
		},
	}
	handler := scheduleHTTPHandler(fixture, service)
	validDraft := string(scheduleDraftBody(t, fixture.agentID, nil))
	invalidBodies := []struct {
		name   string
		path   string
		body   string
		status int
		code   string
	}{
		{
			name:   "unknown field",
			path:   fixture.collectionPath() + "/preview",
			body:   strings.TrimSuffix(validDraft, "}") + `,"unknown":true}`,
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		{
			name:   "trailing JSON",
			path:   fixture.collectionPath() + "/preview",
			body:   validDraft + `{}`,
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		{
			name:   "invalid UTF-8",
			path:   fixture.collectionPath() + "/preview",
			body:   strings.Replace(validDraft, "Daily Brief", "Daily \xffBrief", 1),
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		{
			name:   "omitted sources",
			path:   fixture.collectionPath() + "/preview",
			body:   strings.Replace(validDraft, `,"sourceIds":[]`, "", 1),
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
		},
		{
			name: "invalid trigger union",
			path: fixture.collectionPath() + "/preview",
			body: strings.Replace(
				validDraft,
				`{"expression":"0 8 * * *","timeZone":"Asia/Ho_Chi_Minh","type":"cron"}`,
				`{"expression":"0 8 * * *","runAt":"2026-08-18T01:00:00Z","timeZone":"Asia/Ho_Chi_Minh","type":"cron"}`,
				1,
			),
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
		},
		{
			name:   "resume missing digest",
			path:   fixture.collectionPath() + "/" + scheduleID.String() + "/resume",
			body:   `{"schemaVersion":1,"expectedRevision":1}`,
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
		},
		{
			name:   "resume malformed digest",
			path:   fixture.collectionPath() + "/" + scheduleID.String() + "/resume",
			body:   `{"schemaVersion":1,"expectedRevision":1,"reviewDigest":"not-a-digest"}`,
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
		},
		{
			name:   "Run now malformed request key",
			path:   fixture.collectionPath() + "/" + scheduleID.String() + "/runs",
			body:   `{"schemaVersion":1,"requestKey":"not-a-uuid"}`,
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
		},
	}
	for _, test := range invalidBodies {
		t.Run(test.name, func(t *testing.T) {
			response := serveBearerJSON(handler, http.MethodPost, test.path, []byte(test.body))
			assertErrorResponse(t, response, test.status, test.code)
		})
	}

	invalidQueries := []string{
		"?unknown=true",
		"?limit=25&limit=26",
		"?limit=",
		"?limit=025",
		"?limit=0",
		"?limit=101",
		"?status=Active",
		"?status=unknown",
	}
	for _, query := range invalidQueries {
		response := serveBearerRequest(handler, http.MethodGet, fixture.collectionPath()+query, nil)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
	for _, query := range []string{"?unknown=true", "?limit=0"} {
		response := serveBearerRequest(
			handler,
			http.MethodGet,
			fixture.collectionPath()+"/"+scheduleID.String()+"/runs"+query,
			nil,
		)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
	// A malformed cursor is its own answer, never folded into
	// validation_failed: the client restarts pagination instead of
	// re-checking its filters.
	for _, cursor := range []string{"not-a-cursor", "AAAA", "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		for _, path := range []string{
			fixture.collectionPath(),
			fixture.collectionPath() + "/" + scheduleID.String() + "/runs",
		} {
			response := serveBearerRequest(handler, http.MethodGet, path+"?cursor="+cursor, nil)
			assertErrorResponse(t, response, http.StatusUnprocessableEntity, "invalid_cursor")
		}
	}
	if serviceCalls != 0 {
		t.Fatalf("Schedule service calls for rejected requests = %d, want 0", serviceCalls)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		fixture.collectionPath()+"/preview",
		strings.NewReader(validDraft),
	)
	request.Header.Set("Authorization", "Bearer access-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusUnsupportedMediaType, "unsupported_media_type")
}

func TestScheduleRoutesRequireCurrentOwnerAndKeepResourcesNonEnumerable(t *testing.T) {
	t.Parallel()

	fixture := newScheduleHandlerFixture()
	scheduleID := uuid.Must(uuid.NewV7())
	path := fixture.collectionPath() + "/" + scheduleID.String()
	serviceCalls := 0
	service := &scheduleServiceStub{
		get: func(context.Context, auth.UserPrincipal, uuid.UUID) (schedule.Response, error) {
			serviceCalls++
			return schedule.Response{}, schedule.ErrNotFound
		},
		listFirings: func(
			context.Context, auth.UserPrincipal, uuid.UUID, schedule.FiringListOptions,
		) (schedule.FiringPage, error) {
			return schedule.FiringPage{}, schedule.ErrNotFound
		},
	}

	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	scheduleHTTPHandler(fixture, service).ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusUnauthorized, "authentication_required")

	// Firing history is a per-Schedule read: a Schedule the service cannot
	// resolve for this creator is 404, not an empty page.
	response = serveBearerRequest(scheduleHTTPHandler(fixture, service), http.MethodGet, path+"/runs", nil)
	assertErrorResponse(t, response, http.StatusNotFound, "schedule_not_found")

	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "current non-owner", err: company.ErrForbidden, status: http.StatusForbidden, code: "company_permission_required"},
		{name: "revoked or cross-Company owner", err: company.ErrNotFound, status: http.StatusNotFound, code: "company_not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := New(Handler{
				AuthService: authenticatedAuthStub(fixture.userID),
				CompanyService: &companyServiceStub{
					requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
						return auth.UserPrincipal{}, test.err
					},
				},
				ScheduleService: service,
			})
			assertErrorResponse(
				t,
				serveBearerRequest(handler, http.MethodGet, path, nil),
				test.status,
				test.code,
			)
		})
	}
	if serviceCalls != 0 {
		t.Fatalf("Schedule Get() calls before owner authorization = %d, want 0", serviceCalls)
	}

	response = serveBearerRequest(scheduleHTTPHandler(fixture, service), http.MethodGet, path, nil)
	assertErrorResponse(t, response, http.StatusNotFound, "schedule_not_found")
	if serviceCalls != 1 {
		t.Fatalf("Schedule Get() calls = %d, want 1", serviceCalls)
	}

	response = serveBearerRequest(
		scheduleHTTPHandler(fixture, service),
		http.MethodGet,
		fixture.collectionPath()+"/not-a-uuid",
		nil,
	)
	assertErrorResponse(t, response, http.StatusNotFound, "schedule_not_found")
	if serviceCalls != 1 {
		t.Fatalf("Schedule Get() called for malformed ID; calls = %d", serviceCalls)
	}
}

func TestScheduleRunNowRouteMapsActiveConflictAndRedactsUnexpectedFailures(t *testing.T) {
	t.Parallel()

	fixture := newScheduleHandlerFixture()
	scheduleID := uuid.Must(uuid.NewV7())
	body := []byte(`{"schemaVersion":1,"requestKey":"` + uuid.Must(uuid.NewV7()).String() + `"}`)
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "active Run", err: schedule.ErrRunActive, status: http.StatusConflict, code: "schedule_run_active"},
		{name: "creator binding removed", err: schedule.ErrAgentBindingRequired, status: http.StatusConflict, code: "agent_binding_required"},
		{name: "Agent archived before the drift pass", err: schedule.ErrConflict, status: http.StatusConflict, code: "schedule_conflict"},
		{name: "private missing Schedule", err: schedule.ErrNotFound, status: http.StatusNotFound, code: "schedule_not_found"},
		{name: "unexpected failure", err: errors.New("password=do-not-return"), status: http.StatusInternalServerError, code: "schedule_request_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &scheduleServiceStub{
				runNow: func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.RunNowInput) (schedule.RunNowResult, error) {
					return schedule.RunNowResult{}, test.err
				},
			}
			response := serveBearerJSON(
				scheduleHTTPHandler(fixture, service),
				http.MethodPost,
				fixture.collectionPath()+"/"+scheduleID.String()+"/runs",
				body,
			)
			assertErrorResponse(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "password=do-not-return") {
				t.Fatalf("Schedule error response leaked internal detail: %s", response.Body.String())
			}
		})
	}
}

func TestScheduleErrorMapperGolden(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		err    error
		status int
		detail string
		code   string
	}{
		{name: "unauthenticated", err: schedule.ErrUnauthenticated, status: http.StatusUnauthorized, detail: "authentication is required", code: "authentication_required"},
		{name: "forbidden", err: schedule.ErrForbidden, status: http.StatusForbidden, detail: "Your Company role does not allow this action", code: "company_permission_required"},
		{name: "invalid input", err: schedule.ErrInvalidInput, status: http.StatusUnprocessableEntity, detail: "request validation failed", code: "validation_failed"},
		{name: "not found", err: schedule.ErrNotFound, status: http.StatusNotFound, detail: "Schedule not found", code: "schedule_not_found"},
		{name: "version conflict", err: schedule.ErrVersionConflict, status: http.StatusConflict, detail: "Schedule changed in another session", code: "schedule_version_conflict"},
		{name: "review stale", err: schedule.ErrReviewStale, status: http.StatusConflict, detail: "Schedule review is stale", code: "schedule_review_stale"},
		{
			name: "selection unavailable", err: schedule.ErrSelectionUnavailable,
			status: http.StatusConflict, detail: "Selected Agent or sources are unavailable",
			code: "schedule_conflict",
		},
		{
			name: "creator binding required", err: schedule.ErrAgentBindingRequired,
			status: http.StatusConflict, detail: "set this Agent up on one of your computers first",
			code: "agent_binding_required",
		},
		{name: "Run active", err: schedule.ErrRunActive, status: http.StatusConflict, detail: "Schedule already has an active Run", code: "schedule_run_active"},
		{name: "terminal", err: schedule.ErrTerminal, status: http.StatusConflict, detail: "Schedule is terminal", code: "schedule_terminal"},
		{name: "invalid state", err: schedule.ErrInvalidState, status: http.StatusConflict, detail: "Schedule state does not allow this operation", code: "schedule_invalid_state"},
		{name: "conflict", err: schedule.ErrConflict, status: http.StatusConflict, detail: "Schedule conflicts with current state", code: "schedule_conflict"},
		{name: "invalid cursor", err: errInvalidScheduleCursor, status: http.StatusUnprocessableEntity, detail: "cursor is invalid", code: "invalid_cursor"},
		{name: "fallback", err: errors.New("unmapped schedule error"), status: http.StatusInternalServerError, detail: "could not access Schedules", code: "schedule_request_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertMappedErrorResponse(t, func(c *gin.Context) {
				writeScheduleError(c, errors.Join(errors.New("wrapped"), test.err))
			}, test.status, test.detail, test.code)
		})
	}
}

type scheduleServiceStub struct {
	preview     func(context.Context, auth.UserPrincipal, schedule.PreviewInput) (schedule.PreviewResult, error)
	create      func(context.Context, auth.UserPrincipal, schedule.CreateInput) (schedule.CreateResult, error)
	get         func(context.Context, auth.UserPrincipal, uuid.UUID) (schedule.Response, error)
	list        func(context.Context, auth.UserPrincipal, schedule.ListOptions) (schedule.Page, error)
	update      func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.UpdateInput) (schedule.MutationResult, error)
	pause       func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.RevisionInput) (schedule.MutationResult, error)
	resume      func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.ReviewRevisionInput) (schedule.MutationResult, error)
	cancel      func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.RevisionInput) (schedule.MutationResult, error)
	runNow      func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.RunNowInput) (schedule.RunNowResult, error)
	listFirings func(context.Context, auth.UserPrincipal, uuid.UUID, schedule.FiringListOptions) (schedule.FiringPage, error)
}

func (s *scheduleServiceStub) Preview(
	ctx context.Context,
	principal auth.UserPrincipal,
	input schedule.PreviewInput,
) (schedule.PreviewResult, error) {
	if s.preview == nil {
		return schedule.PreviewResult{}, errors.New("unexpected schedule preview call")
	}
	return s.preview(ctx, principal, input)
}

func (s *scheduleServiceStub) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	input schedule.CreateInput,
) (schedule.CreateResult, error) {
	if s.create == nil {
		return schedule.CreateResult{}, errors.New("unexpected schedule create call")
	}
	return s.create(ctx, principal, input)
}

func (s *scheduleServiceStub) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
) (schedule.Response, error) {
	if s.get == nil {
		return schedule.Response{}, errors.New("unexpected schedule get call")
	}
	return s.get(ctx, principal, scheduleID)
}

func (s *scheduleServiceStub) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options schedule.ListOptions,
) (schedule.Page, error) {
	if s.list == nil {
		return schedule.Page{}, errors.New("unexpected schedule list call")
	}
	return s.list(ctx, principal, options)
}

func (s *scheduleServiceStub) Update(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input schedule.UpdateInput,
) (schedule.MutationResult, error) {
	if s.update == nil {
		return schedule.MutationResult{}, errors.New("unexpected schedule update call")
	}
	return s.update(ctx, principal, scheduleID, input)
}

func (s *scheduleServiceStub) Pause(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input schedule.RevisionInput,
) (schedule.MutationResult, error) {
	if s.pause == nil {
		return schedule.MutationResult{}, errors.New("unexpected schedule pause call")
	}
	return s.pause(ctx, principal, scheduleID, input)
}

func (s *scheduleServiceStub) Resume(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input schedule.ReviewRevisionInput,
) (schedule.MutationResult, error) {
	if s.resume == nil {
		return schedule.MutationResult{}, errors.New("unexpected schedule resume call")
	}
	return s.resume(ctx, principal, scheduleID, input)
}

func (s *scheduleServiceStub) Cancel(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input schedule.RevisionInput,
) (schedule.MutationResult, error) {
	if s.cancel == nil {
		return schedule.MutationResult{}, errors.New("unexpected schedule cancel call")
	}
	return s.cancel(ctx, principal, scheduleID, input)
}

func (s *scheduleServiceStub) RunNow(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	input schedule.RunNowInput,
) (schedule.RunNowResult, error) {
	if s.runNow == nil {
		return schedule.RunNowResult{}, errors.New("unexpected schedule run-now call")
	}
	return s.runNow(ctx, principal, scheduleID, input)
}

func (s *scheduleServiceStub) ListFirings(
	ctx context.Context,
	principal auth.UserPrincipal,
	scheduleID uuid.UUID,
	options schedule.FiringListOptions,
) (schedule.FiringPage, error) {
	if s.listFirings == nil {
		return schedule.FiringPage{}, errors.New("unexpected schedule list-firings call")
	}
	return s.listFirings(ctx, principal, scheduleID, options)
}

type scheduleHandlerFixture struct {
	userID    uuid.UUID
	companyID uuid.UUID
	agentID   uuid.UUID
	principal auth.UserPrincipal
}

func newScheduleHandlerFixture() scheduleHandlerFixture {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	return scheduleHandlerFixture{
		userID: userID, companyID: companyID, agentID: uuid.Must(uuid.NewV7()),
		principal: auth.UserPrincipal{
			UserID: userID, SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
			AuthKind:  auth.TokenKindBearer,
			CompanyID: companyID, Role: auth.CompanyRoleOwner,
		},
	}
}

func (f scheduleHandlerFixture) collectionPath() string {
	return "/api/v1/companies/" + f.companyID.String() + "/schedules"
}

func scheduleHTTPHandler(fixture scheduleHandlerFixture, service ScheduleService) http.Handler {
	return New(Handler{
		AuthService: authenticatedAuthStub(fixture.userID),
		CompanyService: &companyServiceStub{
			requirePermission: func(
				_ context.Context,
				principal auth.UserPrincipal,
				companyID uuid.UUID,
			) (auth.UserPrincipal, error) {
				if principal.UserID != fixture.userID || companyID != fixture.companyID {
					return auth.UserPrincipal{}, company.ErrNotFound
				}
				return fixture.principal, nil
			},
		},
		ScheduleService: service,
	})
}

func scheduleDraftBody(t *testing.T, agentID uuid.UUID, sourceIDs []uuid.UUID) []byte {
	t.Helper()
	if sourceIDs == nil {
		sourceIDs = []uuid.UUID{}
	}
	body, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"agentId":       agentID,
		"name":          "Daily Brief",
		"instructions":  "Summarize the last 24 hours.",
		"presetKey":     "daily_brief_v1",
		"trigger": map[string]any{
			"type": "cron", "expression": "0 8 * * *", "timeZone": "Asia/Ho_Chi_Minh",
		},
		"sourceIds": sourceIDs,
	})
	if err != nil {
		t.Fatalf("marshal Schedule draft: %v", err)
	}
	return body
}

func scheduleCreateBody(
	t *testing.T,
	agentID, requestKey uuid.UUID,
	sourceIDs []uuid.UUID,
) []byte {
	t.Helper()
	var draft map[string]any
	if err := json.Unmarshal(scheduleDraftBody(t, agentID, sourceIDs), &draft); err != nil {
		t.Fatalf("decode Schedule draft fixture: %v", err)
	}
	draft["requestKey"] = requestKey
	draft["reviewDigest"] = strings.Repeat("a", 64)
	body, err := json.Marshal(draft)
	if err != nil {
		t.Fatalf("marshal Schedule create: %v", err)
	}
	return body
}

func scheduleResponseFixture(
	fixture scheduleHandlerFixture,
	scheduleID, conversationID uuid.UUID,
	sources []schedule.Source,
	status schedule.Status,
	now time.Time,
) schedule.Response {
	expression := "0 8 * * *"
	timeZone := "Asia/Ho_Chi_Minh"
	next := now.Add(time.Hour)
	preset := "daily_brief_v1"
	return schedule.Response{
		ID: scheduleID, CompanyID: fixture.companyID, ConversationID: conversationID,
		CreatorUserID: fixture.userID, AgentID: fixture.agentID, ReviewedProvider: "codex",
		Name: "Daily Brief", Instructions: "Summarize the last 24 hours.", PresetKey: &preset,
		Trigger: schedule.Trigger{
			Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &timeZone,
		},
		Sources: sources, NextOccurrenceAt: &next, Status: status,
		CreatedAt: now, UpdatedAt: now, Revision: 2,
	}
}

func assertScheduleFrontendShape(
	t *testing.T,
	response *httptest.ResponseRecorder,
	scheduleID, sourceID uuid.UUID,
) {
	t.Helper()
	var got scheduleResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode Schedule response: %v", err)
	}
	if got.ID != scheduleID || got.Provider != "codex" || got.NextRunAt == nil ||
		len(got.Sources) != 1 || got.Sources[0].ID != sourceID ||
		got.Sources[0].Name != "gmail" || got.Status != schedule.StatusActive {
		t.Fatalf("Schedule response = %+v", got)
	}
	for _, forbidden := range []string{"companyId", "creatorUserId", "conversationId"} {
		if strings.Contains(response.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("founder-private Schedule response exposes %s: %s", forbidden, response.Body.String())
		}
	}
}

var _ ScheduleService = (*scheduleServiceStub)(nil)
