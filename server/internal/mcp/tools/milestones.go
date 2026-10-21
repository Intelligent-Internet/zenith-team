package tools

import (
	"context"
	"errors"
	"strings"

	taskmilestone "github.com/Intelligent-Internet/zenith-team/server/internal/task/milestone"
)

// AttestMilestonesToolName is the one tool that records a founder's statement
// that work was finished before Zenith Team existed. A company arriving with a
// product already selling has nothing in the plan to prove it, so without this
// the CEO Agent would propose rebuilding what already works.
const AttestMilestonesToolName = "attest_milestones"

const maxMilestoneAttestationNote = 2000

type MilestoneAPI interface {
	PostRunJSON(context.Context, string, string, any, any) error
}

type MilestoneToolService interface {
	AttestMilestones(context.Context, AttestMilestonesInput) (AttestMilestonesResult, error)
}

var _ MilestoneToolService = (*DefaultMilestoneToolService)(nil)

type DefaultMilestoneToolService struct {
	api     MilestoneAPI
	context RunContext
}

func NewMilestoneToolService(
	api MilestoneAPI,
	runContext RunContext,
) (*DefaultMilestoneToolService, error) {
	if api == nil || validateRunContext(runContext) != nil ||
		!canonicalUUID(runContext.CompanyID) || !canonicalUUID(runContext.RunID) {
		return nil, errors.New("Zenith Team milestone Run context is unavailable")
	}
	return &DefaultMilestoneToolService{api: api, context: runContext}, nil
}

type AttestMilestonesInput struct {
	// Milestones the founder confirmed were completed outside Zenith Team.
	Milestones []string `json:"milestones" jsonschema:"the milestone types the founder confirmed are already done"`
	// Note is the founder's own explanation, carried verbatim.
	Note string `json:"note,omitempty" jsonschema:"the founder's own words about what already exists"`
}

type AttestMilestonesResult struct {
	Attested    []string `json:"attested"`
	Instruction string   `json:"instruction"`
}

type attestMilestonesRequest struct {
	Milestones []string `json:"milestones"`
	Note       string   `json:"note"`
}

type attestMilestonesResponse struct {
	Attested []string `json:"attested"`
}

// AttestMilestones records the milestones a founder has just confirmed are
// already done. It is deliberately not a judgement the Agent may make alone:
// the prompt requires an ask_human answer first, and the note carries the
// founder's words rather than the Agent's summary of them.
func (s *DefaultMilestoneToolService) AttestMilestones(
	ctx context.Context,
	input AttestMilestonesInput,
) (AttestMilestonesResult, error) {
	milestones, err := normalizeAttestedMilestones(input.Milestones)
	if err != nil {
		return AttestMilestonesResult{}, err
	}
	note := strings.TrimSpace(input.Note)
	if len(note) > maxMilestoneAttestationNote {
		return AttestMilestonesResult{}, errors.New(
			"note must be at most 2000 bytes",
		)
	}

	var response attestMilestonesResponse
	if err := s.api.PostRunJSON(
		ctx,
		"/companies/"+s.context.CompanyID+"/milestone-attestations",
		s.context.RunID,
		attestMilestonesRequest{Milestones: milestones, Note: note},
		&response,
	); err != nil {
		return AttestMilestonesResult{}, err
	}
	return AttestMilestonesResult{
		Attested: response.Attested,
		Instruction: "These milestones are now satisfied prerequisites. Do not plan any Task " +
			"that redoes them. If a milestone you are planning needs a concrete artifact from " +
			"one of these, create a Task the founder does themself (no agent_id) asking for that artifact instead.",
	}, nil
}

func normalizeAttestedMilestones(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("milestones must name at least one milestone type")
	}
	if len(values) > len(taskmilestone.Catalog()) {
		return nil, errors.New("milestones repeats more values than the catalog defines")
	}
	seen := make(map[string]struct{}, len(values))
	milestones := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !taskmilestone.ValidType(taskmilestone.Type(value)) {
			return nil, errors.New("milestones contains a value outside the milestone catalog: " + value)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("milestones repeats " + value)
		}
		seen[value] = struct{}{}
		milestones = append(milestones, value)
	}
	return milestones, nil
}
