package company

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The shared contracts/project fixtures still carry projectId and are being
// rewired with the handlers; until they land, the JSON contract is pinned
// inline here.
func TestCompanyResourceDTOsMatchPinnedJSONContracts(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 12, 1, 2, 3, 0, time.UTC)
	companyID := uuid.MustParse("0195eb55-3f66-7a2e-99d4-31a72edcc859")
	creatorID := uuid.MustParse("0195eb55-3f66-7d5a-9d35-0ff2a4a8cfad")
	daemonID := "daemon-0195eb55"
	daemonName := "Ada's MacBook"
	tests := []struct {
		name     string
		want     string
		response ResourceResponse
	}{
		{
			name: "local",
			want: `{
				"id": "0195eb55-3f66-7af3-865f-e518d22198c6",
				"companyId": "0195eb55-3f66-7a2e-99d4-31a72edcc859",
				"resourceType": "local_directory",
				"resourceRef": {"daemonId": "daemon-0195eb55", "daemonName": "Ada's MacBook"},
				"daemonStatus": "online",
				"daemonLastSeenAt": "2026-07-12T01:02:03Z",
				"label": "Workspace",
				"position": 20,
				"metadata": {},
				"createdBy": {
					"id": "0195eb55-3f66-7d5a-9d35-0ff2a4a8cfad",
					"kind": "user",
					"name": "Ada",
					"avatarUrl": null
				},
				"createdAt": "2026-07-12T01:02:03Z",
				"updatedAt": "2026-07-12T01:02:03Z",
				"revision": 1
			}`,
			response: ResourceResponse{
				ID:               uuid.MustParse("0195eb55-3f66-7af3-865f-e518d22198c6"),
				CompanyID:        companyID,
				ResourceType:     ResourceTypeLocalDirectory,
				ResourceRef:      ResourceRefResponse{DaemonID: daemonID, DaemonName: daemonName},
				DaemonStatus:     DaemonStatusOnline,
				DaemonLastSeenAt: &now,
				Label:            "Workspace",
				Position:         20,
				Metadata:         json.RawMessage(`{}`),
				CreatedBy: ActorRef{
					ID: creatorID, Kind: ActorTypeUser, DisplayName: "Ada",
				},
				CreatedAt: now,
				UpdatedAt: now,
				Revision:  1,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gotJSON, err := json.Marshal(test.response)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if json.Unmarshal([]byte(test.want), &want) != nil || json.Unmarshal(gotJSON, &got) != nil ||
				!reflect.DeepEqual(got, want) {
				t.Fatalf("contract drifted\ngot: %s\nwant: %s", gotJSON, test.want)
			}
		})
	}
}

func TestCompanyResourceDTOUsesClosedTypedRefsAndRedactsLocalPath(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 12, 1, 2, 3, 0, time.UTC)
	base := ResourceResponse{
		ID:        uuid.Must(uuid.NewV7()),
		CompanyID: uuid.Must(uuid.NewV7()),
		Label:     "Repository",
		Metadata:  json.RawMessage(`{}`),
		CreatedBy: ActorRef{
			ID: uuid.Must(uuid.NewV7()), Kind: ActorTypeUser, DisplayName: "Owner",
		},
		CreatedAt: now,
		UpdatedAt: now,
		Revision:  1,
	}
	daemonID := "daemon-" + uuid.Must(uuid.NewV7()).String()
	local := base
	local.ResourceType = ResourceTypeLocalDirectory
	local.ResourceRef = ResourceRefResponse{DaemonID: daemonID, DaemonName: "Owner laptop"}
	local.DaemonStatus = DaemonStatusOnline
	local.DaemonLastSeenAt = &now
	encoded, err := json.Marshal(local)
	if err != nil {
		t.Fatalf("Marshal(local Company resource) error = %v", err)
	}
	if strings.Contains(string(encoded), "path") ||
		!strings.Contains(
			string(encoded),
			`"resourceRef":{"daemonId":"`+daemonID+`","daemonName":"Owner laptop"}`,
		) ||
		!strings.Contains(string(encoded), `"metadata":{}`) {
		t.Fatalf("local Company resource leaks or drifts = %s", encoded)
	}
}

func TestNormalizeLocalCompanyResourceRequiresBoundedAbsolutePathAndEmptyMetadata(t *testing.T) {
	t.Parallel()
	input := CreateLocalDirectoryResourceInput{
		DaemonID: "daemon-1",
		Path:     "/Users/owner/workspace",
		Label:    "  Workspace  ",
	}
	got, err := normalizeCreateLocalDirectoryResourceInput(input)
	if err != nil || got.Label != "Workspace" || got.Path != input.Path || got.DaemonID != "daemon-1" {
		t.Fatalf("normalize local input = %+v, %v", got, err)
	}
	for _, path := range []string{"relative/path", " /absolute/path", "/absolute/path\x00", "/" + strings.Repeat("a", 4096)} {
		candidate := input
		candidate.Path = path
		if _, err := normalizeCreateLocalDirectoryResourceInput(
			candidate,
		); !errors.Is(
			err,
			ErrResourceInvalidInput,
		) {
			t.Fatalf("normalize local path %q error = %v, want ErrResourceInvalidInput", path, err)
		}
	}
}

func TestNormalizeCompanyResourceUpdateBoundsLabelAndPosition(t *testing.T) {
	t.Parallel()
	label := "  Renamed  "
	position := int32(7)
	got, err := normalizeResourceUpdateInput(ResourceUpdateInput{
		ExpectedRevision: 2, Label: &label, Position: &position,
	})
	if err != nil || got.Label == nil || *got.Label != "Renamed" {
		t.Fatalf("normalizeResourceUpdateInput() = %+v, %v", got, err)
	}
	// Two rules this function no longer owns are not listed here. The revision
	// range is `binding:"revision"` on updateCompanyResourceRequest and
	// deleteCompanyResourceRequest, exercised by the handler package's
	// TestRevisionBindingRejectsRevisionsOutsideTheRange. That a patch has to
	// change something is `required_without=Position` on the label, exercised
	// by TestCrossFieldRulesHoldThroughBinding. What is left is the pair no
	// tag can state: a rune count, which `max` on a string reads as bytes, and
	// a ceiling on a position.
	blank, overlong := "   ", strings.Repeat("x", maxResourceLabelRunes+1)
	tooHigh, negative := int32(1_000_001), int32(-1)
	for _, input := range []ResourceUpdateInput{
		{ExpectedRevision: 1, Label: &blank},
		{ExpectedRevision: 1, Label: &overlong},
		{ExpectedRevision: 1, Position: &tooHigh},
		{ExpectedRevision: 1, Position: &negative},
	} {
		if _, err := normalizeResourceUpdateInput(input); !errors.Is(err, ErrResourceInvalidInput) {
			t.Fatalf("normalize invalid update %+v error = %v", input, err)
		}
	}
}
