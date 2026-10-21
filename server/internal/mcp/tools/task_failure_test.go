package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

type taskFailureAPIStub struct {
	paths       []string
	requestKeys []string
	requests    []map[string]json.RawMessage
	status      string
}

func (s *taskFailureAPIStub) PostRunJSON(
	_ context.Context,
	path, requestKey string,
	source, destination any,
) error {
	encoded, err := json.Marshal(source)
	if err != nil {
		return err
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &request); err != nil {
		return err
	}
	s.paths = append(s.paths, path)
	s.requestKeys = append(s.requestKeys, requestKey)
	s.requests = append(s.requests, request)
	destination.(*MarkTaskFailedResult).Status = s.status
	return nil
}

func TestMarkTaskFailedUsesRunProofAndReasonOnlyRequest(t *testing.T) {
	t.Parallel()
	api := &taskFailureAPIStub{status: "failed"}
	service, err := NewTaskFailureService(api, RunContext{
		CompanyID:      "01990ad8-0bb0-7de0-acfa-9f2821fc1a52",
		ConversationID: "01990ad8-0bb0-7de0-acfa-9f2821fc1a53",
		RunID:          "00000000-0000-4000-8000-000000000071",
		AgentID:        "01990ad8-0bb0-7de0-acfa-9f2821fc1a54",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, callErr := service.MarkTaskFailed(
			context.Background(),
			MarkTaskFailedInput{Reason: " Contract forbids delivery "},
		)
		if callErr != nil {
			t.Fatal(callErr)
		}
		if result.Status != "failed" {
			t.Fatalf("MarkTaskFailed() = %+v", result)
		}
	}

	wantPath := "/companies/01990ad8-0bb0-7de0-acfa-9f2821fc1a52/task-failures"
	if len(api.paths) != 2 || api.paths[0] != wantPath || api.paths[1] != wantPath {
		t.Fatalf("paths = %v, want %q twice", api.paths, wantPath)
	}
	if api.requestKeys[0] != api.requestKeys[1] {
		t.Fatalf("request keys = %v, want retry convergence", api.requestKeys)
	}
	requestKey, err := uuid.Parse(api.requestKeys[0])
	if err != nil || requestKey.Version() != 5 {
		t.Fatalf("request key = %q, want deterministic UUID v5", api.requestKeys[0])
	}
	if len(api.requests[0]) != 1 ||
		string(api.requests[0]["reason"]) != `"Contract forbids delivery"` {
		t.Fatalf("request = %s", mustMarshalJSON(t, api.requests[0]))
	}
}

func TestMarkTaskFailedRejectsUnexpectedCloudStatus(t *testing.T) {
	t.Parallel()
	api := &taskFailureAPIStub{status: "running"}
	service, err := NewTaskFailureService(api, RunContext{
		CompanyID:      "company",
		ConversationID: "conversation",
		RunID:          "00000000-0000-4000-8000-000000000071",
		AgentID:        "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.MarkTaskFailed(
		context.Background(),
		MarkTaskFailedInput{Reason: "Blocked"},
	); err == nil {
		t.Fatal("MarkTaskFailed() error = nil, want invalid response")
	}
}
