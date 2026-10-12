package protocol

// RuntimeAccess is the viewer's server-derived standing on one runtime, or
// on one Agent through the runtime it is bound to, so a client renders from
// it instead of reconstructing permission from a Company role. It rides on
// the runtime roster, on the Agent's runtime reference and on the chat
// agent reference, one shape in all three places.
type RuntimeAccess struct {
	// Execute: the viewer's own requests may run here. On a runtime row it
	// also means Agents may be bound to it on the viewer's behalf. True for
	// the registrant, for anyone while the runtime is public, and — on an
	// Agent reference — for anyone when the registrant hosts the Agent
	// (ADR 0011).
	Execute bool `json:"execute"`
}
