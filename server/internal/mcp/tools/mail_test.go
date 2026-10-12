package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

type mailAPIStub struct {
	paths       []string
	requestKeys []string
	requests    []map[string]json.RawMessage
}

func (s *mailAPIStub) PostRunJSON(
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
	response := destination.(*createMailDraftsResponse)
	response.ID = "group-id"
	response.RecipientCount = 1
	response.Drafts = append(response.Drafts, struct {
		ID string `json:"id"`
	}{ID: "draft-id"})
	return nil
}

func TestCreateMailDraftsUsesRunProofAndDeterministicRequestKey(t *testing.T) {
	t.Parallel()
	api := new(mailAPIStub)
	runContext := RunContext{
		CompanyID:      "01990ad8-0bb0-7de0-acfa-9f2821fc1a52",
		ConversationID: "01990ad8-0bb0-7de0-acfa-9f2821fc1a53",
		RunID:          "00000000-0000-4000-8000-000000000071",
		AgentID:        "01990ad8-0bb0-7de0-acfa-9f2821fc1a54",
	}
	service, err := NewMailService(api, runContext)
	if err != nil {
		t.Fatal(err)
	}
	input := CreateMailDraftsInput{Drafts: []CreateMailDraftInput{{
		Subject: "Release",
		Text:    "The release is ready.",
		Deliveries: []CreateMailDraftDeliveryInput{{
			To: []string{"customer@example.com"},
		}},
	}}}
	for range 2 {
		result, callErr := service.CreateMailDrafts(context.Background(), input)
		if callErr != nil {
			t.Fatal(callErr)
		}
		if result.GroupID != "group-id" || result.Drafts != 1 || result.RecipientCount != 1 {
			t.Fatalf("CreateMailDrafts() = %+v", result)
		}
	}

	wantPath := "/companies/01990ad8-0bb0-7de0-acfa-9f2821fc1a52/conversations/" +
		"01990ad8-0bb0-7de0-acfa-9f2821fc1a53/mail-draft-groups"
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
	request := api.requests[0]
	if len(request) != 1 || request["drafts"] == nil {
		t.Fatalf("request = %s", mustMarshalJSON(t, request))
	}
	for _, retiredField := range []string{"runId", "agentId", "claimId"} {
		if _, exists := request[retiredField]; exists {
			t.Fatalf("request contains authority field %s: %s", retiredField, mustMarshalJSON(t, request))
		}
	}
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
