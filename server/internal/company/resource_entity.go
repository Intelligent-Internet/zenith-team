package company

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const (
	maxResourceLabelRunes = 120
)

type ResourceField string

const (
	ResourceFieldLabel    ResourceField = "label"
	ResourceFieldPosition ResourceField = "position"
)

type CreateLocalDirectoryResourceInput struct {
	DaemonID string
	Path     string
	Label    string
	Position int32
}

type ResourceUpdateInput struct {
	ExpectedRevision int64
	Label            *string
	Position         *int32
}

type RevisionInput struct {
	ExpectedRevision int64
}

type ResourceListOptions struct {
	DaemonID *string
}

type ResourceCreateResult struct {
	Resource ResourceResponse
	Created  bool
}

type resourceRecord struct {
	Response  ResourceResponse
	LocalPath string
}

// companyResourceFromRow is the row-to-entity mapping for resources, and the
// only direction the generated resource type travels: in at the query, out as
// a resourceRecord.
func companyResourceFromRow(row generated.CompanyResource) resourceRecord {
	resource := companyResourceResponse(
		row,
		row.DaemonID,
		DaemonStatusUnregistered,
		nil,
		"Unknown",
		nil,
	)
	return resourceRecord{Response: resource, LocalPath: row.LocalPath}
}

func companyResourceResponse(
	row generated.CompanyResource,
	daemonName string,
	daemonStatus DaemonStatus,
	daemonLastSeenAt *time.Time,
	createdByDisplayName string,
	createdByAvatarURL *string,
) ResourceResponse {
	return ResourceResponse{
		ID:               row.ID,
		CompanyID:        row.CompanyID,
		ResourceType:     ResourceType(row.ResourceType),
		ResourceRef:      ResourceRefResponse{DaemonID: row.DaemonID, DaemonName: daemonName},
		DaemonStatus:     daemonStatus,
		DaemonLastSeenAt: daemonLastSeenAt,
		Label:            row.Label,
		Position:         row.Position,
		Metadata:         json.RawMessage(row.Metadata),
		CreatedBy: ActorRef{
			ID: row.CreatedByUserID, Kind: ActorTypeUser,
			DisplayName: createdByDisplayName, AvatarURL: createdByAvatarURL,
		},
		CreatedAt: row.CreatedAt.Time.UTC(),
		UpdatedAt: row.UpdatedAt.Time.UTC(),
		Revision:  row.Version,
	}
}

func normalizeCreateLocalDirectoryResourceInput(
	input CreateLocalDirectoryResourceInput,
) (CreateLocalDirectoryResourceInput, error) {
	input.DaemonID = protocol.TrimContractWhitespace(input.DaemonID)
	if input.DaemonID == "" || !validCanonicalAbsolutePath(input.Path) {
		return CreateLocalDirectoryResourceInput{}, ErrResourceInvalidInput
	}
	label, err := normalizeResourceLabel(input.Label)
	if err != nil || !validResourcePosition(input.Position) {
		return CreateLocalDirectoryResourceInput{}, ErrResourceInvalidInput
	}
	input.Label = label
	return input, nil
}

// normalizeResourceUpdateInput holds the two bounds the patch DTO cannot state
// in a tag: a label's rune count, which `max` would read as bytes, and a
// position's ceiling. That the patch has to change something is the binding's
// — `required_without=Position` on the label — and
// TestCrossFieldRulesHoldThroughBinding pins it there.
func normalizeResourceUpdateInput(input ResourceUpdateInput) (ResourceUpdateInput, error) {
	if input.Label != nil {
		label, err := normalizeResourceLabel(*input.Label)
		if err != nil {
			return ResourceUpdateInput{}, err
		}
		input.Label = &label
	}
	if input.Position != nil && !validResourcePosition(*input.Position) {
		return ResourceUpdateInput{}, ErrResourceInvalidInput
	}
	return input, nil
}

func normalizeResourceLabel(value string) (string, error) {
	value = protocol.TrimContractWhitespace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxResourceLabelRunes {
		return "", ErrResourceInvalidInput
	}
	return value, nil
}

func validResourcePosition(position int32) bool {
	return position >= 0 && position <= 1_000_000
}

func validCanonicalAbsolutePath(value string) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > 4096 ||
		protocol.TrimContractWhitespace(value) != value || containsControl(value) {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
