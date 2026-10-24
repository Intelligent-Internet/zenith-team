package tools

import (
	"context"
	"fmt"
	"net/url"
)

type PageEditInput struct {
	SupersedesID *string         `json:"supersedesId,omitempty" jsonschema:"Unresolved prior change-set ID this revision replaces. Read current shared content and propose only remaining changes; accepted suggestions are already shared."`
	PageID       string          `json:"pageId"                 jsonschema:"Stable Page ID returned by project_read or page_create. The Project overview is edited using its overviewPageId."`
	Sequence     int64           `json:"sequence"               jsonschema:"Base revision sequence from page_read. Keep this sequence and its block hashes together. Proposals are compared with the current page at review; concurrent edits do not require you to stop. Read again only to intentionally revise the base."`
	Summary      string          `json:"summary"                jsonschema:"Short human-readable explanation of the contribution."`
	Operations   []PageOperation `json:"operations,omitempty"   jsonschema:"At most 32 focused insert/update/remove/move operations. All changes become suggestions requiring human acceptance. If validation rejects an operation, correct the indicated structure and retry; no proposal was saved."`
	Theme        *string         `json:"theme,omitempty"        jsonschema:"Optional Page theme: default, editorial or compact. Submit theme changes separately, without operations. Changes presentation only; preserve the current theme unless a new one helps the requested result."`
}
type PageOperation struct {
	Source          *string    `json:"source,omitempty"          jsonschema:"Versioned Zenith source for exactly one subtree; mutually exclusive with content. Updates preserve every block ID using the Native escape from page_read. Load zenith-intelligent-ui before authoring."`
	LanguageVersion int        `json:"languageVersion,omitempty" jsonschema:"Required with source; currently 1."`
	Type            string     `json:"type"                      jsonschema:"insert adds one subtree; update replaces a target subtree without changing its block type; remove deletes a subtree; move relocates an existing subtree."`
	BlockID         string     `json:"blockId,omitempty"         jsonschema:"Existing target attrs.blockId from the destination read; required for update, remove and move. Omit for insert."`
	ParentID        string     `json:"parentId,omitempty"        jsonschema:"Structural parent attrs.blockId for insert or move. Omit for document-level paragraphs and headings, including content under a section heading. Headings and paragraphs are not containers; never use their IDs as parentId. Choose a parent that accepts the block type, such as a list for listItem or table for tableRow."`
	AfterID         string     `json:"afterId,omitempty"         jsonschema:"Place an inserted or moved subtree after this existing sibling in the destination parent. Mutually exclusive with beforeId; omit both to append."`
	BeforeID        string     `json:"beforeId,omitempty"        jsonschema:"Place an inserted or moved subtree before this existing sibling in the destination parent. Mutually exclusive with afterId; omit both to append."`
	ExpectedHash    string     `json:"expectedHash,omitempty"    jsonschema:"Exact target hash from the destination read, required except for insert."`
	Content         *PageBlock `json:"content,omitempty"         jsonschema:"One native block subtree: type, attrs, content, text and marks. Inserts omit attrs.blockId throughout the entire subtree; the server allocates IDs. Updates retain the target's type and attrs.blockId equal to the operation's blockId, preserving all existing descendant IDs. Insert new descendant blocks with separate insert operations. Put reference fields in attrs: artifactReference uses versionId/artifactId/label, format live for a current native Page or pinned for exact evidence, and display link/card/section/expanded; taskReference uses taskId; taskCollection uses filter and display list/gallery/table/board; outputCollection uses filter and display list/gallery/table/expanded; workflowReference uses a real workflowKey and display link/card/expanded; images and media use display card/section/expanded. Reference evidence rather than duplicating it."`
}

