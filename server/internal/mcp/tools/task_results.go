package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/uuid"
)

type taskResultAPI interface {
	PostRunJSON(context.Context, string, string, any, any) error
}

type TaskResultToolService interface {
	FinalizeTaskResults(context.Context, FinalizeTaskResultsInput) (FinalizeTaskResultsResult, error)
}

var _ TaskResultToolService = (*DefaultTaskResultToolService)(nil)

type DefaultTaskResultToolService struct {
	api     taskResultAPI
	context RunContext
}

func NewTaskResultToolService(api taskResultAPI, run RunContext) (*DefaultTaskResultToolService, error) {
	if api == nil {
		return nil, errors.New("Task result API is unavailable")
	}
	if err := validateRunContext(run); err != nil {
		return nil, err
	}
	taskID, err := uuid.Parse(run.TaskID)
	if err != nil || taskID == uuid.Nil {
		return nil, errors.New("Task result finalization requires a Task Run")
	}
	return &DefaultTaskResultToolService{api: api, context: run}, nil
}

type FinalizeTaskResultsInput struct {
	ExpectedRevision int64    `json:"expectedRevision" jsonschema:"Current Task revision read immediately before selecting final results. Refresh after a conflict."`
	OutputIDs        []string `json:"outputIds"        jsonschema:"Ordered outputIds returned by present_output in this Run. Select 1 to 32; the first is primary. Delivery alone does not select a Task result."`
}
type FinalizeTaskResultsResult struct {
	Status   string `json:"status"`
	Revision int64  `json:"revision"`
}

func (s *DefaultTaskResultToolService) FinalizeTaskResults(
	ctx context.Context,
	in FinalizeTaskResultsInput,
) (FinalizeTaskResultsResult, error) {
	// Only a Task with deliveries needs a selection, so an answer-only Run has
	// nothing to finalize; say so rather than reject it as malformed input.
	if len(in.OutputIDs) == 0 {
		return FinalizeTaskResultsResult{}, errors.New(
			"Task result selection is empty: select outputIds that present_output returned in this Run, or end the Run without finalizing when it presented none",
		)
	}
	if in.ExpectedRevision < 1 || len(in.OutputIDs) > 32 {
		return FinalizeTaskResultsResult{}, errors.New("Task result selection is invalid")
	}
	ids := make([]string, len(in.OutputIDs))
	seen := map[uuid.UUID]bool{}
	for i, value := range in.OutputIDs {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || seen[id] {
			return FinalizeTaskResultsResult{}, errors.New(
				"Task result selection contains an invalid or duplicate output",
			)
		}
		ids[i] = id.String()
		seen[id] = true
	}
	in.OutputIDs = ids
	key, err := runRequestKey("finalize_task_results", s.context, in)
	if err != nil {
		return FinalizeTaskResultsResult{}, err
	}
	path := fmt.Sprintf(
		"/companies/%s/tasks/%s/results",
		url.PathEscape(s.context.CompanyID),
		url.PathEscape(s.context.TaskID),
	)
	var result FinalizeTaskResultsResult
	if err = s.api.PostRunJSON(ctx, path, key, in, &result); err != nil {
		return result, fmt.Errorf("finalize Task results: %w", err)
	}
	if result.Status != "in_progress" || result.Revision < 1 {
		return FinalizeTaskResultsResult{}, errors.New("Task result finalization response is invalid")
	}
	return result, nil
}
