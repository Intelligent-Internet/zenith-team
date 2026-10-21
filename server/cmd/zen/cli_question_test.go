package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/runchannel"
)

const testQuestionID = "0198aa00-0000-7000-8000-00000000d001"

// sampleQuestion is one pending Question the way the server serves it: two
// prompts, the first a choice and the second free text.
const sampleQuestion = `{"id":"` + testQuestionID + `","companyId":"` + testCompanyID + `","status":"pending",` +
	`"title":"Which plan?","askedByAgent":{"id":"` + testRunAgentID + `","kind":"agent","name":"Scout","avatarUrl":null},` +
	`"originRunId":"` + testRunID + `","taskId":null,"conversationId":"` + testRunConvID + `",` +
	`"prompts":[{"id":"p1","prompt":"Pick the launch plan","selectionMode":"single","required":true,` +
	`"options":[{"id":"p1-o1","label":"Ship now"},{"id":"p1-o2","label":"Wait"}]},` +
	`{"id":"p2","prompt":"Anything else?","selectionMode":"single","required":false,"options":[]}],` +
	`"expiresAt":"2026-09-18T00:00:00Z","answers":null,"answeredBy":null,"continuationRunId":null,` +
	`"createdAt":"2026-09-17T00:00:00Z","updatedAt":"2026-09-17T00:00:00Z","revision":2}`

// The operator reads one Question through GET /questions/{id}, and `answer`
// and `withdraw` pre-read the same route for the prompts and the revision
// they then name in their POST.
func TestQuestionGetAnswerAndWithdrawReadTheQuestionByID(t *testing.T) {
	var requests []string
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		base := "/api/v1/companies/" + testCompanyID + "/questions/" + testQuestionID
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base:
			_, _ = w.Write([]byte(sampleQuestion))
		case r.Method == http.MethodPost && (r.URL.Path == base+"/answer" || r.URL.Path == base+"/withdraw"):
			posted = nil
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Errorf("decode body: %v", err)
			}
			resolved := strings.Replace(sampleQuestion, `"status":"pending"`, `"status":"resolved"`, 1)
			_, _ = w.Write([]byte(resolved))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	t.Setenv("ZENITH_TEAM_RUN_ID", "")
	t.Setenv("ZENITH_TEAM_RUN_TOKEN", "")

	out, err := executeCLI(t, newRootCmd, "question", "get", testQuestionID)
	if err != nil || !strings.Contains(out, `"title": "Which plan?"`) {
		t.Fatalf("question get = %q (err %v)", out, err)
	}
	if requests[0] != "GET /api/v1/companies/"+testCompanyID+"/questions/"+testQuestionID {
		t.Fatalf("question get requested %q", requests[0])
	}

	// --select and --text pair with the prompts in order; the pre-read
	// supplies the prompt ids and the revision the POST names.
	requests = nil
	out, err = executeCLI(t, newRootCmd, "question", "answer", testQuestionID,
		"--select", "p1-o2", "--text", "", "--text", "Ship after pricing lands")
	if err != nil || !strings.Contains(out, `"status": "resolved"`) {
		t.Fatalf("question answer = %q (err %v)", out, err)
	}
	if len(requests) != 2 || !strings.HasPrefix(requests[0], "GET ") ||
		requests[1] != "POST /api/v1/companies/"+testCompanyID+"/questions/"+testQuestionID+"/answer" {
		t.Fatalf("question answer requested %v, want the read then the POST", requests)
	}
	if posted["expectedRevision"] != float64(2) {
		t.Fatalf("answer expectedRevision = %v, want the revision read", posted["expectedRevision"])
	}
	answers, _ := posted["answers"].([]any)
	if len(answers) != 2 {
		t.Fatalf("answers = %v, want one per prompt", posted["answers"])
	}
	first, _ := answers[0].(map[string]any)
	second, _ := answers[1].(map[string]any)
	if first["promptId"] != "p1" || second["promptId"] != "p2" {
		t.Fatalf("answers = %v, want the prompt ids from the read", answers)
	}
	if selected, _ := first["optionIds"].([]any); len(selected) != 1 || selected[0] != "p1-o2" {
		t.Fatalf("first answer = %v, want the selected option", first)
	}
	if second["text"] != "Ship after pricing lands" {
		t.Fatalf("second answer = %v, want the free text", second)
	}

	// Withdraw reads the revision the same way, and --revision skips the read.
	requests = nil
	if _, err := executeCLI(t, newRootCmd, "question", "withdraw", testQuestionID); err != nil {
		t.Fatalf("question withdraw: %v", err)
	}
	if len(requests) != 2 || posted["expectedRevision"] != float64(2) {
		t.Fatalf("question withdraw requested %v with %v", requests, posted)
	}
	requests = nil
	if _, err := executeCLI(t, newRootCmd, "question", "withdraw", testQuestionID, "--revision", "5"); err != nil {
		t.Fatalf("question withdraw --revision: %v", err)
	}
	if len(requests) != 1 || posted["expectedRevision"] != float64(5) {
		t.Fatalf("question withdraw --revision requested %v with %v", requests, posted)
	}
}