// PageBlock makes the replacement subtree explicit in the tool schema. Child
// nodes retain the native document shape, including marks and reference attrs.
type PageBlock struct {
	Type    string           `json:"type"              jsonschema:"Native block type from page_read, such as paragraph or heading. Required; an update preserves the target type."`
	Attrs   map[string]any   `json:"attrs,omitempty"   jsonschema:"Copy the target attrs from page_read for updates, including blockId. Inserts omit blockId."`
	Content []map[string]any `json:"content,omitempty" jsonschema:"Native child nodes, each with a type. Text is {type: text, text: ...}; line breaks are {type: hardBreak}. Preserve descendant block IDs on updates."`
	Text    string           `json:"text,omitempty"`
	Marks   []map[string]any `json:"marks,omitempty"`
}

type ProjectResult struct {
	Project map[string]any `json:"project"`
}
type PageResult struct {
	Page map[string]any `json:"page"`
}
type SourceResult struct {
	Source map[string]any `json:"source"`
}
type PagePreviewResult struct {
	Preview map[string]any `json:"preview"`
}

type PageCreateInput struct {
	Source          *string        `json:"source,omitempty"          jsonschema:"Zenith Markdown/component source; mutually exclusive with markdown and content. Load zenith-intelligent-ui for the supported grammar."`
	LanguageVersion int            `json:"languageVersion,omitempty" jsonschema:"Required with source; currently 1."`
	Title           string         `json:"title"                     jsonschema:"Concise result title, such as Autumn campaign or Audience research."`
	Content         map[string]any `json:"content,omitempty"         jsonschema:"Native document with type doc and content array. Blocks have type, optional attrs, content; text nodes have type text and text. Omit blockId on new blocks; the server allocates IDs. Use artifactReference, taskReference, taskCollection and workflowReference for real sources and work."`
	Markdown        *string        `json:"markdown,omitempty"        jsonschema:"Ordinary Markdown for prose, headings, lists, tables and links. Supply either markdown or native content, never both. Use native blocks for live Task/Workflow references and rich layouts."`
	Theme           *string        `json:"theme,omitempty"           jsonschema:"Optional curated theme: default, editorial or compact. Omit for the standard Page presentation."`
}
type PageReadInput struct {
	ChangeID string `json:"changeId,omitempty" jsonschema:"For page_preview, the exact change-set ID to preview as a proposed result; omit to capture shared content."`
	PageID   string `json:"pageId"             jsonschema:"Stable Page ID from project_read or page_create; overviewPageId selects the Project home Page."`
}
type PageEditResult struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Sequence int64  `json:"sequence"`
}
type SourceReadInput struct {
	VersionID string `json:"versionId"         jsonschema:"Exact permitted saved versionId returned by project_read or page_read sources. Unlike a pageId, it selects immutable evidence."`
	BlockID   string `json:"blockId,omitempty" jsonschema:"Optional source block or heading section. Reads at most 64 KiB; whole-page reads include the first eight blocks."`
}
type PagePresentInput struct {
	SubmissionNotes string `json:"submissionNotes,omitempty"`
	ChangeID        string `json:"changeId,omitempty"`
	PageID          string `json:"pageId"                    jsonschema:"Stable ID of the Page reviewed with page_preview; any Page in this Task's Project can become its result."`
	Sequence        int64  `json:"sequence"                  jsonschema:"Acknowledged sequence from page_preview."`
	Fingerprint     string `json:"fingerprint"               jsonschema:"Exact fingerprint from page_preview. Review its issues and content first."`
	Summary         string `json:"summary"                   jsonschema:"What this Page result demonstrates; do not claim review approval."`
}
type pageAPI interface {
	FetchRunJSON(context.Context, string, any) error
	PostRunJSON(context.Context, string, string, any, any) error
}
type PageToolService interface {
	StartSourcePreview(context.Context, PageSourcePreviewStartInput) (PageSourcePreviewResult, error)
	ReadSourcePreview(context.Context, PageSourcePreviewReadInput) (PageSourcePreviewResult, error)
	PatchSourcePreview(context.Context, PageSourcePreviewPatchInput) (PageSourcePreviewResult, error)
	ReadProject(context.Context) (ProjectResult, error)
	CreatePage(context.Context, PageCreateInput) (PageResult, error)
	ReadPage(context.Context, PageReadInput) (PageResult, error)
	EditPage(context.Context, PageEditInput) (PageEditResult, error)
	ReadSource(context.Context, SourceReadInput) (SourceResult, error)
	PreviewPage(context.Context, PageReadInput) (PagePreviewResult, error)
}

