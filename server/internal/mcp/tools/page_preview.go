package tools

import (
	"context"
	"fmt"
	"net/url"
)

// Preview generations are temporary source buffers. The server independently
// admits the live Run for every call and never applies a buffer to a Page.
type PageSourcePreviewStartInput struct {
	PageID          string `json:"pageId"          jsonschema:"Destination Page ID from page_read. Document-thread Runs may use only their originating Page."`
	DraftKey        string `json:"draftKey"        jsonschema:"Stable unique name for this authoring attempt. Reuse it for an exact retry; choose a new name for a separate generation."`
	ProtocolVersion int    `json:"protocolVersion" jsonschema:"Preview protocol version; currently 1."`
	LanguageVersion int    `json:"languageVersion" jsonschema:"Source language version; currently 1."`
}

type PageSourcePreviewReadInput struct {
	PageID       string `json:"pageId"       jsonschema:"The destination Page used when starting this generation."`
	GenerationID string `json:"generationId" jsonschema:"Exact id returned by page_source_preview_start."`
}

type PageSourcePreviewPatchInput struct {
	PageID           string `json:"pageId"`
	GenerationID     string `json:"generationId"`
	ProtocolVersion  int    `json:"protocolVersion"  jsonschema:"Currently 1."`
	LanguageVersion  int    `json:"languageVersion"  jsonschema:"Currently 1."`
	ExpectedRevision int64  `json:"expectedRevision" jsonschema:"Latest acknowledged revision. Read the generation after a conflict before making a new edit."`
	Start            int    `json:"start"            jsonschema:"UTF-8 byte offset into the current source; never a character index."`
	DeleteBytes      int    `json:"deleteBytes"      jsonschema:"Number of UTF-8 bytes to replace; both edit boundaries must be between characters."`
	Text             string `json:"text"             jsonschema:"Replacement source, at most 65536 UTF-8 bytes. Entire generation at most 262144 bytes."`
	Discard          bool   `json:"discard"          jsonschema:"Close the generation and erase its source; requires start=0, deleteBytes=0 and empty text."`
}

type PageSourcePreviewResult struct {
	Generation map[string]any `json:"generation"`
}

func (s *DefaultPageToolService) StartSourcePreview(
	ctx context.Context,
	in PageSourcePreviewStartInput,
) (PageSourcePreviewResult, error) {
	if in.DraftKey == "" || len(in.DraftKey) > 200 {
		return PageSourcePreviewResult{}, fmt.Errorf("draftKey must contain 1 to 200 bytes")
	}
	key, err := runRequestKey("page_source_preview_start", s.run, in)
	if err != nil {
		return PageSourcePreviewResult{}, err
	}
	request := struct {
		RequestKey      string `json:"requestKey"`
		ProtocolVersion int    `json:"protocolVersion"`
		LanguageVersion int    `json:"languageVersion"`
	}{key, in.ProtocolVersion, in.LanguageVersion}
	var result map[string]any
	if err := s.api.PostRunJSON(ctx, s.pagePath(in.PageID)+"/source-previews", key, request, &result); err != nil {
		return PageSourcePreviewResult{}, fmt.Errorf("start source preview: %w", err)
	}
	return PageSourcePreviewResult{Generation: result}, nil
}

func (s *DefaultPageToolService) ReadSourcePreview(
	ctx context.Context,
	in PageSourcePreviewReadInput,
) (PageSourcePreviewResult, error) {
	var result map[string]any
	if err := s.api.FetchRunJSON(
		ctx,
		s.pagePath(in.PageID)+"/source-previews/"+url.PathEscape(in.GenerationID),
		&result,
	); err != nil {
		return PageSourcePreviewResult{}, fmt.Errorf("read source preview: %w", err)
	}
	return PageSourcePreviewResult{Generation: result}, nil
}

func (s *DefaultPageToolService) PatchSourcePreview(
	ctx context.Context,
	in PageSourcePreviewPatchInput,
) (PageSourcePreviewResult, error) {
	key, err := runRequestKey("page_source_preview_patch", s.run, in)
	if err != nil {
		return PageSourcePreviewResult{}, err
	}
	request := struct {
		RequestKey       string `json:"requestKey"`
		ProtocolVersion  int    `json:"protocolVersion"`
		LanguageVersion  int    `json:"languageVersion"`
		ExpectedRevision int64  `json:"expectedRevision"`
		Start            int    `json:"start"`
		DeleteBytes      int    `json:"deleteBytes"`
		Text             string `json:"text"`
		Discard          bool   `json:"discard"`
	}{key, in.ProtocolVersion, in.LanguageVersion, in.ExpectedRevision, in.Start, in.DeleteBytes, in.Text, in.Discard}
	var result map[string]any
	if err := s.api.PostRunJSON(
		ctx,
		s.pagePath(in.PageID)+"/source-previews/"+url.PathEscape(in.GenerationID),
		key,
		request,
		&result,
	); err != nil {
		return PageSourcePreviewResult{}, fmt.Errorf("patch source preview: %w", err)
	}
	return PageSourcePreviewResult{Generation: result}, nil
}
