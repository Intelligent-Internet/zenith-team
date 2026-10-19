package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/mcp/tools"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type wikiToolsStub struct{ searched, listed, read, written []string }

func (s *wikiToolsStub) SearchWiki(_ context.Context, input tools.WikiSearchInput) (tools.WikiSearchResult, error) {
	s.searched = append(s.searched, input.Query)
	return tools.WikiSearchResult{Items: []tools.WikiSearchItem{{PageID: "p1", Title: "Refunds", Revision: 4}}, Total: 1}, nil
}

func (s *wikiToolsStub) ListWiki(_ context.Context, input tools.WikiListInput) (tools.WikiListResult, error) {
	s.listed = append(s.listed, input.CollectionID)
	return tools.WikiListResult{
		Collections: []tools.WikiListCollection{{ID: "c1", Name: "Policies", PageCount: 1}},
		Items:       []tools.WikiListItem{{PageID: "p1", Title: "Refunds", Revision: 4}},
		Total:       1,
	}, nil
}

func (s *wikiToolsStub) ReadWiki(_ context.Context, input tools.WikiReadInput) (tools.WikiReadResult, error) {
	s.read = append(s.read, input.PageID)
	return tools.WikiReadResult{PageID: input.PageID, Title: "Refunds", Revision: 4, Content: "body", Notice: "n"}, nil
}

func (s *wikiToolsStub) WriteWiki(_ context.Context, input tools.WikiWriteInput) (tools.WikiWriteResult, error) {
	s.written = append(s.written, input.Pages[0].Key)
	return tools.WikiWriteResult{Pages: []tools.WikiWriteItem{{Key: input.Pages[0].Key, PageID: "p1", Status: "created"}}, Warnings: []string{}, TotalPages: 1}, nil
}

func TestWikiWriteIsAdvertisedOnlyWhenAllowed(t *testing.T) {
	t.Parallel()
	for name, allowedWiki := range map[string][]string{
		"read tools only": {"wiki_search", "wiki_list", "wiki_read"},
		"research tools":  {"wiki_search", "wiki_list", "wiki_read", "wiki_write"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			allowed := map[string]struct{}{}
			for _, tool := range allowedWiki {
				allowed[tool] = struct{}{}
			}
			stub := new(toolsStub)
			wiki := new(wikiToolsStub)
			server := newServer(stub, stub, nil, allowed, optionalTools{wiki: wiki})
			st, ct := mcpsdk.NewInMemoryTransports()
			go func() { _ = server.Run(ctx, st) }()
			session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "wiki-write-test", Version: "test"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			var write *mcpsdk.Tool
			for tool, err := range session.Tools(ctx, nil) {
				if err != nil {
					t.Fatal(err)
				}
				if tool.Name == "wiki_write" {
					write = tool
				}
			}
			if (write != nil) != slices.Contains(allowedWiki, "wiki_write") {
				t.Fatalf("wiki_write advertised = %v for %v", write != nil, allowedWiki)
			}
			if write == nil {
				return
			}
			if write.InputSchema == nil || write.OutputSchema == nil || write.Annotations == nil || write.Annotations.ReadOnlyHint ||
				!write.Annotations.IdempotentHint || write.Annotations.OpenWorldHint == nil || *write.Annotations.OpenWorldHint {
				t.Fatalf("wiki_write schema/annotations = %+v", write.Annotations)
			}
			result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "wiki_write", Arguments: map[string]any{
				"pages": []any{map[string]any{"key": "company-overview", "title": "Company overview", "markdown": "x"}},
			}})
			if err != nil || result.IsError || !slices.Equal(wiki.written, []string{"company-overview"}) {
				t.Fatalf("wiki_write = %#v, %v; written %q", result, err, wiki.written)
			}
		})
	}
}

func TestWikiToolsRequireExplicitAllowlist(t *testing.T) {
	t.Parallel()
	for name, allowedWiki := range map[string][]string{
		"not allowed": nil,
		"search only": {"wiki_search"},
		"list only":   {"wiki_list"},
		"all three":   {"wiki_search", "wiki_list", "wiki_read"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			allowed := map[string]struct{}{"company_plan_export": {}}
			for _, tool := range allowedWiki {
				allowed[tool] = struct{}{}
			}
			stub := new(toolsStub)
			wiki := new(wikiToolsStub)
			server := newServer(stub, stub, nil, allowed, optionalTools{wiki: wiki})
			st, ct := mcpsdk.NewInMemoryTransports()
			go func() { _ = server.Run(ctx, st) }()
			session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "wiki-test", Version: "test"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			found := map[string]*mcpsdk.Tool{}
			for tool, err := range session.Tools(ctx, nil) {
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(tool.Name, "wiki_") {
					found[tool.Name] = tool
				}
			}
			if len(found) != len(allowedWiki) {
				t.Fatalf("wiki tools advertised = %d, want %d", len(found), len(allowedWiki))
			}
			for name, tool := range found {
				if tool.InputSchema == nil || tool.OutputSchema == nil {
					t.Fatalf("%s missing typed schema", name)
				}
				if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint ||
					tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
					t.Fatalf("%s annotations = %+v", name, tool.Annotations)
				}
			}
			if found["wiki_search"] != nil {
				result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "wiki_search", Arguments: map[string]any{"query": "refunds"}})
				if err != nil || result.IsError || !slices.Equal(wiki.searched, []string{"refunds"}) {
					t.Fatalf("wiki_search = %#v, %v; searched %q", result, err, wiki.searched)
				}
			}
			if found["wiki_list"] != nil {
				result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "wiki_list", Arguments: map[string]any{"collectionId": "c1"}})
				if err != nil || result.IsError || !slices.Equal(wiki.listed, []string{"c1"}) {
					t.Fatalf("wiki_list = %#v, %v; listed %q", result, err, wiki.listed)
				}
			}
			if found["wiki_read"] != nil {
				result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "wiki_read", Arguments: map[string]any{"pageId": "p1"}})
				if err != nil || result.IsError || !slices.Equal(wiki.read, []string{"p1"}) {
					t.Fatalf("wiki_read = %#v, %v; read %q", result, err, wiki.read)
				}
			}
		})
	}
}
