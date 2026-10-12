package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

type taskFailureAPI interface {
	PostRunJSON(context.Context, string, string, any, any) error
}

// TaskFailureService validates the MCP-facing reason-only contract.
type TaskFailureService interface {
	MarkTaskFailed(context.Context, MarkTaskFailedInput) (MarkTaskFailedResult, error)
}

var _ TaskFailureService = (*DefaultTaskFailureService)(nil)

type DefaultTaskFailureService struct {
	api     taskFailureAPI
	context RunContext
}

// NewTaskFailureService constructs the Task failure tool service.
func NewTaskFailureService(
	api taskFailureAPI,
	runContext RunContext,
) (*DefaultTaskFailureService, error) {
	if api == nil {
		return nil, errors.New("Task failure API is unavailable")
	}
	if err := validateRunContext(runContext); err != nil {
		return nil, errors.New("Task failure run context is unavailable")
	}
	return &DefaultTaskFailureService{api: api, context: runContext}, nil
}

// MarkTaskFailedInput deliberately contains no caller-selectable identity.
type MarkTaskFailedInput struct {
	Reason string `json:"reason" jsonschema:"Concise business reason why the assigned Task cannot be completed."`
}

// MarkTaskFailedResult confirms the authoritative Task outcome.
type MarkTaskFailedResult struct {
	Status string `json:"status"`
}

// MarkTaskFailed validates and forwards one business-failure reason.
func (s *DefaultTaskFailureService) MarkTaskFailed(
	ctx context.Context,
	input MarkTaskFailedInput,
) (MarkTaskFailedResult, error) {
	reason := strings.TrimSpace(input.Reason)
	if s == nil || s.api == nil {
		return MarkTaskFailedResult{}, errors.New("Task failure capability is unavailable")
	}
	if reason == "" || utf8.RuneCountInString(reason) > 2000 {
		return MarkTaskFailedResult{}, errors.New("Task failure reason is invalid")
	}
	request := struct {
		Reason string `json:"reason"`
	}{Reason: reason}
	requestKey, err := runRequestKey("mark_task_failed", s.context, request)
	if err != nil {
		return MarkTaskFailedResult{}, fmt.Errorf("create Task failure request key: %w", err)
	}
	path := fmt.Sprintf(
		"/companies/%s/task-failures",
		url.PathEscape(s.context.CompanyID),
	)
	var response MarkTaskFailedResult
	if err := s.api.PostRunJSON(ctx, path, requestKey, request, &response); err != nil {
		return MarkTaskFailedResult{}, fmt.Errorf("mark Task failed: %w", err)
	}
	if response.Status != "failed" {
		return MarkTaskFailedResult{}, errors.New("Task failure response is invalid")
	}
	return response, nil
}
