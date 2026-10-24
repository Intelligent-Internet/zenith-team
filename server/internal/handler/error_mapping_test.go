package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/realtime"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	"github.com/Intelligent-Internet/zenith-team/server/internal/workflow"
)

// mappedErrorCase pins one sentinel-to-response mapping. The golden suite locks
// every write*Error mapper to its exact (status, detail, code) triple so the
// declarative-table refactor cannot change any response byte.
type mappedErrorCase struct {
	name   string
	err    error
	status int
	detail string
	code   string
}

func assertMappedErrorResponse(t *testing.T, write func(*gin.Context), status int, detail, code string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	util.SetRequestID(c, "golden-request-id")
	write(c)
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d", recorder.Code, status)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (body %q)", err, recorder.Body.String())
	}
	if body["detail"] != detail {
		t.Fatalf("detail = %q, want %q", body["detail"], detail)
	}
	if body["code"] != code {
		t.Fatalf("code = %q, want %q", body["code"], code)
	}
	if body["requestId"] != "golden-request-id" {
		t.Fatalf("requestId = %q", body["requestId"])
	}
}

// The head-mismatch 409 is the only error response that carries more than the
// envelope, so the golden triple above is not the whole contract for it.
// internal/daemon reads runId/throughSeq flat off this body
// to reconcile; a boundary that serves the table's fixed body leaves it
// nothing to reconcile against, so it re-sends the same stale proof and the
// run stays non-terminal forever (bug R1).
func TestTerminalHeadMismatchServesTheServersOwnHead(t *testing.T) {
	t.Parallel()

	mismatch := &execution.TranscriptHeadMismatchError{
		RunID:            uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		ProofThroughSeq:  7,
		ServerThroughSeq: 9,
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	util.SetRequestID(c, "golden-request-id")
	writeTerminalRunError(
		c, fmt.Errorf("complete run: %w", mismatch), boundaryErrorRunCompletionFailed,
	)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (body %q)", err, recorder.Body.String())
	}
	if body["code"] != "run_transcript_head_mismatch" ||
		body["detail"] != "terminal transcript head does not match the committed run transcript" ||
		body["requestId"] != "golden-request-id" {
		t.Fatalf("envelope = %v", body)
	}
	// The server's OWN head, not an echo of the proof that was refused.
	if body["runId"] != "00000000-0000-4000-8000-000000004711" || body["throughSeq"] != float64(9) {
		t.Fatalf("head = %v", body)
	}
}

// The sibling terminal 409 has no head to reconcile against, so it must not
// grow one: a daemon that adopted a head from an invalid-final-item refusal
// would be inventing a transcript prefix it was never told.
func TestTerminalFinalItemConflictCarriesNoHead(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	util.SetRequestID(c, "golden-request-id")
	writeTerminalRunError(c, execution.ErrTerminalFinalItemInvalid, boundaryErrorRunFailureFailed)

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (body %q)", err, recorder.Body.String())
	}
	if len(body) != 3 || body["code"] != "run_terminal_item_invalid" {
		t.Fatalf("body = %v, want the bare envelope", body)
	}
}

// The handoff refusal carries runLive flat beside the envelope: the client
// words its confirmation dialog from it ("cancel the run" or only "hand it
// over"), and a Task in progress may have nothing live — paused on an
// approval, or handed off once already — so the client cannot guess it.
func TestTaskHandoffRefusalCarriesRunLive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		err     error
		runLive bool
		detail  string
	}{
		{
			"run live",
			&task.HandoffConfirmationError{RunLive: true},
			true,
			"this Task has a run in flight; confirm the handoff to cancel it and move the Task",
		},
		{
			"nothing live",
			&task.HandoffConfirmationError{},
			false,
			"this Task has run before; confirm the handoff to move it and start the work over",
		},
		{
			"bare sentinel",
			task.ErrHandoffConfirmationRequired,
			false,
			"this Task has run before; confirm the handoff to move it and start the work over",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			util.SetRequestID(c, "golden-request-id")
			writeTaskError(c, fmt.Errorf("update Task: %w", tc.err))

			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", recorder.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error body: %v (body %q)", err, recorder.Body.String())
			}
			if body["code"] != "task_handoff_confirmation_required" ||
				body["detail"] != tc.detail ||
				body["requestId"] != "golden-request-id" {
				t.Fatalf("envelope = %v", body)
			}
			if runLive, ok := body["runLive"].(bool); !ok || runLive != tc.runLive {
				t.Fatalf(
					"runLive = %v (present %v), want %v; body %v",
					body["runLive"],
					ok,
					tc.runLive,
					body,
				)
			}
			if len(body) != 4 {
				t.Fatalf("body = %v, want the envelope plus runLive", body)
			}
		})
	}

	// Every other Task refusal stays the bare envelope.
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	writeTaskError(c, task.ErrConflict)
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (body %q)", err, recorder.Body.String())
	}
	if len(body) != 3 || body["code"] != "task_conflict" {
		t.Fatalf("body = %v, want the bare envelope", body)
	}
}

