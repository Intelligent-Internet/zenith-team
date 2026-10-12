package tools

import "context"

type ArtifactInspectInput struct {
	ArtifactID string `json:"artifactId" jsonschema:"Canonical immutable output ID from an attached artifact reference."`
}
type ArtifactFetchInput struct {
	ArtifactID string `json:"artifactId"       jsonschema:"Canonical immutable output ID from an attached artifact reference."`
	ItemID     string `json:"itemId,omitempty" jsonschema:"Item ID returned by artifact_inspect. Omit to fetch the primary file."`
}
type ArtifactItem struct {
	ID             string `json:"id"`
	AssetVersionID string `json:"assetVersionId"`
	ContentType    string `json:"contentType"`
	SizeBytes      int64  `json:"sizeBytes"`
	Ordinal        int    `json:"ordinal"`
	Role           string `json:"role"`
	Label          string `json:"label"`
	DisplayName    string `json:"displayName"`
}

// ArtifactManifest describes an immutable Output, not the source workspace.
// Items pin uploaded Asset Versions; document destinations pin saved evidence.
type ArtifactManifest struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	Title           string         `json:"title"`
	Summary         string         `json:"summary"`
	Items           []ArtifactItem `json:"items"`
	URL             *string        `json:"url,omitempty"`
	CanvasURL       *string        `json:"canvasUrl,omitempty"`
	PresentationURL *string        `json:"presentationUrl,omitempty"`
}
type ArtifactFetchResult struct {
	ArtifactID     string `json:"artifactId"`
	ItemID         string `json:"itemId"`
	AssetVersionID string `json:"assetVersionId"`
	LocalPath      string `json:"localPath"`
	ContentType    string `json:"contentType"`
	SizeBytes      int64  `json:"sizeBytes"`
	SHA256         string `json:"sha256"`
}
type ArtifactChannelService interface {
	InspectArtifact(context.Context, ArtifactInspectInput) (ArtifactManifest, error)
	FetchArtifact(context.Context, ArtifactFetchInput) (ArtifactFetchResult, error)
}

var _ ArtifactChannelService = (*DefaultArtifactChannelService)(nil)

type DefaultArtifactChannelService struct{ channel *DefaultOutputChannelService }

func NewArtifactChannelService(endpoint, capability, runID string) (*DefaultArtifactChannelService, error) {
	channel, err := NewOutputChannelService(endpoint, capability, runID)
	if err != nil {
		return nil, err
	}
	return &DefaultArtifactChannelService{channel: channel}, nil
}

func (s *DefaultArtifactChannelService) InspectArtifact(
	ctx context.Context,
	input ArtifactInspectInput,
) (ArtifactManifest, error) {
	var result ArtifactManifest
	err := s.channel.call(ctx, "/runs/"+s.channel.runID+"/artifact-inspect", input, &result)
	return result, err
}

func (s *DefaultArtifactChannelService) FetchArtifact(
	ctx context.Context,
	input ArtifactFetchInput,
) (ArtifactFetchResult, error) {
	var result ArtifactFetchResult
	err := s.channel.call(ctx, "/runs/"+s.channel.runID+"/artifact-fetch", input, &result)
	return result, err
}
