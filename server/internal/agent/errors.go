package agent

import "errors"

var (
	ErrUnauthenticated  = errors.New("user is not authenticated")
	ErrInvalidInput     = errors.New("invalid Agent input")
	ErrConflict         = errors.New("Agent create conflict")
	ErrNotFound         = errors.New("Agent or Company not found")
	ErrForbidden        = errors.New("Company role lacks the required permission")
	ErrRevisionConflict = errors.New("Agent revision conflict")
	ErrCycle            = errors.New("Agent reports-to cycle")
	ErrRootImmutable    = errors.New("root Agent topology is immutable")
	ErrArchiveBlocked   = errors.New("Agent archive is blocked")
	// ErrThinkingLevelUnsupported: the level is not one the bound runtime's
	// provider can take (pkg/agent.KnownThinkingLevel).
	ErrThinkingLevelUnsupported = errors.New("Agent thinking level is outside the provider vocabulary")
	// ErrProviderImmutable: an update named the provider, which is fixed at
	// creation (ADR 0014). Duplicating the Agent is how it changes provider.
	ErrProviderImmutable = errors.New("Agent provider is immutable")
)
