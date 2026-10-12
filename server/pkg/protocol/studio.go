package protocol

// StudioProjectRequest is the daemon's Run-proven conversation project operation.
type StudioProjectRequest struct {
	SchemaVersion int    `json:"schemaVersion,omitempty"`
	RequestID     string `json:"requestId,omitempty"`
	Action        string `json:"action"`
	RunToken      string `json:"runToken"`
	Title         string `json:"title,omitempty"`
	Description   string `json:"description,omitempty"`
	BindingID     string `json:"bindingId,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	// APIOrigin is supplied by the authenticated daemon so legacy reservations
	// use the same Studio authority as the runtime that will open them.
	APIOrigin string `json:"apiOrigin,omitempty"`
}

// StudioProject contains no credentials or daemon-local project state.
type StudioProject struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CanCreate   bool   `json:"canCreate"`
}