func TestErrorMappersGolden(t *testing.T) {
	t.Parallel()

	errUnmapped := errors.New("unmapped failure")
	mappers := []struct {
		name  string
		write func(*gin.Context, error)
		cases []mappedErrorCase
	}{
		{name: "whiteboards", write: writeWhiteboardError, cases: []mappedErrorCase{
			{
				"missing",
				canvas.ErrBoardNotFound,
				404,
				"Whiteboard or file is unavailable",
				"whiteboard_not_found",
			},
			{
				"missing object",
				canvas.ErrBoardItemNotFound,
				404,
				"An object in this Whiteboard change no longer exists",
				"whiteboard_item_not_found",
			},
			{
				"forbidden",
				canvas.ErrBoardForbidden,
				403,
				"You do not have permission for this Whiteboard action",
				"whiteboard_forbidden",
			},
			{
				"reused request key",
				canvas.ErrBoardRequestKeyReused,
				422,
				"This request key was already used for a different Whiteboard change",
				"whiteboard_request_key_reused",
			},
			{"invalid", canvas.ErrBoardInvalid, 422, "Invalid Whiteboard operation", "whiteboard_invalid"},
			{
				"invalid, with a reason",
				fmt.Errorf(
					"compose: %w",
					canvas.BoardInvalidError{Reason: `style "solid" is not one of arrow, line`},
				),
				422,
				`Invalid Whiteboard operation: style "solid" is not one of arrow, line`,
				"whiteboard_invalid",
			},
			{
				"conflict",
				canvas.ErrBoardConflict,
				409,
				"The Whiteboard item changed. Review the saved version before retrying.",
				"whiteboard_conflict",
			},
			{
				"limit",
				canvas.ErrBoardLimit,
				422,
				"This Whiteboard operation exceeds the supported size",
				"whiteboard_limit",
			},
			{
				"locked object",
				canvas.ErrBoardLocked,
				409,
				"This Whiteboard object is locked. Unlock it before moving it.",
				"whiteboard_locked",
			},
			{
				"presence full",
				realtime.ErrCanvasPresenceFull,
				409,
				"Too many people are present on this Whiteboard",
				"whiteboard_presence_full",
			},
			{
				"storage unavailable",
				attachment.ErrStorageUnavailable,
				503,
				"Attachment storage is unavailable",
				"attachment_storage_unavailable",
			},
			{
				"file service unavailable",
				attachment.ErrUnavailable,
				503,
				"Attachment service is unavailable",
				"attachment_unavailable",
			},
		}},
		{name: "canvas", write: writeCanvasError, cases: []mappedErrorCase{
			{
				"authoring disabled",
				wiki.ErrUIAuthoringDisabled,
				http.StatusServiceUnavailable,
				"Intelligent UI authoring is temporarily disabled",
				"intelligent_ui_authoring_disabled",
			},
			{"missing", canvas.ErrNotFound, 404, "Canvas or source is unavailable", "canvas_not_found"},
			{
				"forbidden",
				canvas.ErrForbidden,
				403,
				"You do not have permission to change this canvas content",
				"canvas_forbidden",
			},
			{"invalid", canvas.ErrInvalid, 422, "Invalid canvas operation", "canvas_invalid"},
			{
				"invalid page insert",
				canvas.ErrPageInvalidInsert,
				422,
				"Invalid inserted block or destination. Use the native block shape from page_read. Headings and paragraphs cannot contain blocks: omit parentId for document-level siblings, and use afterId or beforeId to position them.",
				"page_invalid_insert",
			},
			{
				"conflict",
				canvas.ErrConflict,
				409,
				"The work changed. Refresh and try again.",
				"canvas_conflict",
			},
			{
				"limit",
				canvas.ErrLimit,
				422,
				"This canvas operation exceeds the supported size",
				"canvas_limit",
			},
		}},
		{
			name:  "huddle AI",
			write: writeHuddleAIError,
			cases: []mappedErrorCase{
				{
					"conflict",
					conversation.ErrHuddleAIConflict,
					409,
					"Huddle AI settings changed. Reload and retry.",
					"huddle_ai_conflict",
				},
				{
					"voice agents disabled",
					conversation.ErrHuddleAIDisabled,
					403,
					"Voice Agents are turned off on this server.",
					"huddle_ai_disabled",
				},
				{
					"tool limit",
					conversation.ErrHuddleToolLimit,
					429,
					"Voice lookup limit reached. Try again later.",
					"huddle_tool_limit",
				},
				{
					"proposal conflict",
					conversation.ErrHuddleProposalConflict,
					409,
					"Work proposal changed or is no longer editable. Reload and retry.",
					"huddle_proposal_conflict",
				},
				{
					"proposal limit",
					conversation.ErrHuddleProposalLimit,
					429,
					"This call has reached its work proposal limit.",
					"huddle_proposal_limit",
				},
			},
		},
		{
			name:  "huddle budget",
			write: writeHuddleBudgetError,
			cases: []mappedErrorCase{
				{
					"invalid",
					usage.ErrHuddleBudgetInvalid,
					422,
					"request validation failed",
					"validation_failed",
				},
				{
					"forbidden",
					usage.ErrHuddleBudgetForbidden,
					403,
					"Huddle budget access is not allowed.",
					"huddle_budget_forbidden",
				},
				{
					"conflict",
					usage.ErrHuddleBudgetConflict,
					409,
					"Huddle budget changed. Reload and retry.",
					"huddle_budget_conflict",
				},
				{
					"exhausted",
					usage.ErrHuddleBudgetExhausted,
					409,
					"Huddle AI budget is unavailable or exhausted.",
					"huddle_budget_exhausted",
				},
			},
		},
		{
			name:  "activity insight",
			write: writeActivityInsightError,
			cases: []mappedErrorCase{
				{
					"unauthenticated",
					usage.ErrUnauthenticated,
					http.StatusUnauthorized,
					"authentication is required",
					"authentication_required",
				},
				{
					"forbidden",
					usage.ErrActivityInsightForbidden,
					http.StatusForbidden,
					"Company access is required",
					"company_access_required",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		// ADR 0017: an Analytics filter naming a Project or Task the caller
		// does not read answers like one naming nothing, byte for byte.
		{
			name:  "analytics",
			write: writeAnalyticsError,
			cases: []mappedErrorCase{
				{
					"invalid",
					usage.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"forbidden",
					usage.ErrForbidden,
					http.StatusForbidden,
					"Your Company role does not allow this action",
					"company_permission_required",
				},
				{
					"task usage not found",
					usage.ErrTaskUsageNotFound,
					http.StatusNotFound,
					"Task not found",
					"task_not_found",
				},
				{
					"project filter not visible",
					auth.ErrProjectNotVisible,
					http.StatusNotFound,
					"Project not found",
					"project_not_found",
				},
				{
					"task filter not visible",
					auth.ErrTaskNotVisible,
					http.StatusNotFound,
					"Task not found",
					"task_not_found",
				},
				{
					"busy",
					usage.ErrAnalyticsBusy,
					http.StatusServiceUnavailable,
					"Analytics is busy; try again",
					"analytics_busy",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "run",
			write: writeRunError,
			cases: []mappedErrorCase{
				{
					"not found",
					execution.ErrRunNotFound,
					http.StatusNotFound,
					"Run not found",
					"run_not_found",
				},
				{
					"cancel permission required",
					execution.ErrRunCancelPermissionRequired,
					http.StatusForbidden,
					"Only the member whose computer runs it, owners and admins can stop this run.",
					"company_permission_required",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		// A Run's own reads and the daemon's uploads share one refusal pair.
		{
			name:  "run proof",
			write: writeRunProofError,
			cases: []mappedErrorCase{
				{
					"proof not the member's live Run",
					execution.ErrRunProofInvalid,
					http.StatusUnauthorized,
					"Run token is invalid",
					"run_token_invalid",
				},
				{
					"another Run or chat",
					task.ErrOutsideRunScope,
					http.StatusForbidden,
					"Permission denied. You can only access your own Tasks.",
					"task_outside_run_scope",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		// Task-origin Approvals and Questions are decided inside their Project
		// (ADR 0017).
		{
			name:  "approval",
			write: writeApprovalError,
			cases: []mappedErrorCase{
				{
					"not found",
					approval.ErrApprovalNotFound,
					http.StatusNotFound,
					"Approval not found",
					"approval_not_found",
				},
				{
					"Project decider permission",
					auth.ErrProjectPermissionRequired,
					http.StatusForbidden,
					"Your access to this Project does not allow this action",
					"project_permission_required",
				},
				{
					"no eligible resolver",
					auth.ErrNoEligibleResolver,
					http.StatusConflict,
					"No one who can see this Project may decide this, so this Run has been stopped",
					"no_eligible_resolver",
				},
				// A plan refused while it is raised or approved reads as the
				// direct apply's refusal.
				{
					"plan invalid",
					company.ErrPlanInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"plan conflict",
					company.ErrPlanConflict,
					http.StatusConflict,
					"Work Plan mutation conflicts with current state",
					"work_plan_conflict",
				},
			},
		},
		{
			name:  "question",
			write: writeQuestionError,
			cases: []mappedErrorCase{
				{
					"Project decider permission",
					auth.ErrProjectPermissionRequired,
					http.StatusForbidden,
					"Your access to this Project does not allow this action",
					"project_permission_required",
				},
				{
					"no eligible resolver",
					auth.ErrNoEligibleResolver,
					http.StatusConflict,
					"No one who can see this Project may decide this, so this Run has been stopped",
					"no_eligible_resolver",
				},
			},
		},
		{
			name:  "deliverable",
			write: writeDeliverableError,
			cases: []mappedErrorCase{
				{
					"invalid Run proof",
					execution.ErrRunProofInvalid,
					http.StatusUnauthorized,
					"Run token is invalid",
					"run_token_invalid",
				},
				{
					"invalid input",
					execution.ErrDeliverableInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"forbidden",
					execution.ErrDeliverableForbidden,
					http.StatusForbidden,
					"Artifact action is not permitted",
					"forbidden",
				},
				{
					"not found",
					execution.ErrDeliverableNotFound,
					http.StatusNotFound,
					"Run result not found",
					"not_found",
				},
				{
					"Artifacts not found",
					execution.ErrArtifactNotFound,
					http.StatusNotFound,
					"Conversation Artifacts not found",
					"not_found",
				},
				// A Task's result is reviewed inside its Project (ADR 0017).
				{
					"Project review permission",
					auth.ErrProjectPermissionRequired,
					http.StatusForbidden,
					"Your access to this Project does not allow this action",
					"project_permission_required",
				},
				{
					"no eligible reviewer",
					fmt.Errorf("present: %w", auth.ErrNoEligibleResolver),
					http.StatusConflict,
					"No one who can see this Project may decide this, so this Run has been stopped",
					"no_eligible_resolver",
				},
				{
					"conflict",
					execution.ErrDeliverableConflict,
					http.StatusConflict,
					"the Run result changed; refresh and try again",
					"conflict",
				},
				{
					"webapp forbidden",
					execution.ErrPublicationForbidden,
					http.StatusForbidden,
					"owner access is required",
					"forbidden",
				},
				{
					"webapp disabled",
					execution.ErrPublicationDisabled,
					http.StatusServiceUnavailable,
					"Public artifact publication is disabled",
					"publication_disabled",
				},
				{
					"webapp source invalid",
					execution.ErrPublicationNotPublishable,
					http.StatusUnprocessableEntity,
					"the primary output must be a verified ZIP archive",
					"output_not_publishable",
				},
				{
					"target unavailable",
					execution.ErrDeliverableTargetUnavailable,
					http.StatusUnprocessableEntity,
					"a submitted result item is unavailable",
					"target_unavailable",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"Run result is unavailable",
					"deliverable_unavailable",
				},
			},
		},
		{
			name:  "publication",
			write: writePublicationError,
			cases: []mappedErrorCase{
				{
					"subdomain taken",
					execution.ErrPublicationSubdomainTaken,
					http.StatusConflict,
					"this subdomain is already claimed",
					"publication_subdomain_taken",
				},
				{
					"subdomain disabled",
					execution.ErrPublicationSubdomainDisabled,
					http.StatusServiceUnavailable,
					"custom URL routing is not configured for this publication",
					"publication_subdomain_disabled",
				},
				{
					"invalid input",
					execution.ErrPublicationInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"forbidden",
					execution.ErrPublicationForbidden,
					http.StatusForbidden,
					"owner access is required",
					"forbidden",
				},
				{
					"not found",
					execution.ErrPublicationNotFound,
					http.StatusNotFound,
					"run result was not found",
					"not_found",
				},
				{
					"conflict",
					execution.ErrPublicationConflict,
					http.StatusConflict,
					"the publication changed; refresh and try again",
					"conflict",
				},
				{
					"not publishable",
					execution.ErrPublicationNotPublishable,
					http.StatusUnprocessableEntity,
					"the primary output must be a verified ZIP archive",
					"output_not_publishable",
				},
				{
					"approval required",
					execution.ErrPublicationApprovalRequired,
					http.StatusConflict,
					"approve this result before publishing it",
					"approval_required",
				},
				{
					"disabled",
					execution.ErrPublicationDisabled,
					http.StatusServiceUnavailable,
					"Public artifact publication is disabled",
					"publication_disabled",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"publication is unavailable",
					"publication_unavailable",
				},
			},
		},
		{
			name: "terminal run",
			write: func(c *gin.Context, err error) {
				writeTerminalRunError(c, err, boundaryErrorRunCompletionFailed)
			},
			cases: []mappedErrorCase{
				{
					"head mismatch",
					execution.ErrTranscriptHeadMismatch,
					http.StatusConflict,
					"terminal transcript head does not match the committed run transcript",
					"run_transcript_head_mismatch",
				},
				{
					"final item invalid",
					execution.ErrTerminalFinalItemInvalid,
					http.StatusConflict,
					"terminal transcript final item is not the latest committed agent text item",
					"run_terminal_item_invalid",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"failed to complete run",
					"internal_error",
				},
			},
		},
		{
			name:  "run usage",
			write: writeRunUsageError,
			cases: []mappedErrorCase{
				{
					"conflict",
					usage.ErrRunUsageConflict,
					http.StatusConflict,
					"run usage conflicts with the stored immutable fact",
					"run_usage_conflict",
				},
				{
					"invalid",
					usage.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "attachment",
			write: writeAttachmentError,
			cases: []mappedErrorCase{
				{
					"invalid input",
					attachment.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"conflict",
					attachment.ErrConflict,
					http.StatusConflict,
					"Attachment conflicts with current state",
					"attachment_conflict",
				},
				{
					"not found",
					attachment.ErrNotFound,
					http.StatusNotFound,
					"Attachment not found",
					"attachment_not_found",
				},
				{
					"upload incomplete",
					attachment.ErrUploadIncomplete,
					http.StatusConflict,
					"Attachment upload is incomplete",
					"attachment_upload_incomplete",
				},
				{
					"upload expired",
					attachment.ErrUploadExpired,
					http.StatusGone,
					"Attachment upload expired",
					"attachment_upload_expired",
				},
				{
					"integrity mismatch",
					attachment.ErrIntegrityMismatch,
					http.StatusUnprocessableEntity,
					"Attachment content does not match its metadata",
					"attachment_integrity_mismatch",
				},
				{
					"storage unavailable",
					attachment.ErrStorageUnavailable,
					http.StatusServiceUnavailable,
					"Attachment storage is unavailable",
					"attachment_storage_unavailable",
				},
				{
					"unavailable",
					attachment.ErrUnavailable,
					http.StatusServiceUnavailable,
					"Attachment service is unavailable",
					"attachment_unavailable",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "auth",
			write: writeAuthError,
			cases: []mappedErrorCase{
				{
					"invalid input",
					auth.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"email exists",
					auth.ErrEmailExists,
					http.StatusConflict,
					"email is already registered",
					"email_already_registered",
				},
				{
					"invalid credentials",
					auth.ErrInvalidCredentials,
					http.StatusUnauthorized,
					"email or password is incorrect",
					"invalid_credentials",
				},
				{
					"invalid session",
					auth.ErrInvalidSession,
					http.StatusUnauthorized,
					"session is invalid or expired",
					"session_invalid",
				},
				{
					"refresh token reused",
					auth.ErrRefreshTokenReused,
					http.StatusUnauthorized,
					"session is invalid or expired",
					"session_invalid",
				},
				{
					"session already rotated",
					auth.ErrSessionAlreadyRotated,
					http.StatusConflict,
					"session was already rotated; retry with the current credential",
					"session_already_rotated",
				},
				{
					"account disabled",
					auth.ErrAccountDisabled,
					http.StatusForbidden,
					"account is disabled",
					"account_disabled",
				},
				{
					"oauth not configured",
					auth.ErrOAuthNotConfigured,
					http.StatusServiceUnavailable,
					"OAuth provider is not configured",
					"oauth_not_configured",
				},
				{
					"oauth flow invalid",
					auth.ErrOAuthFlowInvalid,
					http.StatusBadRequest,
					"OAuth flow is invalid or expired",
					"oauth_flow_invalid",
				},
				{
					"oauth provider denied",
					auth.ErrOAuthProviderDenied,
					http.StatusUnauthorized,
					"OAuth authorization was denied",
					"oauth_provider_denied",
				},
				{
					"oauth provider unavailable",
					auth.ErrOAuthProviderUnavailable,
					http.StatusBadGateway,
					"OAuth provider is unavailable",
					"oauth_provider_unavailable",
				},
				{
					"oauth identity invalid",
					auth.ErrOAuthIdentityInvalid,
					http.StatusUnauthorized,
					"OAuth identity is invalid",
					"oauth_identity_invalid",
				},
				{
					"oauth email unverified",
					auth.ErrOAuthEmailUnverified,
					http.StatusForbidden,
					"OAuth email is not verified",
					"oauth_email_unverified",
				},
				{
					"oauth account conflict",
					auth.ErrOAuthAccountConflict,
					http.StatusConflict,
					"OAuth identity conflicts with an existing account",
					"oauth_account_conflict",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "company",
			write: writeCompanyError,
			cases: []mappedErrorCase{
				{
					"last owner",
					company.ErrLastOwner,
					http.StatusConflict,
					"The last owner cannot leave or be removed",
					"team_last_owner",
				},
				{
					"member revision",
					company.ErrMemberRevisionConflict,
					http.StatusConflict,
					"Membership changed; refresh and try again",
					"team_member_revision_conflict",
				},

				{
					"unauthenticated",
					company.ErrUnauthenticated,
					http.StatusUnauthorized,
					"authentication is required",
					"authentication_required",
				},
				{
					"invalid input",
					company.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"conflict",
					company.ErrConflict,
					http.StatusConflict,
					"Company creation conflicts with an existing resource",
					"company_create_conflict",
				},
				{
					"delete conflict",
					company.ErrDeleteConflict,
					http.StatusConflict,
					"Company changed; refresh and confirm deletion again",
					"company_delete_conflict",
				},
				{
					"not found",
					company.ErrNotFound,
					http.StatusNotFound,
					"Company not found",
					"company_not_found",
				},
				{
					"forbidden",
					company.ErrForbidden,
					http.StatusForbidden,
					"Your Company role does not allow this action",
					"company_permission_required",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "company search",
			write: writeCompanySearchError,
			cases: []mappedErrorCase{
				{
					"unauthenticated",
					company.ErrUnauthenticated,
					http.StatusUnauthorized,
					"authentication is required",
					"authentication_required",
				},
				{
					"invalid input",
					company.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "conversation",
			write: writeConversationError,
			cases: []mappedErrorCase{
				{
					"unauthenticated",
					conversation.ErrUnauthenticated,
					http.StatusUnauthorized,
					"authentication is required",
					"authentication_required",
				},
				{
					"invalid input",
					conversation.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				// Posting into a Task chat without project.tasks.write (ADR 0017).
				{
					"Task chat post without write",
					auth.ErrProjectPermissionRequired,
					http.StatusForbidden,
					"Your access to this Project does not allow this action",
					"project_permission_required",
				},
				// A Task chat message whose Run the assignee may not start
				// (ADR 0017) answers exactly like a refused Task start.
				{
					"Task chat Run without execute",
					fmt.Errorf("check Task Message Run: %w",
						&execution.ExecutePermissionError{AgentName: "Builder"}),
					http.StatusForbidden,
					"Builder didn't start: you can't run tasks here.",
					"project_permission_required",
				},
				{
					"chat model assignee required",
					conversation.ErrModelSettingsForbidden,
					http.StatusForbidden,
					"only the Task assignee with execution permission may change its chat model",
					"conversation_model_settings_forbidden",
				},
				{
					"conflict",
					conversation.ErrConflict,
					http.StatusConflict,
					"Conversation mutation conflicts with current state",
					"conversation_conflict",
				},
				{
					"company preparation pending",
					conversation.ErrInitializationPending,
					http.StatusConflict,
					"This conversation is waiting for your company research to start. You can explore the workspace meanwhile.",
					"conversation_initialization_pending",
				},
				{
					"agent binding required",
					execution.ErrRunAgentNotBound,
					http.StatusConflict,
					"set this Agent up on one of your computers first",
					"agent_binding_required",
				},
				{
					"run queue conflict",
					conversation.ErrRunQueueConflict,
					http.StatusConflict,
					"Run queue conflicts with current state",
					"run_queue_conflict",
				},
				{
					"run queue full",
					conversation.ErrRunQueueFull,
					http.StatusConflict,
					"Run queue is full",
					"run_queue_full",
				},
				{
					"not found",
					conversation.ErrNotFound,
					http.StatusNotFound,
					"Conversation not found",
					"conversation_not_found",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "company resource",
			write: writeCompanyResourceError,
			cases: []mappedErrorCase{
				{
					"unauthenticated",
					company.ErrUnauthenticated,
					http.StatusUnauthorized,
					"authentication is required",
					"authentication_required",
				},
				{
					"invalid input",
					company.ErrResourceInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"conflict",
					company.ErrResourceConflict,
					http.StatusConflict,
					"Company resource mutation conflicts with current state",
					"company_resource_conflict",
				},
				{
					"not found",
					company.ErrResourceNotFound,
					http.StatusNotFound,
					"Company resource not found",
					"company_resource_not_found",
				},
				{
					"forbidden",
					company.ErrForbidden,
					http.StatusForbidden,
					"Your Company role does not allow this action",
					"company_permission_required",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		// Groups reuse the Company's own not-found and
		// permission sentinels, answered as the Team routes answer them.
		{
			name:  "group",
			write: writeGroupError,
			cases: []mappedErrorCase{
				{
					"invalid",
					company.ErrGroupInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"company not found",
					company.ErrNotFound,
					http.StatusNotFound,
					"Company not found",
					"company_not_found",
				},
				{
					"company permission",
					company.ErrForbidden,
					http.StatusForbidden,
					"Your Company role does not allow this action",
					"company_permission_required",
				},
				{
					"not found",
					company.ErrGroupNotFound,
					http.StatusNotFound,
					"Group not found",
					"group_not_found",
				},
				{
					"conflict",
					company.ErrGroupConflict,
					http.StatusConflict,
					"The group changed or another group has this name; reload it and try again",
					"group_conflict",
				},
				{
					"member required",
					company.ErrGroupMemberRequired,
					http.StatusConflict,
					"Only active Team members can be in a group",
					"group_member_required",
				},
				{
					"limit",
					company.ErrGroupLimitReached,
					http.StatusConflict,
					"Group limit reached: 100 groups, 500 people in a group, or 50 groups on a project",
					"group_limit_reached",
				},
				{
					"no laundering",
					company.ErrGroupGrantPermissionRequired,
					http.StatusForbidden,
					"This change moves access on a project where you cannot change access yourself",
					"group_grant_permission_required",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		// The two Run-question resources are one domain behind two HTTP
		// surfaces. Five collapsed sentinels reach two mapping tables, so both
		// tables need golden cases: that is what makes the collapse legal.
		// ADR 0017: a Project the caller cannot see answers exactly like a
		// missing one, whichever package's sentinel carries it.
		{
			name:  "project",
			write: writeProjectError,
			cases: []mappedErrorCase{
				{
					"not found",
					company.ErrProjectNotFound,
					http.StatusNotFound,
					"Project not found",
					"project_not_found",
				},
				{
					"not visible",
					auth.ErrProjectNotVisible,
					http.StatusNotFound,
					"Project not found",
					"project_not_found",
				},
				{
					"project permission required",
					auth.ErrProjectPermissionRequired,
					http.StatusForbidden,
					"Your access to this Project does not allow this action",
					"project_permission_required",
				},
				{
					"company permission required",
					company.ErrProjectForbidden,
					http.StatusForbidden,
					"Your Company role does not allow this action",
					"company_permission_required",
				},
				{
					"member required",
					company.ErrProjectMemberRequired,
					http.StatusConflict,
					"Project access can be given only to an active Company member",
					"project_member_required",
				},
				{
					"access revision conflict",
					company.ErrProjectGrantRevisionConflict,
					http.StatusConflict,
					"Project access changed; reload it and try again",
					"project_grant_revision_conflict",
				},
				{
					"self-grant cap",
					company.ErrProjectGrantExceedsOwn,
					http.StatusForbidden,
					"You can only give or change access you have on this Project",
					"project_grant_exceeds_own",
				},
				{
					"visibility floor",
					company.ErrProjectVisibilityFloor,
					http.StatusConflict,
					"Everyone keeps read, task and run access on a Project open to the Company; make it restricted first",
					"project_visibility_floor",
				},
				// A Group grant answers these two as the Group routes do.
				{
					"Group not found",
					company.ErrGroupNotFound,
					http.StatusNotFound,
					"Group not found",
					"group_not_found",
				},
				{
					"Group limit",
					company.ErrGroupLimitReached,
					http.StatusConflict,
					"Group limit reached: 100 groups, 500 people in a group, or 50 groups on a project",
					"group_limit_reached",
				},
				// A Project read sent through a Run's channel (ADR 0017).
				{
					"Run proof invalid",
					execution.ErrRunProofInvalid,
					http.StatusUnauthorized,
					"Run token is invalid",
					"run_token_invalid",
				},
				{
					"outside the Run's scope",
					task.ErrOutsideRunScope,
					http.StatusForbidden,
					"Permission denied. You can only access your own Tasks.",
					"task_outside_run_scope",
				},
				// A grant naming a permission outside the catalog: the
				// answer names it, so an old desktop sending the removed
				// project.results.review is told which one.
				{
					"unknown permission",
					auth.ErrProjectPermissionUnknown,
					http.StatusUnprocessableEntity,
					"Unknown Project permission",
					"project_permission_unknown",
				},
				{
					"unknown permission named",
					&auth.ProjectPermissionUnknownError{Name: "project.results.review"},
					http.StatusUnprocessableEntity,
					`Unknown Project permission "project.results.review"`,
					"project_permission_unknown",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		// A manual merge: a stale review is a conflict, a member who may
		// not run Tasks in the Project is told that instead.
		{
			name:  "task merge",
			write: writeTaskMergeError,
			cases: []mappedErrorCase{
				{
					"claim not found",
					task.ErrMergeClaimNotFound,
					http.StatusNotFound,
					"Task merge claim not found",
					"task_merge_claim_not_found",
				},
				{
					"stale review",
					task.ErrMergeConflict,
					http.StatusConflict,
					"Task merge conflicts with current state",
					"task_merge_conflict",
				},
				{
					"project permission required",
					auth.ErrProjectPermissionRequired,
					http.StatusForbidden,
					"Your access to this Project does not allow this action",
					"project_permission_required",
				},
			},
		},
		{
			name:  "task",
			write: writeTaskError,
			cases: []mappedErrorCase{
				{
					"result selection required",
					task.ErrResultSelectionRequired,
					http.StatusConflict,
					"Select the delivered results before completing this Task",
					"task_result_selection_required",
				},
				{
					"result application required",
					task.ErrResultApplicationRequired,
					http.StatusConflict,
					"Accept the required suggestions before approving these results",
					"task_result_application_required",
				},

				{
					"unauthenticated",
					task.ErrUnauthenticated,
					http.StatusUnauthorized,
					"authentication is required",
					"authentication_required",
				},
				{
					"invalid Run proof",
					execution.ErrRunProofInvalid,
					http.StatusUnauthorized,
					"Run token is invalid",
					"run_token_invalid",
				},
				{
					"invalid input",
					task.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"conflict",
					task.ErrConflict,
					http.StatusConflict,
					"Task mutation conflicts with current state",
					"task_conflict",
				},
				{
					"assignee required",
					task.ErrAssigneeRequired,
					http.StatusForbidden,
					"only the Task's assignee can start it; it runs on their computer",
					"task_assignee_required",
				},
				// One code, three details: each tells the member what they may
				// do instead.
				{
					"assign permission required",
					task.ErrAssignPermissionRequired,
					http.StatusForbidden,
					"Only owners and admins can assign a task to someone else. You can assign it to yourself.",
					"company_permission_required",
				},
				{
					"handoff permission required",
					task.ErrHandoffPermissionRequired,
					http.StatusForbidden,
					"Only the task's assignee, owners and admins can change who works on it.",
					"company_permission_required",
				},
				{
					"review permission required",
					task.ErrReviewPermissionRequired,
					http.StatusForbidden,
					"Only the task's assignee, owners and admins can move it out of review.",
					"company_permission_required",
				},
				{
					"assignee inactive",
					task.ErrAssigneeInactive,
					http.StatusConflict,
					"the assignee is not an active member of this Company",
					"task_conflict",
				},
				// One code, two details: the refusal says whether confirming
				// cancels a Run in flight or only moves a Task that ran before.
				{
					"handoff confirmation required",
					task.ErrHandoffConfirmationRequired,
					http.StatusConflict,
					"this Task has run before; confirm the handoff to move it and start the work over",
					"task_handoff_confirmation_required",
				},
				{
					"handoff confirmation required, nothing live",
					fmt.Errorf("update: %w", &task.HandoffConfirmationError{}),
					http.StatusConflict,
					"this Task has run before; confirm the handoff to move it and start the work over",
					"task_handoff_confirmation_required",
				},
				{
					"handoff confirmation required, run live",
					fmt.Errorf("update: %w", &task.HandoffConfirmationError{RunLive: true}),
					http.StatusConflict,
					"this Task has a run in flight; confirm the handoff to cancel it and move the Task",
					"task_handoff_confirmation_required",
				},
				{
					"agent binding required",
					execution.ErrRunAgentNotBound,
					http.StatusConflict,
					"set this Agent up on one of your computers first",
					"agent_binding_required",
				},
				{
					"kernel assignee fence",
					execution.ErrRunTaskAssigneeRequired,
					http.StatusForbidden,
					"only the Task's assignee can start it; it runs on their computer",
					"task_assignee_required",
				},
				// A Run reads only its own Tasks. One code and one fixed
				// sentence: it names no Task, member or Agent.
				{
					"outside the Run's scope",
					task.ErrOutsideRunScope,
					http.StatusForbidden,
					"Permission denied. You can only access your own Tasks.",
					"task_outside_run_scope",
				},
				{
					"not found",
					task.ErrNotFound,
					http.StatusNotFound,
					"Task not found",
					"task_not_found",
				},
				// ADR 0017: a Task in a Project the caller cannot read collapses
				// into the missing-Task answer, byte for byte.
				{
					"not visible",
					auth.ErrTaskNotVisible,
					http.StatusNotFound,
					"Task not found",
					"task_not_found",
				},
				{
					"project permission required",
					auth.ErrProjectPermissionRequired,
					http.StatusForbidden,
					"Your access to this Project does not allow this action",
					"project_permission_required",
				},
				// A list filtered by a Project the caller cannot read answers
				// like one filtered by a Project that does not exist.
				{
					"project filter not visible",
					auth.ErrProjectNotVisible,
					http.StatusNotFound,
					"Project not found",
					"project_not_found",
				},
				{
					"assignee outside the Project",
					task.ErrAssigneeOutsideProject,
					http.StatusConflict,
					"the assignee cannot see this Project; give them access to it first",
					"project_member_required",
				},
				// The execute refusal names the Agent in a few words, never
				// the Project, and tells nobody to ask someone; one code with
				// the plain permission row.
				{
					"execute permission required",
					fmt.Errorf(
						"start: %w",
						&execution.ExecutePermissionError{AgentName: "Builder"},
					),
					http.StatusForbidden,
					"Builder didn't start: you can't run tasks here.",
					"project_permission_required",
				},
				{
					"execute permission required, a colleague's Task",
					&execution.ExecutePermissionError{AssigneeID: uuid.New(), AgentName: "Builder"},
					http.StatusForbidden,
					"Builder didn't start: the assignee can't run tasks here.",
					"project_permission_required",
				},
				{
					"execute permission required, Agent unreadable",
					&execution.ExecutePermissionError{},
					http.StatusForbidden,
					"You can't run tasks here.",
					"project_permission_required",
				},
				{
					"execute permission required, a colleague's Task, Agent unreadable",
					&execution.ExecutePermissionError{AssigneeID: uuid.New()},
					http.StatusForbidden,
					"The assignee can't run tasks here.",
					"project_permission_required",
				},
				{
					"dependency blocked",
					task.ErrDependencyBlocked,
					http.StatusConflict,
					"Task execution is blocked by an incomplete dependency",
					"task_dependency_blocked",
				},
				{
					"attachment unavailable",
					task.ErrAttachmentUnavailable,
					http.StatusConflict,
					"Task attachment is unavailable",
					"task_attachment_unavailable",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "task failure",
			write: writeTaskFailureError,
			cases: []mappedErrorCase{
				{
					"invalid Run proof",
					execution.ErrRunProofInvalid,
					http.StatusUnauthorized,
					"Run token is invalid",
					"run_token_invalid",
				},
				{
					"invalid input",
					task.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"conflict",
					task.ErrConflict,
					http.StatusConflict,
					"Task mutation conflicts with current state",
					"task_conflict",
				},
				{
					"not found",
					task.ErrNotFound,
					http.StatusNotFound,
					"Task not found",
					"task_not_found",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "agent",
			write: writeAgentError,
			cases: []mappedErrorCase{
				{
					"unauthenticated",
					agent.ErrUnauthenticated,
					http.StatusUnauthorized,
					"authentication is required",
					"authentication_required",
				},
				{
					"invalid input",
					agent.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"thinking level outside the provider vocabulary",
					agent.ErrThinkingLevelUnsupported,
					http.StatusUnprocessableEntity,
					"thinkingLevel is not a level the template's provider accepts" +
						" (Claude Code: low, medium, high, xhigh, max; Codex: a level from the runtime's model catalog)",
					"validation_failed",
				},
				{
					"provider named on an update",
					agent.ErrProviderImmutable,
					http.StatusUnprocessableEntity,
					"An agent's provider is fixed; duplicate the agent to use the other provider",
					"validation_failed",
				},
				{
					"conflict",
					agent.ErrConflict,
					http.StatusConflict,
					"Agent creation conflicts with an existing resource",
					"agent_create_conflict",
				},
				{
					"revision conflict",
					agent.ErrRevisionConflict,
					http.StatusConflict,
					"Agent mutation conflicts with the current revision",
					"agent_revision_conflict",
				},
				{
					"cycle",
					agent.ErrCycle,
					http.StatusConflict,
					"Agent reports-to relationship would create a cycle",
					"agent_reports_to_cycle",
				},
				{
					"root immutable",
					agent.ErrRootImmutable,
					http.StatusConflict,
					"root Agent topology is immutable",
					"root_agent_topology_immutable",
				},
				{
					"archive blocked",
					agent.ErrArchiveBlocked,
					http.StatusConflict,
					"Agent has active dependents",
					"agent_archive_blocked",
				},
				{
					"not found",
					agent.ErrNotFound,
					http.StatusNotFound,
					"Agent not found",
					"agent_not_found",
				},
				{
					"forbidden",
					agent.ErrForbidden,
					http.StatusForbidden,
					"only the Agent's creator, or a Company owner or admin, may do this; the root Agent needs agents.manage",
					"agent_creator_required",
				},
				{
					"runtime the caller may not bind onto",
					agent.ErrRuntimeNotFound,
					http.StatusNotFound,
					"Runtime not found",
					"runtime_not_found",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "runtime",
			write: writeRuntimeError,
			cases: []mappedErrorCase{
				{
					"not found",
					agent.ErrRuntimeNotFound,
					http.StatusNotFound,
					"Runtime not found",
					"runtime_not_found",
				},
				{
					"offline",
					agent.ErrRuntimeOffline,
					http.StatusConflict,
					"Runtime is offline",
					"runtime_offline",
				},
				{
					"removal target of another provider",
					agent.ErrBindingProviderMismatch,
					http.StatusUnprocessableEntity,
					"the target runtime runs a different provider than the agents bound here",
					"agent_binding_provider_mismatch",
				},
				{
					"model discovery not found",
					agent.ErrModelDiscoveryNotFound,
					http.StatusNotFound,
					"Model discovery not found",
					"model_discovery_not_found",
				},
				{
					"model discovery not running",
					agent.ErrModelDiscoveryNotRunning,
					http.StatusConflict,
					"Model discovery is not running",
					"model_discovery_not_running",
				},
				{
					"model discovery invalid",
					agent.ErrModelDiscoveryInvalid,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"rename input invalid",
					agent.ErrRuntimeInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"remove blocked: online",
					agent.ErrRuntimeRemoveBlockedOnline,
					http.StatusConflict,
					"Quit Zenith Team on that computer first, or stop its zen daemon, then try again",
					"runtime_remove_blocked",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "wiki",
			write: writeWikiError,
			cases: []mappedErrorCase{
				{
					"authoring disabled",
					wiki.ErrUIAuthoringDisabled,
					http.StatusServiceUnavailable,
					"Intelligent UI authoring is temporarily disabled",
					"intelligent_ui_authoring_disabled",
				},
				{
					"invalid input",
					wiki.ErrPageInvalidInput,
					http.StatusUnprocessableEntity,
					"Invalid Wiki input",
					"validation_failed",
				},
				{
					"body too large",
					wiki.ErrPageBodyTooLarge,
					http.StatusRequestEntityTooLarge,
					"Wiki page is too large",
					"request_too_large",
				},
				{
					"publish forbidden",
					wiki.ErrPageForbidden,
					http.StatusForbidden,
					"Your Company role does not allow Wiki publishing",
					"company_permission_required",
				},
				{
					"page not found",
					wiki.ErrPageNotFound,
					http.StatusNotFound,
					"Wiki page not found",
					"wiki_page_not_found",
				},
				{
					"collection not found",
					wiki.ErrCollectionNotFound,
					http.StatusNotFound,
					"Wiki collection not found",
					"wiki_collection_not_found",
				},
				{
					"page conflict",
					wiki.ErrPageConflict,
					http.StatusConflict,
					"Wiki page changed since you opened it",
					"wiki_page_conflict",
				},
				{
					"page archived",
					wiki.ErrPageArchived,
					http.StatusConflict,
					"Wiki page is archived",
					"wiki_page_archived",
				},
				{
					"collection conflict",
					wiki.ErrCollectionConflict,
					http.StatusConflict,
					"Wiki collection changed or name is already used",
					"wiki_collection_conflict",
				},
				{
					"agent access disabled",
					wiki.ErrAgentAccessDisabled,
					http.StatusForbidden,
					"Company Wiki reading is switched off for this Agent",
					"wiki_agent_access_disabled",
				},
				{
					"agent write forbidden",
					wiki.ErrAgentWriteForbidden,
					http.StatusForbidden,
					"This Run may not write the Company Wiki",
					"wiki_agent_write_forbidden",
				},
				{
					"agent write limit",
					wiki.ErrAgentWriteLimit,
					http.StatusUnprocessableEntity,
					"A Run can write at most 40 Wiki pages",
					"wiki_agent_write_limit",
				},
				{
					"cited Artifact unavailable",
					wiki.ErrArtifactRefUnavailable,
					http.StatusUnprocessableEntity,
					"A cited Artifact is missing or you cannot open it",
					"wiki_artifact_ref_unavailable",
				},
				{
					"too many cited Artifacts",
					wiki.ErrArtifactRefLimit,
					http.StatusUnprocessableEntity,
					"A Wiki page can cite at most 50 Artifacts",
					"wiki_artifact_ref_limit",
				},
				{
					"run not found",
					wiki.ErrRunNotFound,
					http.StatusNotFound,
					"Run not found",
					"run_not_found",
				},
				{
					"run proof invalid",
					execution.ErrRunProofInvalid,
					http.StatusUnauthorized,
					"Run token is invalid",
					"run_token_invalid",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			name:  "agent request",
			write: writeAgentRequestError,
			cases: []mappedErrorCase{
				{
					"json shape",
					errInvalidAgentJSONShape,
					http.StatusBadRequest,
					"request body is invalid",
					"invalid_request",
				},
				{
					"invalid input",
					agent.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
		{
			// Workflow readiness reuses the Agent surface's own answers for
			// the agent sentinels it collapses, so both tables pin them.
			name: "workflow",
			write: func(c *gin.Context, err error) {
				writeMappedError(c, err, workflowErrorMappings, internalErrorFallback)
			},
			cases: []mappedErrorCase{
				{
					"workflow not found",
					workflow.ErrNotFound,
					http.StatusNotFound,
					"workflow not found",
					"workflow_not_found",
				},
				{
					"unauthenticated",
					agent.ErrUnauthenticated,
					http.StatusUnauthorized,
					"authentication is required",
					"authentication_required",
				},
				{
					"invalid input",
					agent.ErrInvalidInput,
					http.StatusUnprocessableEntity,
					"request validation failed",
					"validation_failed",
				},
				{
					"agent not found",
					agent.ErrNotFound,
					http.StatusNotFound,
					"Agent not found",
					"agent_not_found",
				},
				{
					"fallback",
					errUnmapped,
					http.StatusInternalServerError,
					"internal server error",
					"internal_error",
				},
			},
		},
	}

	for _, mapper := range mappers {
		for _, mapped := range mapper.cases {
			t.Run(mapper.name+"/"+mapped.name, func(t *testing.T) {
				t.Parallel()
				// Services wrap sentinels with fmt.Errorf("...: %w", err); the
				// mappers must match through the wrap chain.
				err := fmt.Errorf("wrapped for golden test: %w", mapped.err)
				assertMappedErrorResponse(t, func(c *gin.Context) {
					mapper.write(c, err)
				}, mapped.status, mapped.detail, mapped.code)
			})
		}
	}
}

func TestInitializationErrorMappings(t *testing.T) {
	t.Parallel()
	for _, tc := range []mappedErrorCase{
		{"model", company.ErrInitializationModel, 409, "Luna is not available on this Codex runtime. Refresh model discovery and try again.", "initialization_model_unavailable"},
		{"conflict", company.ErrInitializationConflict, 409, "Initialization cannot advance; refresh and retry", "initialization_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertMappedErrorResponse(
				t,
				func(c *gin.Context) { writeInitializationError(c, tc.err) },
				tc.status,
				tc.detail,
				tc.code,
			)
		})
	}
}
