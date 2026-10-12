package execution

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestNativePresentationUsesTextArtifact(t *testing.T) {
	id, attachment := uuid.New(), uuid.New()
	for _, test := range []struct {
		name         string
		artifactType ArtifactType
		wantError    bool
	}{
		{"text", ArtifactTypeText, false}, {"media", ArtifactTypeMedia, true}, {"file", ArtifactTypeFile, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeDeliverablePresentation(
				DeliverablePresentRequest{
					Type:           test.artifactType,
					PresentationID: &id,
					Title:          "Project result",
					Summary:        "The reviewed overview and exact evidence.",
					Items: []DeliverablePresentItemRequest{
						{
							Role:         DeliverableItemRolePrimary,
							Label:        "Presentation",
							AttachmentID: &attachment,
						},
					},
				},
			)
			if (err != nil) != test.wantError {
				t.Fatalf("normalization = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestNormalizeDeliverablePresentationFreezesOnePrimaryOrderedItemSet(t *testing.T) {
	t.Parallel()
	primaryAssetVersion := uuid.Must(uuid.NewV7())
	attachment := uuid.Must(uuid.NewV7())
	input := DeliverablePresentRequest{
		Title:           "  Launch site  ",
		Summary:         "  Responsive release candidate.  ",
		SubmissionNotes: deliverableStringPointer("  Tested on desktop and mobile.  "),
		Items: []DeliverablePresentItemRequest{
			{
				Role: DeliverableItemRolePrimary, Label: "  Report  ",
				AssetVersionID: &primaryAssetVersion,
			},
			{
				Role: DeliverableItemRoleFinal, Label: "  QA report  ",
				AttachmentID: &attachment,
			},
		},
	}

	got, err := normalizeDeliverablePresentation(input)
	if err != nil {
		t.Fatalf("normalizeDeliverablePresentation() error = %v", err)
	}
	if got.Title != "Launch site" || got.Summary != "Responsive release candidate." ||
		got.SubmissionNotes == nil || *got.SubmissionNotes != "Tested on desktop and mobile." ||
		got.Items[0].ordinal != 0 || got.Items[1].ordinal != 1 || got.Items[1].Role != "final" {
		t.Fatalf("normalizeDeliverablePresentation() = %+v", got)
	}
	if len(got.fingerprint) != 64 {
		t.Fatalf("fingerprint = %q, want SHA-256 hex", got.fingerprint)
	}

	replay, err := normalizeDeliverablePresentation(input)
	if err != nil || replay.fingerprint != got.fingerprint {
		t.Fatalf("replay fingerprint = %q, %v; want %q", replay.fingerprint, err, got.fingerprint)
	}
	input.Items[1].Label = "Appendix"
	changed, err := normalizeDeliverablePresentation(input)
	if err != nil || changed.fingerprint == got.fingerprint {
		t.Fatalf("changed fingerprint = %q, %v; want distinct", changed.fingerprint, err)
	}
}

func TestNormalizeDeliverablePresentationRejectsInvalidItemSets(t *testing.T) {
	t.Parallel()
	assetVersion := uuid.Must(uuid.NewV7())
	valid := DeliverablePresentRequest{
		Title: "Result", Summary: "Complete result.",
		Items: []DeliverablePresentItemRequest{{
			Role: DeliverableItemRolePrimary, Label: "Report", AssetVersionID: &assetVersion,
		}},
	}
	for _, test := range []struct {
		name   string
		mutate func(*DeliverablePresentRequest)
	}{
		{name: "empty", mutate: func(input *DeliverablePresentRequest) { input.Items = nil }},
		{name: "no primary", mutate: func(input *DeliverablePresentRequest) {
			input.Items[0].Role = DeliverableItemRoleSupporting
		}},
		{name: "two primary", mutate: func(input *DeliverablePresentRequest) {
			input.Items = append(input.Items, input.Items[0])
		}},
		{name: "two targets", mutate: func(input *DeliverablePresentRequest) {
			attachment := uuid.Must(uuid.NewV7())
			input.Items[0].AttachmentID = &attachment
		}},
		{name: "no target", mutate: func(input *DeliverablePresentRequest) {
			input.Items[0].AssetVersionID = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			input.Items = append([]DeliverablePresentItemRequest(nil), valid.Items...)
			test.mutate(&input)
			if _, err := normalizeDeliverablePresentation(input); !errors.Is(
				err,
				ErrDeliverableInvalidInput,
			) {
				t.Fatalf(
					"normalizeDeliverablePresentation() error = %v, want ErrDeliverableInvalidInput",
					err,
				)
			}
		})
	}
}

func TestNormalizeDeliverablePresentationAcceptsAppCommandsWithoutItems(t *testing.T) {
	t.Parallel()
	got, err := normalizeDeliverablePresentation(DeliverablePresentRequest{
		Type: ArtifactTypeApp, Title: "  App  ", Summary: "  Ready.  ",
		App: &DeliverablePresentAppRequest{Commands: []AppPreviewCommand{{
			Command: "npm", Args: []string{"run", "dev"}, WorkingDirectory: "web", Port: 5173,
		}}},
	})
	if err != nil || got.previewURL != "" || got.App == nil ||
		len(got.App.Commands) != 1 || len(got.Items) != 0 || len(got.fingerprint) != 64 {
		t.Fatalf("normalizeDeliverablePresentation(app) = %+v, %v", got, err)
	}
}

func TestNormalizeDeliverablePresentationRejectsMismatchedAppTargets(t *testing.T) {
	t.Parallel()
	assetVersionID := uuid.Must(uuid.NewV7())
	for _, request := range []DeliverablePresentRequest{
		{Type: ArtifactTypeApp, Title: "App", Summary: "Ready."},
		{
			Type: ArtifactTypeApp, Title: "App", Summary: "Ready.",
			App: &DeliverablePresentAppRequest{Commands: []AppPreviewCommand{{Command: "npm", Port: 4173}}},
			Items: []DeliverablePresentItemRequest{{
				Role: DeliverableItemRolePrimary, Label: "App", AssetVersionID: &assetVersionID,
			}},
		},
		{
			Type: ArtifactTypeFile, Title: "File", Summary: "Ready.",
			App: &DeliverablePresentAppRequest{Commands: []AppPreviewCommand{{Command: "npm", Port: 4173}}},
		},
	} {
		if _, err := normalizeDeliverablePresentation(request); !errors.Is(err, ErrDeliverableInvalidInput) {
			t.Fatalf("normalizeDeliverablePresentation(%+v) error = %v", request, err)
		}
	}
}

func deliverableStringPointer(value string) *string { return &value }

func TestArtifactTypePreservesFileReplayAndDistinguishesKinds(t *testing.T) {
	t.Parallel()
	attachmentID := uuid.New()
	input := DeliverablePresentRequest{
		Title:   "App",
		Summary: "Ready.",
		Items: []DeliverablePresentItemRequest{
			{Role: DeliverableItemRolePrimary, Label: "App", AttachmentID: &attachmentID},
		},
	}
	original, err := normalizeDeliverablePresentation(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Type = ArtifactTypeFile
	file, err := normalizeDeliverablePresentation(input)
	if err != nil || file.Type != ArtifactTypeFile || file.fingerprint != original.fingerprint {
		t.Fatalf("file replay = %+v, %v", file, err)
	}
	for _, artifactType := range []ArtifactType{
		ArtifactTypeMedia,
		ArtifactTypeText,
	} {
		input.Type = artifactType
		artifact, err := normalizeDeliverablePresentation(input)
		if err != nil || artifact.fingerprint == file.fingerprint {
			t.Fatalf("%s fingerprint = %q, %v", artifactType, artifact.fingerprint, err)
		}
	}
	app, err := normalizeDeliverablePresentation(DeliverablePresentRequest{
		Type: ArtifactTypeApp, Title: "App", Summary: "Ready.",
		App: &DeliverablePresentAppRequest{Commands: []AppPreviewCommand{{Command: "npm", Port: 5173}}},
	})
	if err != nil || app.fingerprint == file.fingerprint {
		t.Fatalf("app fingerprint = %q, %v", app.fingerprint, err)
	}
	input.Type = "files"
	if _, err := normalizeDeliverablePresentation(input); !errors.Is(err, ErrDeliverableInvalidInput) {
		t.Fatalf("invalid type error = %v", err)
	}
}

func TestArtifactTypeRequiresMatchingPrimaryContent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		artifactType ArtifactType
		contentType  string
		want         bool
	}{
		{artifactType: ArtifactTypeApp, contentType: "application/zip", want: true},
		{artifactType: ArtifactTypeApp, contentType: "text/html"},
		{artifactType: ArtifactTypeMedia, contentType: "image/png", want: true},
		{artifactType: ArtifactTypeMedia, contentType: "video/mp4", want: true},
		{artifactType: ArtifactTypeMedia, contentType: "audio/mpeg", want: true},
		{artifactType: ArtifactTypeMedia, contentType: "audio/wav", want: true},
		{artifactType: ArtifactTypeMedia, contentType: " Audio/MP4; codecs=mp4a.40.2 ", want: true},
		{artifactType: ArtifactTypeMedia, contentType: "application/pdf"},
		{artifactType: ArtifactTypeMedia, contentType: "application/octet-stream"},
		{artifactType: ArtifactTypeText, contentType: "text/markdown", want: true},
		{artifactType: ArtifactTypeText, contentType: "application/json", want: true},
		{artifactType: ArtifactTypeText, contentType: "application/octet-stream", want: true},
		{artifactType: ArtifactTypeText, contentType: "image/png"},
		{artifactType: ArtifactTypeFile, contentType: "application/octet-stream", want: true},
	} {
		t.Run(string(test.artifactType)+"/"+test.contentType, func(t *testing.T) {
			t.Parallel()
			if got := artifactPrimaryContentMatches(test.artifactType, test.contentType); got != test.want {
				t.Fatalf("artifactPrimaryContentMatches() = %v, want %v", got, test.want)
			}
		})
	}
}
