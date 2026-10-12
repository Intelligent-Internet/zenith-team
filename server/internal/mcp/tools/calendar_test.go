package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type calendarAPIStub struct {
	paths, keys []string
	bodies      []string
}

func (s *calendarAPIStub) FetchRunJSON(_ context.Context, path string, destination any) error {
	s.paths = append(s.paths, path)
	return json.Unmarshal([]byte(`{"catalog":{"calendars":[],"members":[]},"items":[]}`), destination)
}
func (s *calendarAPIStub) PostRunJSON(_ context.Context, path, key string, source, destination any) error {
	b, err := json.Marshal(source)
	if err != nil {
		return err
	}
	s.paths = append(s.paths, path)
	s.keys = append(s.keys, key)
	s.bodies = append(s.bodies, string(b))
	return json.Unmarshal([]byte(`{"event":null}`), destination)
}
func TestCalendarToolsBindRunAndConvergeRetries(t *testing.T) {
	t.Parallel()
	api := new(calendarAPIStub)
	service, err := NewCalendarToolService(api, RunContext{CompanyID: "company", ConversationID: "conversation", RunID: "run", AgentID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ReadCalendar(context.Background(), ReadCalendarInput{From: "2026-09-22T00:00:00+07:00", To: "2026-09-23T00:00:00+07:00"}); err != nil {
		t.Fatal(err)
	}
	input := WriteCalendarInput{Action: "delete", EventID: "event", ExpectedRevision: 2}
	for range 2 {
		if _, err = service.WriteCalendar(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	if api.keys[0] != api.keys[1] {
		t.Fatal("retries must reuse the request key")
	}
	if api.paths[1] != "/companies/company/conversations/conversation/calendar" {
		t.Fatalf("path = %q", api.paths[1])
	}
	var body map[string]any
	if err = json.Unmarshal([]byte(api.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"userId", "runId", "agentId", "companyId"} {
		if _, ok := body[key]; ok {
			t.Fatalf("model body includes authority %s", key)
		}
	}
}

func TestCalendarToolPreservesCustomRecurrence(t *testing.T) {
	t.Parallel()
	api := new(calendarAPIStub)
	service, err := NewCalendarToolService(api, RunContext{CompanyID: "company", ConversationID: "conversation", RunID: "run", AgentID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	count := int32(13)
	input := WriteCalendarInput{Action: "create", Input: &CalendarEventInput{Recurrence: "weekly", RecurrenceInterval: 2, RecurrenceWeekdays: []int32{1, 3}, RecurrenceMonthlyMode: "day_of_month", RecurrenceCount: &count}}
	if _, err = service.WriteCalendar(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	var got WriteCalendarInput
	if err = json.Unmarshal([]byte(api.bodies[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Input == nil || got.Input.RecurrenceInterval != 2 || len(got.Input.RecurrenceWeekdays) != 2 || got.Input.RecurrenceCount == nil || *got.Input.RecurrenceCount != 13 {
		t.Fatalf("custom rule lost: %+v", got.Input)
	}
}
