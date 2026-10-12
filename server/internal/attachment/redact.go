package attachment

import (
	"fmt"
	"log/slog"

	"boardly.dev/boardly/server/pkg/redact"
)

func (DefaultAttachmentService) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.DefaultAttachmentService")
}

func (DefaultAttachmentService) GoString() string {
	return redact.TypeString("attachment.DefaultAttachmentService")
}

func (DefaultAttachmentService) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.DefaultAttachmentService")
}

func (ServiceOptions) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.ServiceOptions")
}
func (ServiceOptions) GoString() string     { return redact.TypeString("attachment.ServiceOptions") }
func (ServiceOptions) LogValue() slog.Value { return redact.TypeLogValue("attachment.ServiceOptions") }

func (CleanupWorkerOptions) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.CleanupWorkerOptions")
}

func (CleanupWorkerOptions) GoString() string {
	return redact.TypeString("attachment.CleanupWorkerOptions")
}

func (CleanupWorkerOptions) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.CleanupWorkerOptions")
}

func (CleanupWorker) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.CleanupWorker")
}
func (CleanupWorker) GoString() string     { return redact.TypeString("attachment.CleanupWorker") }
func (CleanupWorker) LogValue() slog.Value { return redact.TypeLogValue("attachment.CleanupWorker") }

func (InitiateInput) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.InitiateInput")
}
func (InitiateInput) GoString() string     { return redact.TypeString("attachment.InitiateInput") }
func (InitiateInput) LogValue() slog.Value { return redact.TypeLogValue("attachment.InitiateInput") }

func (CompleteInput) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.CompleteInput")
}
func (CompleteInput) GoString() string     { return redact.TypeString("attachment.CompleteInput") }
func (CompleteInput) LogValue() slog.Value { return redact.TypeLogValue("attachment.CompleteInput") }

func (DownloadInput) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.DownloadInput")
}
func (DownloadInput) GoString() string     { return redact.TypeString("attachment.DownloadInput") }
func (DownloadInput) LogValue() slog.Value { return redact.TypeLogValue("attachment.DownloadInput") }

func (DeleteInput) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.DeleteInput")
}
func (DeleteInput) GoString() string     { return redact.TypeString("attachment.DeleteInput") }
func (DeleteInput) LogValue() slog.Value { return redact.TypeLogValue("attachment.DeleteInput") }

func (CleanupInput) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.CleanupInput")
}
func (CleanupInput) GoString() string     { return redact.TypeString("attachment.CleanupInput") }
func (CleanupInput) LogValue() slog.Value { return redact.TypeLogValue("attachment.CleanupInput") }

// Response redacts itself in entity.go, beside the projection.

func (InitiateResult) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.InitiateResult")
}
func (InitiateResult) GoString() string     { return redact.TypeString("attachment.InitiateResult") }
func (InitiateResult) LogValue() slog.Value { return redact.TypeLogValue("attachment.InitiateResult") }

func (CompleteResult) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.CompleteResult")
}
func (CompleteResult) GoString() string     { return redact.TypeString("attachment.CompleteResult") }
func (CompleteResult) LogValue() slog.Value { return redact.TypeLogValue("attachment.CompleteResult") }

func (DownloadResult) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.DownloadResult")
}
func (DownloadResult) GoString() string     { return redact.TypeString("attachment.DownloadResult") }
func (DownloadResult) LogValue() slog.Value { return redact.TypeLogValue("attachment.DownloadResult") }

func (attachmentRecord) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.attachmentRecord")
}
func (attachmentRecord) GoString() string { return redact.TypeString("attachment.attachmentRecord") }
func (attachmentRecord) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.attachmentRecord")
}

func (attachmentLocator) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.attachmentLocator")
}
func (attachmentLocator) GoString() string { return redact.TypeString("attachment.attachmentLocator") }
func (attachmentLocator) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.attachmentLocator")
}

func (pendingAttachmentCreate) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.pendingAttachmentCreate")
}

func (pendingAttachmentCreate) GoString() string {
	return redact.TypeString("attachment.pendingAttachmentCreate")
}

func (pendingAttachmentCreate) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.pendingAttachmentCreate")
}

func (pendingAttachmentResult) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.pendingAttachmentResult")
}

func (pendingAttachmentResult) GoString() string {
	return redact.TypeString("attachment.pendingAttachmentResult")
}

func (pendingAttachmentResult) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.pendingAttachmentResult")
}

func (attachmentCompletion) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.attachmentCompletion")
}

func (attachmentCompletion) GoString() string {
	return redact.TypeString("attachment.attachmentCompletion")
}

func (attachmentCompletion) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.attachmentCompletion")
}

func (attachmentCompletionAuthorization) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.attachmentCompletionAuthorization")
}

func (attachmentCompletionAuthorization) GoString() string {
	return redact.TypeString("attachment.attachmentCompletionAuthorization")
}

func (attachmentCompletionAuthorization) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.attachmentCompletionAuthorization")
}

func (attachmentDownloadAuthorization) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.attachmentDownloadAuthorization")
}

func (attachmentDownloadAuthorization) GoString() string {
	return redact.TypeString("attachment.attachmentDownloadAuthorization")
}

func (attachmentDownloadAuthorization) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.attachmentDownloadAuthorization")
}

func (attachmentCompletionResult) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.attachmentCompletionResult")
}

func (attachmentCompletionResult) GoString() string {
	return redact.TypeString("attachment.attachmentCompletionResult")
}

func (attachmentCompletionResult) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.attachmentCompletionResult")
}

func (attachmentDeletionRequest) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.attachmentDeletionRequest")
}

func (attachmentDeletionRequest) GoString() string {
	return redact.TypeString("attachment.attachmentDeletionRequest")
}

func (attachmentDeletionRequest) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.attachmentDeletionRequest")
}

func (attachmentDeletionFinalization) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.attachmentDeletionFinalization")
}

func (attachmentDeletionFinalization) GoString() string {
	return redact.TypeString("attachment.attachmentDeletionFinalization")
}

func (attachmentDeletionFinalization) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.attachmentDeletionFinalization")
}

func (LifecycleEffect) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.LifecycleEffect")
}
func (LifecycleEffect) GoString() string { return redact.TypeString("attachment.LifecycleEffect") }
func (LifecycleEffect) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.LifecycleEffect")
}

func (safeAttachmentReader) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "attachment.safeAttachmentReader")
}

func (safeAttachmentReader) GoString() string {
	return redact.TypeString("attachment.safeAttachmentReader")
}

func (safeAttachmentReader) LogValue() slog.Value {
	return redact.TypeLogValue("attachment.safeAttachmentReader")
}
