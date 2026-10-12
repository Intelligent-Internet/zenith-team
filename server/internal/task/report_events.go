package task

import (
	"errors"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
)

const maxSafeInteger = int64(9_007_199_254_740_991)

// ReportReadyPayload identifies a newly durable Task report queue item.
// Clients use it only as a refetch signal; report content remains authoritative
// in the Task report preview endpoint.
type ReportReadyPayload struct {
	TaskReportEventID string `json:"taskReportEventId"`
	RecipientAgentID  string `json:"recipientAgentId"`
	TaskID            string `json:"taskId"`
	TaskRevision      int64  `json:"taskRevision"`
	auth.ProjectFrame
}

// ReportChangedPayload identifies a durable change to the pending report
// queue. Clients refetch the authoritative preview when they receive it.
type ReportChangedPayload struct {
	RecipientAgentID string `json:"recipientAgentId"`
	TaskID           string `json:"taskId"`
	TaskRevision     int64  `json:"taskRevision"`
	// ProjectID is the Project whose readers receive the frame (ADR 0017)
	// when the producer knows it; zero leaves it to the Task on the
	// envelope. Read in-process and never on the wire.
	ProjectID uuid.UUID `json:"-"`
	auth.ProjectFrame
}

// NewReportReadyPayload builds a validated durable queue refetch signal.
func NewReportReadyPayload(
	taskReportEventID uuid.UUID,
	recipientAgentID uuid.UUID,
	taskID uuid.UUID,
	taskRevision int64,
) (ReportReadyPayload, error) {
	payload := ReportReadyPayload{
		TaskReportEventID: taskReportEventID.String(),
		RecipientAgentID:  recipientAgentID.String(),
		TaskID:            taskID.String(),
		TaskRevision:      taskRevision,
	}
	if err := validateReportReadyPayload(payload); err != nil {
		return ReportReadyPayload{}, err
	}
	return payload, nil
}

// NewReportChangedPayload builds a validated durable queue refetch signal.
func NewReportChangedPayload(
	recipientAgentID uuid.UUID,
	taskID uuid.UUID,
	taskRevision int64,
) (ReportChangedPayload, error) {
	payload := ReportChangedPayload{
		RecipientAgentID: recipientAgentID.String(),
		TaskID:           taskID.String(),
		TaskRevision:     taskRevision,
	}
	if err := validateReportChangedPayload(payload); err != nil {
		return ReportChangedPayload{}, err
	}
	return payload, nil
}

func validateReportReadyPayload(payload ReportReadyPayload) error {
	for _, id := range []string{
		payload.TaskReportEventID,
		payload.RecipientAgentID,
		payload.TaskID,
	} {
		if value, err := uuid.Parse(id); err != nil || value == uuid.Nil {
			return errors.New("Task report ready payload ID is invalid")
		}
	}
	if payload.TaskRevision < 1 || payload.TaskRevision > maxSafeInteger {
		return errors.New("Task report ready payload version is invalid")
	}
	return nil
}

func validateReportChangedPayload(payload ReportChangedPayload) error {
	for _, id := range []string{payload.RecipientAgentID, payload.TaskID} {
		if value, err := uuid.Parse(id); err != nil || value == uuid.Nil {
			return errors.New("Task report changed payload ID is invalid")
		}
	}
	if payload.TaskRevision < 1 || payload.TaskRevision > maxSafeInteger {
		return errors.New("Task report changed payload version is invalid")
	}
	return nil
}
