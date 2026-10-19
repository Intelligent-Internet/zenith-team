package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type wikiAPIStub struct {
	paths  []string
	keys   []string
	posted []string
	body   string
	err    error
}

func (s *wikiAPIStub) PostRunJSON(_ context.Context, path, key string, source, destination any) error {
	encoded, err := json.Marshal(source)
	if err != nil {
		return err
	}
	s.paths, s.keys, s.posted = append(s.paths, path), append(s.keys, key), append(s.posted, string(encoded))
	if s.err != nil {
		return s.err
	}
	return json.Unmarshal([]byte(s.body), destination)
}

func (s *wikiAPIStub) FetchRunJSON(_ context.Context, path string, destination any) error {
	s.paths = append(s.paths, path)
	if s.err != nil {
		return s.err
	}
	return json.Unmarshal([]byte(s.body), destination)
}

func newTestWikiService(t *testing.T, api *wikiAPIStub) *DefaultWikiToolService {
	t.Helper()
	service, err := NewWikiToolService(api, RunContext{CompanyID: "company", ConversationID: "conversation", RunID: "run", AgentID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestWikiToolsUseTheRunProvenRoutesBoundToTheRun(t *testing.T) {
	t.Parallel()
	api := &wikiAPIStub{body: `{"items":[{"pageId":"p1","title":"Refunds","summary":"","snippet":"…","collectionName":null,"revision":4,"updatedAt":"2026-09-29T00:00:00Z"}],"total":1}`}
	service := newTestWikiService(t, api)
	result, err := service.SearchWiki(context.Background(), WikiSearchInput{Query: "  refund policy & fees ", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Revision != 4 || result.Items[0].CollectionName != nil {
		t.Fatalf("result = %+v", result)
	}
	if want := "/companies/company/runs/run/agent-wiki/search?limit=3&q=refund+policy+%26+fees"; api.paths[0] != want {
		t.Fatalf("search path = %q, want %q", api.paths[0], want)
	}

	api.body = `{"pageId":"p1","title":"Refunds","summary":"","collectionName":"Policies","revision":4,"revisionId":"r4","updatedAt":"2026-09-29T00:00:00Z","updatedByName":null,"content":"text","offset":24000,"returnedChars":4,"totalChars":24004,"truncated":false,"nextOffset":null,"notice":"n"}`
	read, err := service.ReadWiki(context.Background(), WikiReadInput{PageID: "p1", Offset: 24000})
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != "text" || read.Revision != 4 || read.NextOffset != nil || read.Notice != "n" {
		t.Fatalf("read = %+v", read)
	}
	if want := "/companies/company/runs/run/agent-wiki/pages/p1?offset=24000"; api.paths[1] != want {
		t.Fatalf("read path = %q, want %q", api.paths[1], want)
	}
	if _, err = service.ReadWiki(context.Background(), WikiReadInput{PageID: "p1"}); err != nil {
		t.Fatal(err)
	}
	if want := "/companies/company/runs/run/agent-wiki/pages/p1"; api.paths[2] != want {
		t.Fatalf("first-chunk path = %q, want %q", api.paths[2], want)
	}
	// The server rejects limit<1, so an omitted limit must not be sent at all.
	api.body = `{"items":[],"total":0}`
	if _, err = service.SearchWiki(context.Background(), WikiSearchInput{Query: "refunds"}); err != nil {
		t.Fatal(err)
	}
	if want := "/companies/company/runs/run/agent-wiki/search?q=refunds"; api.paths[3] != want {
		t.Fatalf("default search path = %q, want %q", api.paths[3], want)
	}
}

func TestWikiListUsesTheRunProvenRouteAndSendsOnlyWhatWasAsked(t *testing.T) {
	t.Parallel()
	api := &wikiAPIStub{body: `{"collections":[{"id":"c1","name":"Policies","pageCount":2}],"items":[{"pageId":"p1","title":"Refunds","summary":"","collectionName":"Policies","revision":4,"updatedAt":"2026-09-29T00:00:00Z"}],"total":30,"nextOffset":25}`}
	service := newTestWikiService(t, api)
	result, err := service.ListWiki(context.Background(), WikiListInput{CollectionID: " c1 ", Offset: 25, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Collections) != 1 || result.Collections[0].PageCount != 2 || len(result.Items) != 1 ||
		result.Items[0].Revision != 4 || result.Total != 30 || result.NextOffset == nil || *result.NextOffset != 25 {
		t.Fatalf("result = %+v", result)
	}
	if want := "/companies/company/runs/run/agent-wiki/list?collectionId=c1&limit=10&offset=25"; api.paths[0] != want {
		t.Fatalf("list path = %q, want %q", api.paths[0], want)
	}
	// The server rejects limit<1, so an omitted limit and offset are not sent.
	api.body = `{"collections":null,"items":null,"total":0,"nextOffset":null}`
	result, err = service.ListWiki(context.Background(), WikiListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "/companies/company/runs/run/agent-wiki/list"; api.paths[1] != want {
		t.Fatalf("default list path = %q, want %q", api.paths[1], want)
	}
	if result.Collections == nil || result.Items == nil || result.NextOffset != nil {
		t.Fatalf("empty result = %+v, want empty slices and no next offset", result)
	}
}

func TestWikiToolsRejectBadInputBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	api := new(wikiAPIStub)
	service := newTestWikiService(t, api)
	for name, call := range map[string]func() error{
		"blank query": func() error {
			_, err := service.SearchWiki(context.Background(), WikiSearchInput{Query: "  "})
			return err
		},
		"limit too big": func() error {
			_, err := service.SearchWiki(context.Background(), WikiSearchInput{Query: "x", Limit: 11})
			return err
		},
		"negative limit": func() error {
			_, err := service.SearchWiki(context.Background(), WikiSearchInput{Query: "x", Limit: -1})
			return err
		},
		"missing page id": func() error { _, err := service.ReadWiki(context.Background(), WikiReadInput{}); return err },
		"list limit too big": func() error {
			_, err := service.ListWiki(context.Background(), WikiListInput{Limit: 51})
			return err
		},
		"list negative limit": func() error {
			_, err := service.ListWiki(context.Background(), WikiListInput{Limit: -1})
			return err
		},
		"list negative offset": func() error {
			_, err := service.ListWiki(context.Background(), WikiListInput{Offset: -1})
			return err
		},
		"negative offset": func() error {
			_, err := service.ReadWiki(context.Background(), WikiReadInput{PageID: "p", Offset: -1})
			return err
		},
	} {
		if call() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(api.paths) != 0 {
		t.Fatalf("requests sent for invalid input: %v", api.paths)
	}
}

func TestWikiToolsWrapServerRefusals(t *testing.T) {
	t.Parallel()
	service := newTestWikiService(t, &wikiAPIStub{err: errors.New("wiki_agent_access_disabled")})
	_, searchErr := service.SearchWiki(context.Background(), WikiSearchInput{Query: "x"})
	_, listErr := service.ListWiki(context.Background(), WikiListInput{})
	_, readErr := service.ReadWiki(context.Background(), WikiReadInput{PageID: "p"})
	for name, err := range map[string]error{"search": searchErr, "list": listErr, "read": readErr} {
		if err == nil || !strings.Contains(err.Error(), "wiki_agent_access_disabled") {
			t.Errorf("%s error = %v, want the server refusal code", name, err)
		}
	}
}

func TestWikiWritePostsTheBatchToTheRunRouteWithAConvergingKey(t *testing.T) {
	t.Parallel()
	api := &wikiAPIStub{body: `{"pages":[{"key":"company-overview","pageId":"p1","title":"Company overview","revision":1,"status":"created"}],"warnings":["w"],"totalPages":1}`}
	service := newTestWikiService(t, api)
	input := WikiWriteInput{Pages: []WikiWritePage{
		{Key: "company-overview", Title: "Company overview", Summary: "s", Markdown: "See [[products]]"},
	}}
	result, err := service.WriteWiki(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 1 || result.Pages[0].Status != "created" || result.Pages[0].PageID != "p1" ||
		result.TotalPages != 1 || len(result.Warnings) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if want := "/companies/company/runs/run/agent-wiki/pages"; api.paths[0] != want {
		t.Fatalf("path = %q, want %q", api.paths[0], want)
	}
	want := `{"pages":[{"key":"company-overview","title":"Company overview","summary":"s","markdown":"See [[products]]"}]}`
	if api.posted[0] != want {
		t.Fatalf("body = %s, want %s", api.posted[0], want)
	}

	// A retry of the same batch carries the same key; a different batch does not.
	if _, err = service.WriteWiki(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	input.Pages[0].Markdown = "changed"
	if _, err = service.WriteWiki(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if api.keys[0] != api.keys[1] || api.keys[0] == api.keys[2] || api.keys[0] == "" {
		t.Fatalf("idempotency keys = %v", api.keys)
	}
}

func TestWikiWriteForwardsTrimmedKeys(t *testing.T) {
	t.Parallel()
	api := &wikiAPIStub{body: `{"pages":[],"warnings":[],"totalPages":0}`}
	service := newTestWikiService(t, api)
	input := WikiWriteInput{Pages: []WikiWritePage{{Key: "  company-overview\n", Title: "T", Markdown: "m"}}}
	if _, err := service.WriteWiki(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(api.posted[0], `"key":"company-overview"`) {
		t.Fatalf("body = %s, want the trimmed key", api.posted[0])
	}
	if input.Pages[0].Key != "  company-overview\n" {
		t.Fatal("the caller's input was modified")
	}
	// The trimmed and the untrimmed spelling of one key are one key.
	dup := WikiWriteInput{Pages: []WikiWritePage{{Key: "k", Title: "T"}, {Key: " k ", Title: "U"}}}
	if _, err := service.WriteWiki(context.Background(), dup); err == nil {
		t.Fatal("duplicate after trimming accepted")
	}
}
