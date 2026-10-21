package approval

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
)

func TestRequestChatDeepLinkKeepsTheOriginConversation(t *testing.T) {
	t.Parallel()
	agentID := uuid.New()
	conversationID := uuid.New()
	taskID := uuid.New()
	for _, test := range []struct {
		name   string
		taskID uuid.NullUUID
	}{
		{name: "direct"},
		{name: "task", taskID: uuid.NullUUID{UUID: taskID, Valid: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := requestChatDeepLink(agentID, conversationID, test.taskID)
			got, err := url.ParseRequestURI(path)
			if err != nil {
				t.Fatalf("parse request chat path: %v", err)
			}
			query := got.Query()
			if got.Path != "/app" || query.Get("conversationKind") != test.name ||
				query.Get("agentId") != agentID.String() ||
				query.Get("conversationId") != conversationID.String() {
				t.Fatalf("request chat path = %q, want the %s origin", path, test.name)
			}
			if test.taskID.Valid && query.Get("taskId") != taskID.String() ||
				!test.taskID.Valid && query.Has("taskId") {
				t.Fatalf("request chat taskId = %q, want %v", query.Get("taskId"), test.taskID)
			}
		})
	}
}

func TestNormalizeApprovalDecisionInput(t *testing.T) {
	t.Parallel()

	got, err := normalizeApprovalDecisionInput(ApprovalDecisionInput{
		ExpectedRevision: 3,
		DecisionNote:     "  Approved for the reviewed Company Plan.  ",
	})
	if err != nil {
		t.Fatalf("normalizeApprovalDecisionInput() error = %v", err)
	}
	if got.ExpectedRevision != 3 || got.DecisionNote != "Approved for the reviewed Company Plan." {
		t.Fatalf("normalizeApprovalDecisionInput() = %+v", got)
	}
}

func TestNormalizeApprovalDecisionInputRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	// The revision bound is the `revision` binding rule on
	// approvalDecisionRequest, exercised by the handler package's
	// TestRevisionBindingRejectsRevisionsOutsideTheRange, not restated here.
	for name, input := range map[string]ApprovalDecisionInput{
		"oversized note":       {ExpectedRevision: 1, DecisionNote: strings.Repeat("x", 2049)},
		"oversized note bytes": {ExpectedRevision: 1, DecisionNote: strings.Repeat("😀", 2049)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := normalizeApprovalDecisionInput(input); !errors.Is(err, ErrApprovalInvalidInput) {
				t.Fatalf(
					"normalizeApprovalDecisionInput() error = %v, want ErrApprovalInvalidInput",
					err,
				)
			}
		})
	}
}

func TestApprovalAndQuestionStatusesAreDistinct(t *testing.T) {
	t.Parallel()

	for _, status := range []ApprovalStatus{
		ApprovalStatusPending,
		ApprovalStatusApproved,
		ApprovalStatusRejected,
		ApprovalStatusCancelled,
		ApprovalStatusExpired,
	} {
		if !validApprovalStatus(status) {
			t.Errorf("validApprovalStatus(%q) = false", status)
		}
	}
	if validApprovalStatus(ApprovalStatus(QuestionStatusAnswered)) {
		t.Fatal("approval status accepted question-only answered state")
	}
	for _, status := range []QuestionStatus{
		QuestionStatusPending,
		QuestionStatusAnswered,
		QuestionStatusWithdrawn,
		QuestionStatusExpired,
	} {
		if !validQuestionStatus(status) {
			t.Errorf("validQuestionStatus(%q) = false", status)
		}
	}
	if validQuestionStatus(QuestionStatus(ApprovalStatusApproved)) {
		t.Fatal("question status accepted approval-only approved state")
	}
}

func TestNormalizeApprovalListOptionsDefaultsToPending(t *testing.T) {
	t.Parallel()

	got, err := normalizeApprovalListOptions(ApprovalListOptions{})
	if err != nil {
		t.Fatalf("normalizeApprovalListOptions() error = %v", err)
	}
	if got.Limit != 50 || len(got.Statuses) != 1 || got.Statuses[0] != ApprovalStatusPending {
		t.Fatalf("normalizeApprovalListOptions() = %+v", got)
	}
}

func TestCompanyPlanApprovalAcceptsPayloadAboveUsualBodyLimit(t *testing.T) {
	t.Parallel()

	companyID, userID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	principal := auth.UserPrincipal{
		UserID: userID, CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
	origin := execution.RunOrigin{
		RunID: uuid.MustParse("00000000-0000-4000-8000-000000004711"), CompanyID: companyID,
		AgentID: uuid.Must(uuid.NewV7()), ConversationID: uuid.Must(uuid.NewV7()),
		OriginUserID: &userID,
	}
	payload := json.RawMessage(`{"value":"` + strings.Repeat("x", 2<<20) + `"}`)
	if _, _, err := normalizeApprovalRequest(
		principal,
		origin,
		uuid.Must(uuid.NewV7()),
		payload,
	); err != nil {
		t.Fatalf("normalizeApprovalRequest(2 MiB) error = %v", err)
	}
	if _, _, err := normalizePreparedCompanyPlanAction(PreparedCompanyPlanAction{
		CanonicalAction: payload,
		Presentation:    json.RawMessage(`{"kind":"company_plan"}`),
		PreviewMarkdown: "Authoritative Company Plan preview",
	}); err != nil {
		t.Fatalf("normalizePreparedCompanyPlanAction(2 MiB) error = %v", err)
	}
}
