package execution

import "errors"

var (
	ErrDeliverableInvalidInput         = errors.New("deliverable input is invalid")
	ErrDeliverableForbidden            = errors.New("deliverable action is forbidden")
	ErrDeliverableNotFound             = errors.New("deliverable was not found")
	ErrDeliverableConflict             = errors.New("deliverable conflicts with current state")
	ErrDeliverableTargetUnavailable    = errors.New("deliverable target is unavailable")
	ErrDeliverableInvalidConfiguration = errors.New("deliverable service configuration is invalid")
	ErrArtifactNotFound                = errors.New("Conversation Artifacts were not found")
)
