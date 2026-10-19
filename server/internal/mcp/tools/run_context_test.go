package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/mcp/apiclient"
	"boardly.dev/boardly/server/internal/runchannel"
)

func TestRunRequestKeyIncludesExactContextAndPayload(t *testing.T) {
	t.Parallel()
	baseContext := RunContext{
		CompanyID:      "company",
		ConversationID: "conversation",
		RunID:          "00000000-0000-4000-8000-000000000071",
		AgentID:        "agent",
	}
	basePayload := struct {
		Reason string `json:"reason"`
	}{Reason: "blocked"}
	base, err := runRequestKey("mark_task_failed", baseContext, basePayload)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := runRequestKey("mark_task_failed", baseContext, basePayload)
	if err != nil {
		t.Fatal(err)
	}
	changedContext := baseContext
	changedContext.RunID = "00000000-0000-4000-8000-000000000072"
	contextKey, err := runRequestKey("mark_task_failed", changedContext, basePayload)
	if err != nil {
		t.Fatal(err)
	}
	changedPayload := basePayload
	changedPayload.Reason = "incompatible contract"
	payloadKey, err := runRequestKey("mark_task_failed", baseContext, changedPayload)
	if err != nil {
		t.Fatal(err)
	}
	operationKey, err := runRequestKey("create_mail_drafts", baseContext, basePayload)
	if err != nil {
		t.Fatal(err)
	}
	if replay != base {
		t.Fatalf("replay key = %q, want %q", replay, base)
	}
	if contextKey == base || payloadKey == base || operationKey == base {
		t.Fatalf(
			"derived keys did not distinguish operation/context/payload: base=%q context=%q payload=%q operation=%q",
			base,
			contextKey,
			payloadKey,
			operationKey,
		)
	}
}

