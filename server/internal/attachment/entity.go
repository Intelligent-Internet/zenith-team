package attachment

import (
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/redact"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusReady    Status = "ready"
	StatusDeleting Status = "deleting"
	StatusDeleted  Status = "deleted"
)

type InitiateInput struct {
	RequestKey       uuid.UUID
	CompanyID        uuid.UUID
	UploadedByUserID uuid.UUID
	Filename         string
	ContentType      string
	SizeBytes        int64
}

type CompleteInput struct {
	ID               uuid.UUID
	CompanyID        uuid.UUID
	UploadedByUserID uuid.UUID
	// RunActor, when set, is the Run whose proof the upload carried: the
	// daemon uploads a Run's presented files on its member's session, and
	// the completion is then recorded as the Run's Agent acting for that
	// member in the Run. The zero value records the uploader.
	RunActor events.Actor
}

type DownloadInput struct {
	ID                uuid.UUID
	CompanyID         uuid.UUID
	RequestedByUserID uuid.UUID
}

// ResolvedDownloadInput names an Attachment after another domain has already
// resolved the caller's authority to the exact file.
type ResolvedDownloadInput struct {
	ID                   uuid.UUID
	CompanyID            uuid.UUID
	OutputAccessResolved bool
	// KnownRevision permits revalidation of ready, immutable bytes after access
	// has been resolved. Zero always opens storage.
	KnownRevision int64
	// RequestedByUserID is the frozen execution authorizer for a run reference.
	// Its current artifact grants are checked when OutputAccessResolved is false.
	RequestedByUserID uuid.UUID
}

// RunDownloadInput names a file a Run's frozen context lists, for the daemon
// running that Run. AuthorizedByUserID is the Run's authorizer, under ADR 0014
// also the owner of the machine asking.
type RunDownloadInput struct {
	ID                 uuid.UUID
	CompanyID          uuid.UUID
	RunID              uuid.UUID
	AuthorizedByUserID uuid.UUID
	// FrozenProjectDocument says the id is in the Run's frozen Project
	// documents: the freeze read it as an active file the authorizer could
	// open, so it stays open to the Run after it is retired while the
	// authorizer still reads one of its Projects.
	FrozenProjectDocument bool
}

// ResolvedShareInput names an Attachment after another domain has already
// resolved the caller's authority to share the exact file.
type ResolvedShareInput struct {
	ID        uuid.UUID
	CompanyID uuid.UUID
}

type DeleteInput struct {
	ID               uuid.UUID
	CompanyID        uuid.UUID
	UploadedByUserID uuid.UUID
}

type CleanupInput struct {
	ID        uuid.UUID
	CompanyID uuid.UUID
}

type attachmentRecord struct {
	ID                   uuid.UUID
	CompanyID            uuid.UUID
	UploadedByUserID     uuid.UUID
	RequestKey           uuid.UUID
	ObjectKey            string
	Filename             string
	ContentType          string
	SizeBytes            int64
	Status               Status
	UploadLeaseExpiresAt *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	Revision             int64
}

// storableInitiateIdentity reports whether the three identities an initiation
// is keyed by, and the byte count the upload will be streamed against, may be
// written to the attachments row.
//
// Nothing else in the request path decides this. The multipart metadata part
// is decoded with util.BindStrictJSON rather than the gin binder, so no
// `binding` tag on attachmentMetadataRequest ever runs; UploadedByUserID is
// not in that body at all but taken from the authenticated principal; and the
// schema declares `uploaded_by_user_id uuid NOT NULL` with no foreign key and
// `size_bytes bigint NOT NULL` with no CHECK, so Postgres would accept both a
// nil actor and a negative or absurd length.
func storableInitiateIdentity(input InitiateInput) bool {
	return util.ValidUUID(input.RequestKey) && util.ValidUUID(input.CompanyID) &&
		util.ValidUUID(input.UploadedByUserID) && validAttachmentSize(input.SizeBytes)
}