var _ PageToolService = (*DefaultPageToolService)(nil)

type DefaultPageToolService struct {
	api pageAPI
	run RunContext
}

func NewPageToolService(api pageAPI, run RunContext) (*DefaultPageToolService, error) {
	if err := validateRunContext(run); err != nil {
		return nil, err
	}
	return &DefaultPageToolService{api: api, run: run}, nil
}

func (s *DefaultPageToolService) runPath() string {
	return "/companies/" + url.PathEscape(s.run.CompanyID) + "/runs/" + url.PathEscape(s.run.RunID)
}

func (s *DefaultPageToolService) pagePath(id string) string {
	return s.runPath() + "/agent-pages/" + url.PathEscape(id)
}

func (s *DefaultPageToolService) ReadProject(ctx context.Context) (ProjectResult, error) {
	var result map[string]any
	err := s.api.FetchRunJSON(ctx, s.runPath()+"/agent-project", &result)
	return ProjectResult{Project: result}, err
}

func (s *DefaultPageToolService) CreatePage(ctx context.Context, in PageCreateInput) (PageResult, error) {
	key, err := runRequestKey("page_create", s.run, in)
	if err != nil {
		return PageResult{}, err
	}
	request := struct {
		PageCreateInput
		RequestKey string `json:"requestKey"`
	}{in, key}
	var result map[string]any
	if err := s.api.PostRunJSON(ctx, s.runPath()+"/agent-pages", key, request, &result); err != nil {
		return PageResult{}, fmt.Errorf("create Page: %w", err)
	}
	return PageResult{Page: result}, nil
}

func (s *DefaultPageToolService) ReadPage(ctx context.Context, in PageReadInput) (PageResult, error) {
	var result map[string]any
	err := s.api.FetchRunJSON(ctx, s.pagePath(in.PageID), &result)
	return PageResult{Page: result}, err
}

func (s *DefaultPageToolService) EditPage(ctx context.Context, in PageEditInput) (PageEditResult, error) {
	for index, op := range in.Operations {
		if op.Type != "insert" && op.Type != "update" {
			continue
		}
		if op.Source != nil {
			if op.Content != nil || op.LanguageVersion != 1 {
				return PageEditResult{}, fmt.Errorf(
					"operations[%d] requires source with languageVersion 1 and no content",
					index,
				)
			}
			continue
		}
		if op.Content == nil || op.Content.Type == "" {
			return PageEditResult{}, fmt.Errorf(
				"operations[%d].content must be a complete native block with type, attrs and content; copy the block from page_read and edit its text",
				index,
			)
		}
		if op.Type == "update" {
			blockID, _ := op.Content.Attrs["blockId"].(string)
			if op.BlockID == "" || blockID != op.BlockID {
				return PageEditResult{}, fmt.Errorf(
					"operations[%d].content.attrs.blockId must match blockId; copy the target type and attrs from page_read, preserving descendant IDs",
					index,
				)
			}
		}
	}
	key, err := runRequestKey("page_edit", s.run, in)
	if err != nil {
		return PageEditResult{}, err
	}
	request := struct {
		Sequence     int64           `json:"sequence"`
		Summary      string          `json:"summary"`
		Operations   []PageOperation `json:"operations,omitempty"`
		Theme        *string         `json:"theme,omitempty"`
		RequestKey   string          `json:"requestKey"`
		SupersedesID *string         `json:"supersedesId,omitempty"`
	}{in.Sequence, in.Summary, in.Operations, in.Theme, key, in.SupersedesID}
	var result PageEditResult
	if err := s.api.PostRunJSON(ctx, s.pagePath(in.PageID), key, request, &result); err != nil {
		return result, fmt.Errorf("contribute to Page: %w", err)
	}
	return result, nil
}

