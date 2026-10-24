package wiki

// PreviewChatSource uses the Page compiler for inline assistant replies. Chat
// owns no Project document, so project-dependent native blocks are rejected.
// Task sources still resolve through independently authorized client APIs.
func PreviewChatSource(source string, version int) SourcePreview {
	preview := PreviewSource(source, version)
	if preview.Content != nil && ValidateStandaloneBlocks(*preview.Content) != nil {
		preview.Content = nil
		preview.Complete = false
		preview.Diagnostics = append(
			preview.Diagnostics,
			SourceDiagnostic{
				Code:     "chat.project_block",
				Severity: "error",
				Start:    0,
				End:      len(source),
				Repair:   "Use standalone prose and UI components in chat. Put Project document references in a Page.",
			},
		)
	}
	return preview
}