// boardly-mcp's tools reach the server only through the daemon's Run
// channel (Access TECH B3b). Every call leaves boardly-mcp as a channel
// request with the Run capability, never with a bearer or the Run token, and
// every mutation names a UUID request key the daemon forwards. The fake
// channel stands in for the daemon; the daemon's own test drives the same
// tools through the real channel and its policy.
func TestRunScopedToolsCallTheServerOnlyThroughTheRunChannel(t *testing.T) {
	t.Parallel()
	const capability = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	run := RunContext{
		CompanyID:      "0198aa00-0000-7000-8000-0000000000c0",
		ConversationID: "0198aa00-0000-7000-8000-0000000000d0",
		TaskID:         "0198aa00-0000-7000-8000-0000000000e0",
		RunID:          "0198aa00-0000-7000-8000-0000000000b0",
		AgentID:        "0198aa00-0000-7000-8000-0000000000a0",
	}
	var mu sync.Mutex
	var requests []runchannel.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != runchannel.AuthorizationScheme+" "+capability {
			t.Errorf("Authorization = %q, want the Run capability and never a bearer", got)
		}
		if r.Header.Get("X-Boardly-Run-Token") != "" {
			t.Error("a tool sent the Run token; only the daemon's forward carries it")
		}
		var request runchannel.Request
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			t.Errorf("decode channel request: %v", err)
		}
		if request.Method != http.MethodGet {
			if _, err := uuid.Parse(request.RequestKey); err != nil {
				t.Errorf("%s %s has no UUID request key", request.Method, request.Path)
			}
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		body := `{}`
		switch {
		case request.Path == "/companies":
			body = `{"items":[{"id":"` + run.CompanyID + `","slug":"acme"}]}`
		case strings.HasSuffix(request.Path, "/agents"):
			body = `{"items":[]}`
		case strings.HasSuffix(request.Path, "/presentations"):
			body = `{"presentationId":"` + uuid.NewString() + `","attachmentId":"` + uuid.NewString() + `","title":"Plan"}`
		}
		_ = json.NewEncoder(w).Encode(runchannel.Response{
			SchemaVersion: 1, Status: http.StatusOK, Body: json.RawMessage(body),
		})
	}))
	t.Cleanup(server.Close)
	api := apiclient.NewRunChannel(runchannel.RunControlConfig{
		SchemaVersion: 1, Endpoint: server.URL, Capability: capability,
	})

	calendar, calendarErr := NewCalendarToolService(api, run)
	social, socialErr := NewSocialToolService(api, run)
	wiki, wikiErr := NewWikiToolService(api, run)
	pages, pagesErr := NewPageToolService(api, run)
	mail, mailErr := NewMailService(api, run)
	failures, failuresErr := NewTaskFailureService(api, run)
	milestones, milestonesErr := NewMilestoneToolService(api, run)
	// present_output is completed by the daemon itself, but it rides the same
	// channel named by run.json: its endpoint and capability, never a bearer.
	outputs, outputsErr := NewOutputChannelService(server.URL, capability, run.RunID)
	if err := errors.Join(
		calendarErr, socialErr, wikiErr, pagesErr, mailErr, failuresErr, milestonesErr,
		outputsErr,
	); err != nil {
		t.Fatalf("build the tool services: %v", err)
	}
	plans := NewCompanyPlanService(api)
	pageID := uuid.NewString()
	calls := map[string]func(context.Context) error{
		"ask_human": func(ctx context.Context) error {
			_, err := NewQuestionService(api, run.CompanyID).AskHuman(ctx, AskHumanInput{Title: "Name?"})
			return err
		},
		"attest_milestones": func(ctx context.Context) error {
			_, err := milestones.AttestMilestones(ctx, AttestMilestonesInput{
				Milestones: []string{"launch"},
			})
			return err
		},
		"calendar_read": func(ctx context.Context) error {
			_, err := calendar.ReadCalendar(ctx, ReadCalendarInput{})
			return err
		},
		"calendar_write": func(ctx context.Context) error {
			_, err := calendar.WriteCalendar(ctx, WriteCalendarInput{Action: "create"})
			return err
		},
		"company_plan_export": func(ctx context.Context) error {
			_, err := plans.Export(ctx, ExportInput{OutputPath: filepath.Join(t.TempDir(), "plan.yaml")})
			return err
		},
		"create_mail_drafts": func(ctx context.Context) error {
			_, err := mail.CreateMailDrafts(ctx, CreateMailDraftsInput{})
			return err
		},
		"list_agents": func(ctx context.Context) error {
			_, err := plans.ListAgents(ctx, ListAgentsInput{})
			return err
		},
		"mark_task_failed": func(ctx context.Context) error {
			_, err := failures.MarkTaskFailed(ctx, MarkTaskFailedInput{Reason: "The market is gone."})
			return err
		},
		"present_output_page": func(ctx context.Context) error {
			_, err := outputs.PresentOutput(
				ctx,
				PresentOutputInput{Summary: "The plan.", Page: &PresentOutputPage{
					PageID: pageID, Sequence: 1, Fingerprint: strings.Repeat("a", 64),
				}},
			)
			return err
		},
		"page_read": func(ctx context.Context) error {
			_, err := pages.ReadPage(ctx, PageReadInput{PageID: pageID})
			return err
		},
		"present_output": func(ctx context.Context) error {
			_, err := outputs.PresentOutput(ctx, PresentOutputInput{Title: "Plan", Summary: "The plan."})
			return err
		},
		"project_read": func(ctx context.Context) error {
			_, err := pages.ReadProject(ctx)
			return err
		},
		"social_publish": func(ctx context.Context) error {
			_, err := social.PublishSocial(ctx, PublishSocialInput{QuestionID: uuid.NewString()})
			return err
		},
		"social_read": func(ctx context.Context) error {
			_, err := social.ReadSocial(ctx, ReadSocialInput{})
			return err
		},
		"wiki_read": func(ctx context.Context) error {
			_, err := wiki.ReadWiki(ctx, WikiReadInput{PageID: pageID})
			return err
		},
		"wiki_search": func(ctx context.Context) error {
			_, err := wiki.SearchWiki(ctx, WikiSearchInput{Query: "refunds"})
			return err
		},
	}
	for tool, call := range calls {
		mu.Lock()
		before := len(requests)
		mu.Unlock()
		// The fake answers every call with a bare body, so a tool that checks
		// its answer in detail may fail after its call; it must still have
		// made the call, and only through the channel.
		_ = call(t.Context())
		mu.Lock()
		reached := len(requests) > before
		mu.Unlock()
		if !reached {
			t.Errorf("%s made no call through the Run channel", tool)
		}
	}
}