func (s *DefaultPageToolService) ReadSource(ctx context.Context, in SourceReadInput) (SourceResult, error) {
	var result map[string]any
	query := url.Values{"versionId": {in.VersionID}}
	if in.BlockID != "" {
		query.Set("blockId", in.BlockID)
	}
	err := s.api.FetchRunJSON(ctx, s.runPath()+"/agent-sources?"+query.Encode(), &result)
	return SourceResult{Source: result}, err
}

func (s *DefaultPageToolService) PreviewPage(ctx context.Context, in PageReadInput) (PagePreviewResult, error) {
	var result map[string]any
	path := s.pagePath(in.PageID) + "/presentation-preview"
	if in.ChangeID != "" {
		path += "?" + url.Values{"changeId": {in.ChangeID}}.Encode()
	}
	err := s.api.FetchRunJSON(ctx, path, &result)
	return PagePreviewResult{Preview: result}, err
}

func presentNativePage(ctx context.Context, api interface {
	PostRunJSON(context.Context, string, string, any, any) error
}, run RunContext, in PagePresentInput,
) (PresentOutputResult, error) {
	path := "/companies/" + url.PathEscape(run.CompanyID) + "/runs/" + url.PathEscape(run.RunID)
	capture := struct {
		ChangeID    string `json:"changeId,omitempty"`
		PageID      string `json:"pageId"`
		Sequence    int64  `json:"sequence"`
		Fingerprint string `json:"fingerprint"`
	}{in.ChangeID, in.PageID, in.Sequence, in.Fingerprint}
	key, err := runRequestKey("page_capture", run, capture)
	if err != nil {
		return PresentOutputResult{}, err
	}
	request := struct {
		ChangeID    string `json:"changeId,omitempty"`
		RequestKey  string `json:"requestKey"`
		Sequence    int64  `json:"sequence"`
		Fingerprint string `json:"fingerprint"`
	}{in.ChangeID, key, in.Sequence, in.Fingerprint}
	var prepared struct {
		PresentationID string `json:"presentationId"`
		AttachmentID   string `json:"attachmentId"`
		Title          string `json:"title"`
	}
	if err = api.PostRunJSON(
		ctx,
		path+"/agent-pages/"+url.PathEscape(in.PageID)+"/presentations",
		key,
		request,
		&prepared,
	); err != nil {
		return PresentOutputResult{}, fmt.Errorf("capture Page result: %w", err)
	}
	title := []rune(prepared.Title)
	output := struct {
		presentOutputRequest
		PresentationID string `json:"presentationId"`
	}{
		presentOutputRequest: presentOutputRequest{
			Type:    "text",
			Title:   string(title[:min(len(title), maxPresentedOutputTitleRunes)]),
			Summary: in.Summary,
			Items: []presentOutputRequestItem{{
				Role:         "primary",
				Label:        string(title[:min(len(title), maxPresentedOutputLabelRunes)]),
				AttachmentID: prepared.AttachmentID,
			}},
		},
		PresentationID: prepared.PresentationID,
	}
	if in.SubmissionNotes != "" {
		output.SubmissionNotes = &in.SubmissionNotes
	}
	key, err = runRequestKey("present_output", run, output)
	if err != nil {
		return PresentOutputResult{}, err
	}
	// A Run presents its output on the server's own route, outside the
	// Company path; the Run channel forwards it because it names the capture.
	var result PresentOutputResult
	err = api.PostRunJSON(ctx, "/runs/"+url.PathEscape(run.RunID)+"/output", key, output, &result)
	return result, err
}
