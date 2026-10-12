package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type wikiAPIStub struct {
	paths []string
	body  string
	err   error
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
	_, readErr := service.ReadWiki(context.Background(), WikiReadInput{PageID: "p"})
	for name, err := range map[string]error{"search": searchErr, "read": readErr} {
		if err == nil || !strings.Contains(err.Error(), "wiki_agent_access_disabled") {
			t.Errorf("%s error = %v, want the server refusal code", name, err)
		}
	}
}
