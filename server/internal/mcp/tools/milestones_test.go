package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type milestoneAPIStub struct {
	path      string
	runID     string
	body      any
	err       error
	attested  []string
	callCount int
}

func (s *milestoneAPIStub) PostRunJSON(
	_ context.Context,
	path string,
	runID string,
	body any,
	destination any,
) error {
	s.callCount++
	s.path, s.runID, s.body = path, runID, body
	if s.err != nil {
		return s.err
	}
	if response, ok := destination.(*attestMilestonesResponse); ok {
		response.Attested = s.attested
	}
	return nil
}

func milestoneToolFixture(t *testing.T, api MilestoneAPI) *DefaultMilestoneToolService {
	t.Helper()
	service, err := NewMilestoneToolService(api, RunContext{
		CompanyID:      "0197f590-10b8-7cc0-a6c6-160ed7a18b2c",
		ConversationID: "0197f590-10b8-7cc0-a6c6-160ed7a18b2d",
		AgentID:        "0197f590-10b8-7cc0-a6c6-160ed7a18b2e",
		RunID:          "0197f590-10b8-7cc0-a6c6-160ed7a18b2f",
	})
	if err != nil {
		t.Fatalf("NewMilestoneToolService() error = %v", err)
	}
	return service
}

func TestAttestMilestonesSendsFounderWordsUnderRunProof(t *testing.T) {
	t.Parallel()

	api := &milestoneAPIStub{attested: []string{"launch", "product"}}
	service := milestoneToolFixture(t, api)

	result, err := service.AttestMilestones(context.Background(), AttestMilestonesInput{
		Milestones: []string{"product", "launch"},
		Note:       "  Selling since 2021.  ",
	})
	if err != nil {
		t.Fatalf("AttestMilestones() error = %v", err)
	}
	if api.path != "/companies/0197f590-10b8-7cc0-a6c6-160ed7a18b2c/milestone-attestations" {
		t.Errorf("path = %q", api.path)
	}
	if api.runID != "0197f590-10b8-7cc0-a6c6-160ed7a18b2f" {
		t.Errorf("the write did not carry Run proof: runID = %q", api.runID)
	}
	encoded, _ := json.Marshal(api.body)
	if !strings.Contains(string(encoded), `"note":"Selling since 2021."`) {
		t.Errorf("the founder's note was not sent verbatim: %s", encoded)
	}
	if len(result.Attested) != 2 || result.Instruction == "" {
		t.Errorf("result = %+v, want the attested set and a planning instruction", result)
	}
	// Every plan Task is assigned to the founder who applies it (ADR 0014);
	// the artifact Task is the one they do themself, with no Agent.
	if !strings.Contains(result.Instruction, "Task the founder does themself (no agent_id)") {
		t.Errorf("instruction = %q, want a Task the founder does themself", result.Instruction)
	}
}

func TestAttestMilestonesRefusesValuesOutsideTheCatalog(t *testing.T) {
	t.Parallel()

	api := &milestoneAPIStub{}
	service := milestoneToolFixture(t, api)

	for _, milestones := range [][]string{
		nil,
		{},
		{"website"},          // retired vocabulary
		{"sales_growth"},     // the old help-topic vocabulary
		{"unsure"},           // deferring is an empty focus list, never an attestation
		{"launch", "launch"}, // a repeat is a mistake, not a restatement
	} {
		if _, err := service.AttestMilestones(
			context.Background(),
			AttestMilestonesInput{Milestones: milestones},
		); err == nil {
			t.Errorf("AttestMilestones(%v) was accepted", milestones)
		}
	}
	if api.callCount != 0 {
		t.Errorf("a rejected attestation still reached the API %d times", api.callCount)
	}
}

func TestAttestMilestonesBoundsTheNote(t *testing.T) {
	t.Parallel()

	api := &milestoneAPIStub{}
	service := milestoneToolFixture(t, api)

	if _, err := service.AttestMilestones(context.Background(), AttestMilestonesInput{
		Milestones: []string{"product"},
		Note:       strings.Repeat("a", maxMilestoneAttestationNote+1),
	}); err == nil {
		t.Error("an unbounded note was accepted")
	}
	if api.callCount != 0 {
		t.Errorf("an over-long note still reached the API %d times", api.callCount)
	}
}

func TestNewMilestoneToolServiceRequiresRunContext(t *testing.T) {
	t.Parallel()

	if _, err := NewMilestoneToolService(nil, RunContext{}); err == nil {
		t.Error("a service without an API was constructed")
	}
	if _, err := NewMilestoneToolService(&milestoneAPIStub{}, RunContext{}); err == nil {
		t.Error("a service without a Run context was constructed")
	}
}

func TestAttestMilestonesSurfacesAPIFailure(t *testing.T) {
	t.Parallel()

	api := &milestoneAPIStub{err: errors.New("boom")}
	service := milestoneToolFixture(t, api)

	if _, err := service.AttestMilestones(context.Background(), AttestMilestonesInput{
		Milestones: []string{"product"},
	}); err == nil {
		t.Error("an API failure was swallowed")
	}
}
