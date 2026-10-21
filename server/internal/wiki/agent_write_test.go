package wiki

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeAgentWriteRejectsMalformedBatches(t *testing.T) {
	page := func(key string) AgentWritePageInput {
		return AgentWritePageInput{Key: key, Title: "Title", Summary: "Summary", Markdown: "Body"}
	}
	many := make([]AgentWritePageInput, MaxAgentWritePagesPerCall+1)
	for i := range many {
		many[i] = page("page-" + strings.Repeat("a", i+1))
	}
	tests := []struct {
		name  string
		input AgentWriteInput
		want  error
	}{
		{"no pages", AgentWriteInput{}, ErrPageInvalidInput},
		{"too many pages", AgentWriteInput{Pages: many}, ErrPageInvalidInput},
		{"empty key", AgentWriteInput{Pages: []AgentWritePageInput{page("")}}, ErrPageInvalidInput},
		{"uppercase key", AgentWriteInput{Pages: []AgentWritePageInput{page("Overview")}}, ErrPageInvalidInput},
		{
			"uuid key",
			AgentWriteInput{Pages: []AgentWritePageInput{page(uuid.NewString())}},
			ErrPageInvalidInput,
		},
		{
			"duplicate key",
			AgentWriteInput{Pages: []AgentWritePageInput{page("a"), page("a")}},
			ErrPageInvalidInput,
		},
		{
			"blank title",
			AgentWriteInput{Pages: []AgentWritePageInput{{Key: "a", Title: " ", Markdown: "x"}}},
			ErrPageInvalidInput,
		},
		{
			"huge body",
			AgentWriteInput{
				Pages: []AgentWritePageInput{
					{Key: "a", Title: "T", Markdown: strings.Repeat("x", 262145)},
				},
			},
			ErrPageBodyTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := normalizeAgentWrite(test.input); !errors.Is(err, test.want) {
				t.Fatalf("normalizeAgentWrite() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeAgentWriteTrimsAndRedacts(t *testing.T) {
	got, err := normalizeAgentWrite(AgentWriteInput{
		Pages: []AgentWritePageInput{
			{
				Key:      "company-overview",
				Title:    "  Overview ",
				Summary:  " sum ",
				Markdown: "token sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789 here",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pages[0].Title != "Overview" || got.Pages[0].Summary != "sum" {
		t.Fatalf("normalized = %+v", got)
	}
	if strings.Contains(got.Pages[0].Markdown, "abcdefghijklmnop") {
		t.Fatalf("credential survived normalization: %q", got.Pages[0].Markdown)
	}
}

func TestResolveWikiKeys(t *testing.T) {
	hub, products := uuid.New(), uuid.New()
	known := map[string]agentWriteTarget{
		"company-overview": {pageID: hub, title: "Company overview"},
		"products":         {pageID: products, title: "Products & services"},
	}
	tests := []struct {
		name     string
		markdown string
		want     string
		missing  []string
	}{
		{
			"label defaults to title",
			"See [[products]].",
			"See [[" + products.String() + "|Products & services]].",
			nil,
		},
		{
			"explicit label",
			"Back to [[company-overview|the hub]]",
			"Back to [[" + hub.String() + "|the hub]]",
			nil,
		},
		{
			"unresolved becomes text",
			"Later: [[open-questions]] and [[open-questions|Questions]]",
			"Later: open-questions and Questions",
			[]string{"open-questions"},
		},
		{"uuid link untouched", "[[" + hub.String() + "|x]]", "[[" + hub.String() + "|x]]", nil},
		{
			"artifact token untouched",
			"[[artifact:" + hub.String() + "#1]]",
			"[[artifact:" + hub.String() + "#1]]",
			nil,
		},
		{"label cannot break the token", "[[products|a|b]]", "[[" + products.String() + "|a b]]", nil},
		{"plain text untouched", "no links [x] [[ spaced ]]", "no links [x] [[ spaced ]]", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, missing := resolveWikiKeys(test.markdown, known)
			if got != test.want {
				t.Fatalf("resolveWikiKeys() = %q, want %q", got, test.want)
			}
			if strings.Join(missing, ",") != strings.Join(test.missing, ",") {
				t.Fatalf("missing = %v, want %v", missing, test.missing)
			}
		})
	}
}