func normalizeInitiateInput(input InitiateInput) (InitiateInput, error) {
	if !storableInitiateIdentity(input) {
		return InitiateInput{}, ErrInvalidInput
	}
	filename, err := normalizeFilename(input.Filename)
	if err != nil {
		return InitiateInput{}, err
	}
	contentType, err := normalizeContentType(input.ContentType)
	if err != nil {
		return InitiateInput{}, err
	}
	input.Filename, input.ContentType = filename, contentType
	return input, nil
}

func normalizeFilename(value string) (string, error) {
	value = protocol.TrimContractWhitespace(value)
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) ||
		len(value) > 1024 || utf8.RuneCountInString(value) > 255 ||
		strings.ContainsAny(value, `/\\`) || containsControl(value) {
		return "", ErrInvalidInput
	}
	return value, nil
}

func normalizeContentType(value string) (string, error) {
	value = protocol.TrimContractWhitespace(value)
	if value == "" || !utf8.ValidString(value) || len(value) > 255 || containsControl(value) {
		return "", ErrInvalidInput
	}
	mediaType := strings.ToLower(value)
	if !validBareAttachmentContentType(mediaType) {
		return "", ErrInvalidInput
	}
	return mediaType, nil
}

func validBareAttachmentContentType(value string) bool {
	if value == "" || value != strings.ToLower(value) || strings.Count(value, "/") != 1 {
		return false
	}
	typeToken, subtypeToken, _ := strings.Cut(value, "/")
	return validAttachmentMIMEToken(typeToken) && validAttachmentMIMEToken(subtypeToken)
}

func validAttachmentMIMEToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			strings.ContainsRune("!#$%&'+.^_`|~-", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validAttachmentSize(value int64) bool {
	return value >= 0 && value <= (int64(100<<20))
}

func canonicalObjectKey(companyID, attachmentID uuid.UUID) string {
	return path.Join("companies", companyID.String(), "attachments", attachmentID.String())
}

func attachmentDTOFromRecord(record attachmentRecord, status PublicStatus) Response {
	return Response{
		ID: record.ID, CompanyID: record.CompanyID, UploadedByUserID: record.UploadedByUserID,
		Filename: record.Filename, ContentType: record.ContentType, SizeBytes: record.SizeBytes,
		Status:    status,
		CreatedAt: record.CreatedAt.UTC(), UpdatedAt: record.UpdatedAt.UTC(),
		Revision: record.Revision,
	}
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

// PublicStatus is the attachment lifecycle visible to API and realtime
// consumers. Cleanup-only deleting/deleted states remain internal.
type PublicStatus string

const (
	PublicStatusPending PublicStatus = "pending"
	PublicStatusReady   PublicStatus = "ready"
)

// Response is the public Attachment projection: one shape for the REST read
// and the attachment.completed frame, so a field added here reaches both.
// CompletedAt is explicit null while the upload is still pending — never
// absent — so every consumer decodes one shape.
type Response struct {
	ID               uuid.UUID    `json:"id"`
	CompanyID        uuid.UUID    `json:"companyId"`
	UploadedByUserID uuid.UUID    `json:"uploadedByUserId"`
	Filename         string       `json:"filename"`
	ContentType      string       `json:"contentType"`
	SizeBytes        int64        `json:"sizeBytes"`
	Status           PublicStatus `json:"status"`
	CreatedAt        time.Time    `json:"createdAt"`
	UpdatedAt        time.Time    `json:"updatedAt"`
	Revision         int64        `json:"revision"`
}

// Response carries a caller-supplied filename and the identifiers of the row
// it projects, so it prints as a placeholder on every rendering path rather
// than spilling either into a log line.
func (Response) Format(state fmt.State, _ rune) {
	redact.TypeFormat(state, "Response")
}
func (Response) GoString() string     { return redact.TypeString("Response") }
func (Response) LogValue() slog.Value { return redact.TypeLogValue("Response") }
