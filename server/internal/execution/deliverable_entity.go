package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

type DeliverableItemRole string

type ArtifactType string

const (
	ArtifactTypeApp   ArtifactType = "app"
	ArtifactTypeMedia ArtifactType = "media"
	ArtifactTypeText  ArtifactType = "text"
	ArtifactTypeFile  ArtifactType = "file"
)

const (
	DeliverableItemRolePrimary    DeliverableItemRole = "primary"
	DeliverableItemRoleSupporting DeliverableItemRole = "supporting"
	DeliverableItemRoleFinal      DeliverableItemRole = "final"
)

type DeliverableStatus string

const (
	DeliverableStatusReadyForReview   DeliverableStatus = "ready_for_review"
	DeliverableStatusApproved         DeliverableStatus = "approved"
	DeliverableStatusChangesRequested DeliverableStatus = "changes_requested"
	DeliverableStatusSuperseded       DeliverableStatus = "superseded"
)

type DeliverablePresentItemRequest struct {
	Role           DeliverableItemRole `json:"role"`
	Label          string              `json:"label"`
	AssetVersionID *uuid.UUID          `json:"assetVersionId,omitempty"`
	AttachmentID   *uuid.UUID          `json:"attachmentId,omitempty"`
	ordinal        int
}

type AppPreviewCommand struct {
	Command          string   `json:"command"`
	Args             []string `json:"args,omitempty"`
	WorkingDirectory string   `json:"workingDirectory,omitempty"`
	Port             int      `json:"port"`
}

type DeliverablePresentAppRequest struct {
	Commands []AppPreviewCommand `json:"commands"`
}

type DeliverablePresentRequest struct {
	PresentationID  *uuid.UUID                      `json:"presentationId,omitempty"`
	Type            ArtifactType                    `json:"type,omitempty"`
	App             *DeliverablePresentAppRequest   `json:"app,omitempty"`
	Title           string                          `json:"title"`
	Summary         string                          `json:"summary"`
	SubmissionNotes *string                         `json:"submissionNotes,omitempty"`
	Items           []DeliverablePresentItemRequest `json:"items"`
	fingerprint     string
	previewURL      string
	payload         json.RawMessage
}

type DeliverableOutputItem struct {
	ID             uuid.UUID           `json:"id"`
	AssetVersionID uuid.UUID           `json:"assetVersionId"`
	ContentType    string              `json:"contentType"`
	SizeBytes      int64               `json:"sizeBytes"`
	Ordinal        int                 `json:"ordinal"`
	Role           DeliverableItemRole `json:"role"`
	Label          string              `json:"label"`
	Kind           string              `json:"kind"`
	DisplayName    string              `json:"displayName"`
	Detail         string              `json:"detail"`
}

type DeliverableOutput struct {
	ActorType       string                  `json:"actorType"`
	ActorUserID     *uuid.UUID              `json:"actorUserId,omitempty"`
	ActorAgentID    *uuid.UUID              `json:"actorAgentId,omitempty"`
	Freshness       string                  `json:"freshness"`
	LiveURL         *string                 `json:"liveUrl,omitempty"`
	PresentationURL *string                 `json:"presentationUrl,omitempty"`
	CanvasURL       *string                 `json:"canvasUrl,omitempty"`
	ID              uuid.UUID               `json:"id"`
	Type            ArtifactType            `json:"type"`
	URL             *string                 `json:"url,omitempty"`
	Title           string                  `json:"title"`
	Summary         string                  `json:"summary"`
	SubmissionNotes *string                 `json:"submissionNotes,omitempty"`
	Items           []DeliverableOutputItem `json:"items"`
	CreatedAt       time.Time               `json:"createdAt"`
}

type Deliverable struct {
	ID             uuid.UUID         `json:"id"`
	RunID          *uuid.UUID        `json:"runId"`
	Status         DeliverableStatus `json:"status"`
	Output         DeliverableOutput `json:"output"`
	SubmittedAt    time.Time         `json:"submittedAt"`
	ReviewedAt     *time.Time        `json:"reviewedAt,omitempty"`
	ReviewFeedback *string           `json:"reviewFeedback,omitempty"`
	Revision       int64             `json:"revision"`
}

// DeliverableRunResult is one Run's result. RuntimeID names the machine the
// Run executed on to its registrant alone, nil for everyone else (ADR 0014).
type DeliverableRunResult struct {
	StudioCanvas          *SavedStudioCanvas           `json:"studioCanvas,omitempty"`
	CanAttachStudioCanvas bool                         `json:"canAttachStudioCanvas"`
	RuntimeID             *uuid.UUID                   `json:"runtimeId"`
	RunStatus             string                       `json:"runStatus"`
	Output                *DeliverableOutput           `json:"output"`
	Deliverable           *Deliverable                 `json:"deliverable"`
	Publication           *Publication                 `json:"publication"`
	CanPublish            bool                         `json:"canPublish"`
	CanUnpublish          bool                         `json:"canUnpublish"`
	PublicationSubdomain  *PublicationSubdomainOptions `json:"publicationSubdomain,omitempty"`
}

type DeliverablePresentResult struct {
	OutputID uuid.UUID `json:"outputId"`
	Created  bool      `json:"created"`
}

