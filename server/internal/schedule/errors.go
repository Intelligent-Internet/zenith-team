package schedule

import "errors"

var (
	ErrUnauthenticated     = errors.New("Schedule user is not authenticated")
	ErrForbidden           = errors.New("Schedule owner role is required")
	ErrInvalidInput        = errors.New("invalid Schedule input")
	ErrNotFound            = errors.New("Schedule or Company not found")
	ErrConflict            = errors.New("Schedule conflict")
	ErrVersionConflict     = errors.New("Schedule revision conflict")
	ErrReviewStale         = errors.New("Schedule review is stale")
	ErrTerminal            = errors.New("Schedule is terminal")
	ErrInvalidState        = errors.New("Schedule lifecycle transition is invalid")
	ErrRunActive           = errors.New("Schedule Run is already active")
	ErrWorkerConfiguration = errors.New("invalid Schedule firing worker configuration")
	ErrMaterialization     = errors.New("materialize Schedule Run")
	ErrInvalidStoredState  = errors.New("invalid stored Schedule state")

	// ErrSelectionUnavailable is a preview whose selected Agent or Connector
	// source cannot be resolved for the Company. It is its own sentinel
	// because no Schedule exists yet, so ErrNotFound would name the wrong
	// thing; writes that echo a digest translate it to ErrReviewStale.
	ErrSelectionUnavailable = errors.New("Schedule Agent or source cannot be reviewed")

	// ErrAgentBindingRequired is an active Agent the creator, an active
	// member, has not set up on one of their own live runtimes. Every firing
	// runs on that binding (ADR 0014), so it is the creator's setup step
	// rather than an unavailable selection or a stale review.
	ErrAgentBindingRequired = errors.New("Schedule creator has not set the Agent up")
)
