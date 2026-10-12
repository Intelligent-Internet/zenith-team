package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type taskResultsAPIStub struct {
	path, key string
	calls     int
	request   map[string]json.RawMessage
}

func (s *taskResultsAPIStub) PostRunJSON(_ context.Context, path, key string, in, out any) error {
	s.path = path
	if s.key != "" && s.key != key {
		return errors.New("retry changed request key")
	}
	s.key = key
	s.calls++
	body, _ := json.Marshal(in)
	_ = json.Unmarshal(body, &s.request)
	*out.(*FinalizeTaskResultsResult) = FinalizeTaskResultsResult{Status: "in_progress", Revision: 2}
	return nil
}

func TestFinalizeTaskResultsUsesRunTaskAndStableReplay(t *testing.T) {
	t.Parallel()
	api := &taskResultsAPIStub{}
	run := RunContext{
		CompanyID:      uuid.NewString(),
		ConversationID: uuid.NewString(),
		RunID:          uuid.NewString(),
		AgentID:        uuid.NewString(),
		TaskID:         uuid.NewString(),
	}
	service, err := NewTaskResultToolService(api, run)
	if err != nil {
		t.Fatal(err)
	}
	in := FinalizeTaskResultsInput{ExpectedRevision: 1, OutputIDs: []string{uuid.NewString(), uuid.NewString()}}
	for range 2 {
		if _, err = service.FinalizeTaskResults(t.Context(), in); err != nil {
			t.Fatal(err)
		}
	}
	if api.path != "/companies/"+run.CompanyID+"/tasks/"+run.TaskID+"/results" || api.calls != 2 ||
		len(api.request) != 2 ||
		api.request["outputIds"] == nil ||
		api.request["expectedRevision"] == nil {
		t.Fatalf("request = %+v", api)
	}
	if _, err = uuid.Parse(api.key); err != nil {
		t.Fatal(err)
	}
	in.OutputIDs = append(in.OutputIDs, in.OutputIDs[0])
	if _, err = service.FinalizeTaskResults(t.Context(), in); err == nil || api.calls != 2 {
		t.Fatal("duplicate selection reached API")
	}
}