func normalizeDeliverablePresentation(
	request DeliverablePresentRequest,
) (DeliverablePresentRequest, error) {
	if request.Type == "" {
		request.Type = ArtifactTypeFile
	}
	if request.Type != ArtifactTypeApp && request.Type != ArtifactTypeMedia &&
		request.Type != ArtifactTypeText && request.Type != ArtifactTypeFile {
		return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Summary = strings.TrimSpace(request.Summary)
	if request.PresentationID != nil &&
		(request.Type != ArtifactTypeText || !util.ValidUUID(*request.PresentationID)) {
		return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
	}
	if !validDeliverableText(request.Title, 160) ||
		!validDeliverableText(request.Summary, 4000) {
		return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
	}
	switch request.Type {
	case ArtifactTypeApp:
		if len(request.Items) != 0 || request.App == nil {
			return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
		}
		commands, err := normalizeAppPreviewCommands(request.App.Commands)
		if err != nil {
			return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
		}
		request.App = &DeliverablePresentAppRequest{Commands: commands}
		request.payload, err = json.Marshal(request.App)
		if err != nil {
			return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
		}
	default:
		if request.App != nil || len(request.Items) < 1 || len(request.Items) > 32 {
			return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
		}
		request.payload = json.RawMessage(`{}`)
		if request.PresentationID != nil {
			request.payload, _ = json.Marshal(map[string]any{"presentationId": request.PresentationID})
		}
	}
	if request.SubmissionNotes != nil {
		notes := strings.TrimSpace(*request.SubmissionNotes)
		if !validDeliverableText(notes, 4000) {
			return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
		}
		request.SubmissionNotes = &notes
	}

	primaryCount := 0
	items := make([]DeliverablePresentItemRequest, len(request.Items))
	for index, item := range request.Items {
		item.Label = strings.TrimSpace(item.Label)
		item.ordinal = index
		if !validDeliverableText(item.Label, 120) ||
			(item.Role != DeliverableItemRolePrimary && item.Role != DeliverableItemRoleFinal && item.Role != DeliverableItemRoleSupporting) {
			return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
		}
		if item.Role == DeliverableItemRolePrimary {
			primaryCount++
		}
		if (item.AssetVersionID == nil) == (item.AttachmentID == nil) ||
			(item.AssetVersionID != nil && !util.ValidUUID(*item.AssetVersionID)) ||
			(item.AttachmentID != nil && !util.ValidUUID(*item.AttachmentID)) {
			return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
		}
		items[index] = item
	}
	if request.Type != ArtifactTypeApp && primaryCount != 1 {
		return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
	}
	request.Items = items

	type fingerprintRequest struct {
		PresentationID  *uuid.UUID                      `json:"presentationId,omitempty"`
		Type            ArtifactType                    `json:"type,omitempty"`
		App             *DeliverablePresentAppRequest   `json:"app,omitempty"`
		Title           string                          `json:"title"`
		Summary         string                          `json:"summary"`
		SubmissionNotes *string                         `json:"submissionNotes,omitempty"`
		Items           []DeliverablePresentItemRequest `json:"items"`
	}
	fingerprint := fingerprintRequest{
		PresentationID: request.PresentationID,
		Title:          request.Title, Summary: request.Summary,
		App: request.App, SubmissionNotes: request.SubmissionNotes, Items: request.Items,
	}
	if request.Type != ArtifactTypeFile {
		fingerprint.Type = request.Type
	}
	encoded, err := json.Marshal(fingerprint)
	if err != nil {
		return DeliverablePresentRequest{}, ErrDeliverableInvalidInput
	}
	digest := sha256.Sum256(encoded)
	request.fingerprint = hex.EncodeToString(digest[:])
	return request, nil
}

func normalizeAppPreviewCommands(commands []AppPreviewCommand) ([]AppPreviewCommand, error) {
	if len(commands) < 1 || len(commands) > 8 {
		return nil, ErrDeliverableInvalidInput
	}
	normalized := make([]AppPreviewCommand, len(commands))
	ports := make(map[int]struct{}, len(commands))
	for index, command := range commands {
		if !validAppPreviewCommand(command.Command) || len(command.Args) > 64 ||
			command.Port < 1024 || command.Port > 65535 {
			return nil, ErrDeliverableInvalidInput
		}
		if _, duplicate := ports[command.Port]; duplicate {
			return nil, ErrDeliverableInvalidInput
		}
		ports[command.Port] = struct{}{}
		for _, argument := range command.Args {
			if !utf8.ValidString(argument) || len(argument) > 4096 ||
				strings.ContainsAny(argument, "\x00\r\n") {
				return nil, ErrDeliverableInvalidInput
			}
		}
		workingDirectory := command.WorkingDirectory
		if workingDirectory == "" {
			workingDirectory = "."
		}
		if path.IsAbs(workingDirectory) || path.Clean(workingDirectory) != workingDirectory ||
			workingDirectory == ".." || strings.HasPrefix(workingDirectory, "../") {
			return nil, ErrDeliverableInvalidInput
		}
		command.WorkingDirectory = workingDirectory
		command.Args = append([]string(nil), command.Args...)
		normalized[index] = command
	}
	return normalized, nil
}

func validAppPreviewCommand(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		len(value) <= 4096 && strings.IndexFunc(value, func(r rune) bool {
		return r == 0 || r <= ' ' || r == 0x7f
	}) < 0
}

func artifactPrimaryContentMatches(artifactType ArtifactType, contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch artifactType {
	case ArtifactTypeApp:
		return contentType == "application/zip"
	case ArtifactTypeMedia:
		return strings.HasPrefix(contentType, "image/") || strings.HasPrefix(contentType, "video/") ||
			strings.HasPrefix(contentType, "audio/")
	case ArtifactTypeText:
		return strings.HasPrefix(contentType, "text/") ||
			contentType == "application/json" || strings.HasSuffix(contentType, "+json") ||
			contentType == "application/xml" || strings.HasSuffix(contentType, "+xml") ||
			contentType == "application/yaml" || contentType == "application/x-yaml" ||
			contentType == "application/octet-stream"
	case ArtifactTypeFile:
		return true
	default:
		return false
	}
}

func validDeliverableText(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= maximum
}
