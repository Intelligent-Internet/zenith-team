package tools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// RunContext is the immutable identity injected by the daemon for one run.
// Tool input never supplies or overrides these values.
type RunContext struct {
	CompanyID      string `json:"companyId"`
	ConversationID string `json:"conversationId"`
	TaskID         string `json:"taskId,omitempty"`
	RunID          string `json:"runId"`
	AgentID        string `json:"agentId"`
	// WhiteboardID is the Board a Board chat's Run works on.
	WhiteboardID string `json:"whiteboardId,omitempty"`
}

func validateRunContext(runContext RunContext) error {
	if runContext.CompanyID == "" || runContext.ConversationID == "" ||
		runContext.RunID == "" || runContext.AgentID == "" {
		return errors.New("Zenith Team run context is unavailable")
	}
	return nil
}

func runRequestKey(operation string, runContext RunContext, payload any) (string, error) {
	canonical, err := json.Marshal(struct {
		Operation string     `json:"operation"`
		Context   RunContext `json:"context"`
		Payload   any        `json:"payload"`
	}{Operation: operation, Context: runContext, Payload: payload})
	if err != nil {
		return "", fmt.Errorf("encode Zenith Team run request key: %w", err)
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, append([]byte("boardly:run-tool:"), canonical...)).String(), nil
}