func TestQuestionAskBuildsPromptsAndTellsTheAgentToEndTheRun(t *testing.T) {
	channel := newFakeRunChannel(t, func(request runchannel.Request) (int, string) {
		if request.Method != http.MethodPost || request.Path != "/companies/"+testCompanyID+"/questions" {
			return 403, `{"detail":"denied","code":"run_scope_denied"}`
		}
		return 201, `{"question":{"id":"q1","status":"pending","title":"Which plan?"}}`
	})
	seedAgentShell(t, channel, testRunTaskID, []string{"zen question ask"})

	out, err := executeCLI(t, newRootCmd, "question", "ask",
		"--title", "Which plan?",
		"--prompt", "Pick the launch plan",
		"--option", "Ship now", "--option", "Wait for pricing",
		"--help-text", "Both are reversible")
	if err != nil {
		t.Fatalf("question ask: %v", err)
	}
	if !strings.Contains(out, `"id": "q1"`) || !strings.Contains(out, "end the Run now") {
		t.Fatalf("ask printed %q", out)
	}
	var body struct {
		Title   string `json:"title"`
		Prompts []struct {
			ID                string `json:"id"`
			Prompt            string `json:"prompt"`
			SelectionMode     string `json:"selectionMode"`
			Required          bool   `json:"required"`
			AllowCustomAnswer bool   `json:"allowCustomAnswer"`
			HelpText          string `json:"helpText"`
			Options           []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"options"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(channel.requests[0].Body, &body); err != nil {
		t.Fatalf("decode ask body: %v", err)
	}
	if body.Title != "Which plan?" || len(body.Prompts) != 1 {
		t.Fatalf("ask body = %+v", body)
	}
	prompt := body.Prompts[0]
	if prompt.ID != "p1" || prompt.SelectionMode != "single" || !prompt.Required || !prompt.AllowCustomAnswer ||
		prompt.HelpText != "Both are reversible" || len(prompt.Options) != 2 || prompt.Options[1].ID != "p1-o2" ||
		prompt.Options[1].Label != "Wait for pricing" {
		t.Fatalf("prompt = %+v", prompt)
	}
	if channel.requests[0].RequestKey == "" {
		t.Fatal("ask carried no request key")
	}
	// Asking is a Run's act.
	seedSession(t, "http://127.0.0.1:1")
	t.Setenv("ZENITH_TEAM_RUN_ID", "")
	t.Setenv("ZENITH_TEAM_RUN_TOKEN", "")
	if _, err := executeCLI(t, newRootCmd, "question", "ask", "--title", "x", "--prompt", "y"); err == nil ||
		!strings.Contains(err.Error(), "only works in Agent mode") {
		t.Fatalf("question ask as operator = %v", err)
	}
}

// --multi has to speak the API's own vocabulary. It sent "multiple" once,
// which the server rejects as validation_failed, so every multi-select
// question an Agent asked simply failed.
func TestQuestionAskMultiUsesTheAPISelectionVocabulary(t *testing.T) {
	channel := newFakeRunChannel(t, func(request runchannel.Request) (int, string) {
		if request.Method != http.MethodPost || request.Path != "/companies/"+testCompanyID+"/questions" {
			return 403, `{"detail":"denied","code":"run_scope_denied"}`
		}
		return 201, `{"question":{"id":"q1","status":"pending","title":"Completed milestones"}}`
	})
	seedAgentShell(t, channel, testRunTaskID, []string{"zen question ask"})

	if _, err := executeCLI(t, newRootCmd, "question", "ask",
		"--title", "Completed milestones",
		"--prompt", "Which outcomes were already finished?",
		"--option", "Positioning", "--option", "Brand",
		"--multi",
	); err != nil {
		t.Fatalf("question ask --multi: %v", err)
	}

	var body struct {
		Prompts []struct {
			SelectionMode string `json:"selectionMode"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(channel.requests[0].Body, &body); err != nil {
		t.Fatalf("decode ask body: %v", err)
	}
	if len(body.Prompts) != 1 || body.Prompts[0].SelectionMode != "multi" {
		t.Fatalf("selectionMode = %+v, want multi", body.Prompts)
	}
}
