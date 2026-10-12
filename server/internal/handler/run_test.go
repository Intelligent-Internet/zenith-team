package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
)

// Malformed and numeric Run IDs are indistinguishable from missing Runs.
func TestRunRequestAnswers404ForRunIDsThatCannotExist(t *testing.T) {
	t.Parallel()

	for _, runID := range []string{
		"not-a-number",
		"4711",
		"0",
		"-1",
		"007",
		"1.0",
		" 1",
		"",
	} {
		t.Run(runID, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			c.Params = gin.Params{{Key: "runId", Value: runID}}

			if _, _, ok := runRequest(c); ok {
				t.Fatalf("runRequest() accepted runId %q", runID)
			}
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", recorder.Code)
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body %s: %v", recorder.Body.Bytes(), err)
			}
			if body.Code != "run_not_found" {
				t.Fatalf("error code = %q, want run_not_found", body.Code)
			}
		})
	}
}

func TestRunRequestAcceptsAUUID(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Params = gin.Params{{Key: "runId", Value: "00000000-0000-4000-8000-000000004711"}}
	want := auth.UserPrincipal{
		UserID:    uuid.Must(uuid.NewV7()),
		SessionID: uuid.Must(uuid.NewV7()),
		AuthKind:  auth.TokenKindBearer,
	}
	c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), want))

	principal, runID, ok := runRequest(c)
	if !ok || runID != uuid.MustParse("00000000-0000-4000-8000-000000004711") || principal != want {
		t.Fatalf("runRequest() = %+v, %d, %t", principal, runID, ok)
	}
}

func TestParseRunListQuery(t *testing.T) {
	t.Parallel()

	taskID := uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e401")
	conversationID := uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e402")
	agentID := uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e403")
	runtimeID := uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e404")
	cursor := runListCursor{
		CreatedAt: time.Date(2026, time.August, 17, 8, 9, 10, 123456000, time.UTC),
		ID:        uuid.MustParse("00000000-0000-4000-8000-000000004711"),
	}
	tests := []struct {
		name               string
		rawQuery           string
		wantLimit          int32
		wantAfter          *runListCursor
		wantStatus         []string
		wantActive         bool
		wantTaskID         *uuid.UUID
		wantConversationID *uuid.UUID
		wantAgentID        *uuid.UUID
		wantRuntimeID      *uuid.UUID
		wantErr            error
	}{
		{name: "defaults", wantLimit: 50},
		{
			name:      "bounded page after cursor",
			rawQuery:  "limit=2&cursor=" + encodeRunCursor(cursor.CreatedAt, cursor.ID),
			wantLimit: 2,
			wantAfter: &cursor,
		},
		{
			name: "all filters",
			rawQuery: "status=queued%2Crunning&task_id=" + taskID.String() +
				"&conversation_id=" + conversationID.String() + "&agent_id=" + agentID.String() +
				"&runtime_id=" + runtimeID.String(),
			wantLimit:          50,
			wantStatus:         []string{"queued", "running"},
			wantTaskID:         &taskID,
			wantConversationID: &conversationID,
			wantAgentID:        &agentID,
			wantRuntimeID:      &runtimeID,
		},
		{name: "active runs", rawQuery: "active=true", wantLimit: 50, wantActive: true},
		{name: "noncanonical limit", rawQuery: "limit=02", wantErr: errInvalidRunListInput},
		{name: "foreign parameter", rawQuery: "offset=2", wantErr: errInvalidRunListInput},
		{name: "camel case filter", rawQuery: "taskId=" + taskID.String(), wantErr: errInvalidRunListInput},
		{name: "repeated status", rawQuery: "status=queued&status=running", wantErr: errInvalidRunListInput},
		{name: "empty status member", rawQuery: "status=queued%2C", wantErr: errInvalidRunListInput},
		{name: "duplicate status member", rawQuery: "status=queued%2Cqueued", wantErr: errInvalidRunListInput},
		{name: "unknown status", rawQuery: "status=teleported", wantErr: errInvalidRunListInput},
		{
			name:     "status with active true",
			rawQuery: "status=queued&active=true",
			wantErr:  errInvalidRunListInput,
		},
		{
			name:     "status with active false",
			rawQuery: "status=queued&active=false",
			wantErr:  errInvalidRunListInput,
		},
		{name: "noncanonical active", rawQuery: "active=True", wantErr: errInvalidRunListInput},
		{name: "invalid task id", rawQuery: "task_id=not-a-uuid", wantErr: errInvalidRunListInput},
		{name: "invalid cursor", rawQuery: "cursor=garbage", wantErr: errInvalidRunListCursor},
		{
			// A uuid-keyed feed's cursor is not a run position: the run feed
			// must reject it rather than decode 16 bytes of uuid into an id.
			name: "cursor from a uuid-keyed feed",
			rawQuery: "cursor=" + encodeFeedCursor(
				cursor.CreatedAt, uuid.MustParse("0198b3c4-b0d5-7e62-8f19-3ac75d09e4b2"),
			),
			wantErr: errInvalidRunListCursor,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseRunListQuery(runQueryContext(test.rawQuery))
			if !errorIs(err, test.wantErr) {
				t.Fatalf("parseRunListQuery() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				return
			}
			if got.limit != test.wantLimit {
				t.Fatalf("limit = %d, want %d", got.limit, test.wantLimit)
			}
			if !equalStrings(got.statuses, test.wantStatus) || got.activeOnly != test.wantActive ||
				!equalUUIDPointers(got.taskID, test.wantTaskID) ||
				!equalUUIDPointers(got.conversationID, test.wantConversationID) ||
				!equalUUIDPointers(got.agentID, test.wantAgentID) ||
				!equalUUIDPointers(got.runtimeID, test.wantRuntimeID) {
				t.Fatalf(
					"filters = %+v, want status=%v active=%t task=%v conversation=%v agent=%v runtime=%v",
					got,
					test.wantStatus,
					test.wantActive,
					test.wantTaskID,
					test.wantConversationID,
					test.wantAgentID,
					test.wantRuntimeID,
				)
			}
			if test.wantAfter == nil {
				if got.before != nil {
					t.Fatalf("before = %+v, want nil", got.before)
				}
				return
			}
			if got.before == nil || got.before.ID != test.wantAfter.ID ||
				!got.before.CreatedAt.Equal(test.wantAfter.CreatedAt) {
				t.Fatalf("before = %+v, want %+v", got.before, test.wantAfter)
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func equalUUIDPointers(got, want *uuid.UUID) bool {
	return (got == nil && want == nil) || (got != nil && want != nil && *got == *want)
}

func runQueryContext(rawQuery string) *gin.Context {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	path := "/"
	if rawQuery != "" {
		path += "?" + rawQuery
	}
	context.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return context
}

func errorIs(got, want error) bool {
	return errors.Is(got, want)
}
