package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type pageAPIStub struct {
	title       string
	paths, keys []string
	requests    []json.RawMessage
}

func (s *pageAPIStub) FetchRunJSON(_ context.Context, path string, destination any) error {
	s.paths = append(s.paths, path)
	return json.Unmarshal([]byte(`{"sequence":7,"fingerprint":"reviewed"}`), destination)
}

func (s *pageAPIStub) PostRunJSON(_ context.Context, path, key string, request, destination any) error {
	s.paths = append(s.paths, path)
	s.keys = append(s.keys, key)
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	s.requests = append(s.requests, raw)
	if strings.HasSuffix(path, "/presentations") {
		title := s.title
		if title == "" {
			title = "Project result"
		}
		response, err := json.Marshal(
			map[string]string{
				"presentationId": "capture",
				"attachmentId":   "canonical-export",
				"title":          title,
			},
		)
		if err != nil {
			return err
		}
		return json.Unmarshal(response, destination)
	}
	return json.Unmarshal([]byte(`{"outputId":"artifact"}`), destination)
}

func TestPagePresentationUsesCanonicalExportAndNormalSubmission(t *testing.T) {
	t.Parallel()
	api := new(pageAPIStub)
	service, err := NewPageToolService(
		api,
		RunContext{CompanyID: "company", ConversationID: "conversation", RunID: "run", AgentID: "agent"},
	)
	if err != nil {
		t.Fatal(err)
	}
	input := PagePresentInput{
		PageID:      "page-a",
		Sequence:    7,
		Fingerprint: strings.Repeat("a", 64),
		Summary:     "Selected results and their evidence.",
	}
	for range 2 {
		got, err := presentNativePage(t.Context(), service.api, service.run, input)
		if err != nil || got.OutputID != "artifact" {
			t.Fatalf("present = %+v, %v", got, err)
		}
	}
	if api.keys[0] != api.keys[2] || api.keys[1] != api.keys[3] {
		t.Fatal("retry did not reuse the capture and submission receipts")
	}
	// The output is presented on the server's own Run route
	// (POST /api/v1/runs/{runId}/output); there is no Company-scoped one.
	if api.paths[0] != "/companies/company/runs/run/agent-pages/page-a/presentations" ||
		api.paths[1] != "/runs/run/output" {
		t.Fatalf("paths = %v", api.paths)
	}
	var submitted struct {
		Type           string
		PresentationID string
		Items          []struct {
			AttachmentID string
			Role         string
		}
	}
	if err := json.Unmarshal(api.requests[1], &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.Type != "text" || submitted.PresentationID != "capture" || len(submitted.Items) != 1 ||
		submitted.Items[0].AttachmentID != "canonical-export" ||
		submitted.Items[0].Role != "primary" {
		t.Fatalf("submission = %+v", submitted)
	}
}

func TestPageToolsKeepDestinationAndRetryIdentity(t *testing.T) {
	t.Parallel()
	api := new(pageAPIStub)
	service, err := NewPageToolService(api, RunContext{
		CompanyID: "company", ConversationID: "conversation", RunID: "run", AgentID: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	create := PageCreateInput{
		Title: "Autumn campaign",
		Content: map[string]any{"type": "doc", "content": []any{
			map[string]any{"type": "paragraph", "content": []any{
				map[string]any{"type": "text", "text": "Research and launch decisions."},
			}},
		}},
	}
	for range 2 {
		if _, err := service.CreatePage(t.Context(), create); err != nil {
			t.Fatal(err)
		}
	}
	if api.paths[0] != "/companies/company/runs/run/agent-pages" || api.keys[0] != api.keys[1] {
		t.Fatalf("create paths/receipts = %v / %v", api.paths, api.keys)
	}
	read, err := service.ReadPage(t.Context(), PageReadInput{PageID: "page/version"})
	if err != nil || read.Page["sequence"] != float64(7) {
		t.Fatalf("read = %+v, %v", read, err)
	}
	if got := api.paths[2]; got != "/companies/company/runs/run/agent-pages/page%2Fversion" {
		t.Fatalf("read path = %q", got)
	}
	edit := PageEditInput{
		PageID:   "page-a",
		Sequence: 7, Summary: "Add research findings",
		Operations: []PageOperation{{Type: "insert", Content: &PageBlock{Type: "paragraph"}}},
	}
	for range 2 {
		if _, err := service.EditPage(t.Context(), edit); err != nil {
			t.Fatal(err)
		}
	}
	edit.PageID = "page-b"
	if _, err := service.EditPage(t.Context(), edit); err != nil {
		t.Fatal(err)
	}
	if api.keys[2] != api.keys[3] || api.keys[2] == api.keys[4] {
		t.Fatalf("edit receipts = %v, want stable replay and distinct destinations", api.keys)
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(api.requests[2], &request); err != nil {
		t.Fatal(err)
	}
	if len(request) != 4 || string(request["sequence"]) != "7" || len(request["requestKey"]) == 0 ||
		len(request["operations"]) == 0 || len(request["summary"]) == 0 {
		t.Fatalf("edit request = %s, want flat edit input and request key", api.requests[2])
	}
}

func TestPagePresentationBoundsTitles(t *testing.T) {
	t.Parallel()
	for _, title := range []string{strings.Repeat("a", 120), strings.Repeat("a", 121), strings.Repeat("a", 160), strings.Repeat("a", 200), strings.Repeat("界", 66)} {
		t.Run(title, func(t *testing.T) {
			t.Parallel()
			api := &pageAPIStub{title: title}
			service, err := NewPageToolService(
				api,
				RunContext{
					CompanyID:      "company",
					ConversationID: "conversation",
					RunID:          "run",
					AgentID:        "agent",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := presentNativePage(
					t.Context(),
					service.api,
					service.run,
					PagePresentInput{
						PageID:      "page",
						Sequence:    7,
						Fingerprint: strings.Repeat("a", 64),
						Summary:     "Result",
					},
				); err != nil {
					t.Fatal(err)
				}
			}
			var got presentOutputRequest
			if err := json.Unmarshal(api.requests[1], &got); err != nil {
				t.Fatal(err)
			}
			runes := []rune(title)
			if want := string(runes[:min(len(runes), 160)]); got.Title != want {
				t.Fatalf("title = %q, want %q", got.Title, want)
			}
			if want := string(runes[:min(len(runes), 120)]); got.Items[0].Label != want {
				t.Fatalf("label = %q, want %q", got.Items[0].Label, want)
			}
			if api.keys[1] != api.keys[3] {
				t.Fatal("submission retry identity changed")
			}
		})
	}
}

func TestPageSourceTravelsThroughRunChannel(t *testing.T) {
	t.Parallel()
	api := new(pageAPIStub)
	service, err := NewPageToolService(
		api,
		RunContext{CompanyID: "company", ConversationID: "conversation", RunID: "run", AgentID: "agent"},
	)
	if err != nil {
		t.Fatal(err)
	}
	source := "<TaskCollection filter=\"active\" />"
	input := PageEditInput{
		PageID:     "page",
		Sequence:   7,
		Summary:    "Show active work",
		Operations: []PageOperation{{Type: "insert", Source: &source, LanguageVersion: 1}},
	}
	for range 2 {
		if _, err = service.EditPage(t.Context(), input); err != nil {
			t.Fatal(err)
		}
	}
	var request struct {
		Operations []PageOperation `json:"operations"`
	}
	if err = json.Unmarshal(api.requests[0], &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Operations) != 1 || request.Operations[0].Source == nil ||
		*request.Operations[0].Source != source ||
		request.Operations[0].LanguageVersion != 1 ||
		request.Operations[0].Content != nil {
		t.Fatalf("request = %s", api.requests[0])
	}
	if api.keys[0] != api.keys[1] {
		t.Fatal("source replay changed request key")
	}
	input.Operations[0].Content = &PageBlock{Type: "paragraph"}
	if _, err = service.EditPage(t.Context(), input); err == nil {
		t.Fatal("accepted both source and content")
	}
	if len(api.requests) != 2 {
		t.Fatal("invalid source authority reached transport")
	}
}
