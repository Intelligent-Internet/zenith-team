package protocol

// ToolObservation is sanitized metadata for a shared machine installation and
// optional Company-and-Agent CLI-owned authentication home (not_required for a local
// renderer). An empty AgentID describes only the shared installation; it
// cannot report an authenticated account. It never contains credentials,
// local paths, subprocess output, or a promise that a paid operation will work.
type ToolObservation struct {
	AgentID    string `json:"agentId,omitempty"`
	APIOrigin  string `json:"apiOrigin,omitempty"`
	Key        string `json:"key"`
	Version    string `json:"version"`
	State      string `json:"state"`
	AuthState  string `json:"authState"`
	ReasonCode string `json:"reasonCode"`
	ObservedAt string `json:"observedAt"`
}
