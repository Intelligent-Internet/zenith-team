package task

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTaskTextContractsMatchSharedSchemas(t *testing.T) {
	t.Parallel()

	input := validCreateInput()
	input.Title = strings.Repeat("t", 240)
	input.Description = strings.Repeat("d", 64*1024)
	if _, err := normalizeCreateInput(input, callerID); err != nil {
		t.Fatalf("normalizeCreateInput(contract limits) error = %v", err)
	}
	input.Title += "t"
	if _, err := normalizeCreateInput(input, callerID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("normalizeCreateInput(overlong title) error = %v, want %v", err, ErrInvalidInput)
	}
}

// callerID is the acting member every normalization test creates as.
var callerID = uuid.MustParse("0198bb00-0000-7000-8000-0000000000aa")

// The assignment rule (ADR 0014): the caller is the assignee unless the
// create names another member; the assignee does the work themself unless
// the create names an Agent; a user executedBy must be the assignee.
func TestNormalizeCreateInputSettlesAssigneeAndExecutedBy(t *testing.T) {
	t.Parallel()

	input := validCreateInput()
	input.Assignee = ActorRef{}
	input.ExecutedBy = nil
	got, err := normalizeCreateInput(input, callerID)
	if err != nil {
		t.Fatalf("normalizeCreateInput(defaults) error = %v", err)
	}
	if got.Assignee != (ActorRef{Kind: ActorUser, ID: callerID}) ||
		got.ExecutedBy == nil || *got.ExecutedBy != got.Assignee {
		t.Fatalf("defaults = assignee %+v executedBy %+v, want the caller doing their own work", got.Assignee, got.ExecutedBy)
	}

	other := uuid.MustParse("0198bb00-0000-7000-8000-0000000000bb")
	agent := ActorRef{Kind: ActorAgent, ID: uuid.MustParse("0198bb00-0000-7000-8000-0000000000cc")}
	input.Assignee = ActorRef{Kind: ActorUser, ID: other}
	input.ExecutedBy = &agent
	got, err = normalizeCreateInput(input, callerID)
	if err != nil || got.Assignee.ID != other || got.ExecutedBy.ID != agent.ID {
		t.Fatalf("normalizeCreateInput(other member, agent) = %+v / %+v, %v", got.Assignee, got.ExecutedBy, err)
	}
	self := ActorRef{Kind: ActorUser, ID: other}
	input.ExecutedBy = &self
	if _, err := normalizeCreateInput(input, callerID); err != nil {
		t.Fatalf("normalizeCreateInput(assignee does it themself) error = %v", err)
	}

	for name, mutate := range map[string]func(*CreateInput){
		"agent assignee":          func(in *CreateInput) { in.Assignee = agent },
		"executedBy other member": func(in *CreateInput) { in.ExecutedBy = &ActorRef{Kind: ActorUser, ID: callerID} },
		"executedBy unknown kind": func(in *CreateInput) { in.ExecutedBy = &ActorRef{Kind: "bogus", ID: agent.ID} },
		"executedBy nil agent id": func(in *CreateInput) { in.ExecutedBy = &ActorRef{Kind: ActorAgent} },
		"assignee with nil user":  func(in *CreateInput) { in.Assignee = ActorRef{Kind: ActorUser} },
		"executedBy nil user self": func(in *CreateInput) {
			in.Assignee = ActorRef{Kind: ActorUser, ID: other}
			in.ExecutedBy = &ActorRef{Kind: ActorUser}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := validCreateInput()
			in.Assignee = ActorRef{Kind: ActorUser, ID: other}
			mutate(&in)
			if _, err := normalizeCreateInput(in, callerID); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeCreateInput() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestNormalizeCreateInputAppliesDefaultsWithoutClientOwnedIdentities(t *testing.T) {
	t.Parallel()

	input := validCreateInput()
	input.Title = "\u00a0 Fix the release gate \u202f"
	input.Description = "\t Inspect the failing tests \n"
	input.Status = ""
	input.Priority = ""

	got, err := normalizeCreateInput(input, callerID)
	if err != nil {
		t.Fatalf("normalizeCreateInput() error = %v", err)
	}
	if got.Title != "Fix the release gate" || got.Description != "Inspect the failing tests" ||
		got.Status != StatusBacklog || got.Priority != PriorityMedium {
		t.Fatalf("normalizeCreateInput() = %+v", got)
	}

	if got.AttachmentIDs == nil || len(got.AttachmentIDs) != 0 {
		t.Fatalf("normalizeCreateInput() attachments = %#v, want an empty set", got.AttachmentIDs)
	}
	withFiles := input
	for range maxCreateAttachments {
		withFiles.AttachmentIDs = append(withFiles.AttachmentIDs, uuid.Must(uuid.NewV7()))
	}
	if got, err := normalizeCreateInput(withFiles, callerID); err != nil || len(got.AttachmentIDs) != maxCreateAttachments {
		t.Fatalf("normalizeCreateInput(%d files) = %d files, %v", maxCreateAttachments, len(got.AttachmentIDs), err)
	}

	userAssigned := input
	userAssigned.Assignee = ActorRef{Kind: ActorUser, ID: uuid.Must(uuid.NewV7())}
	if _, err := normalizeCreateInput(userAssigned, callerID); err != nil {
		t.Fatalf("normalizeCreateInput(user assigned) error = %v", err)
	}

	// An omitted key is not a malformed one: Create mints {prefix}-{number}.
	unkeyed := input
	unkeyed.Key = ""
	if got, err := normalizeCreateInput(unkeyed, callerID); err != nil || got.Key != "" {
		t.Fatalf("normalizeCreateInput(no key) = %q, %v; want an empty key and no error", got.Key, err)
	}
	if got := MintKey("ENG", 14); got != "ENG-14" {
		t.Fatalf("MintKey() = %q, want ENG-14", got)
	}
	for key, want := range map[string]bool{
		"ENG-14": true, "eng-14": true, "ENG-0": true,
		"ENG-": false, "ENG-14a": false, "ENG-x-14": false, "XENG-14": false,
		"ENG_14": false, "ship_release": false, "eng-14-": false,
	} {
		if got := KeyInMintedNamespace("ENG", key); got != want {
			t.Errorf("KeyInMintedNamespace(ENG, %q) = %t, want %t", key, got, want)
		}
	}
}

func TestNormalizeTaskInputsRejectMalformedOrCrossedValues(t *testing.T) {
	t.Parallel()

	validCreate := validCreateInput()
	invalidCreates := []CreateInput{
		func() CreateInput { value := validCreate; value.Title = "   "; return value }(),
		func() CreateInput { value := validCreate; value.Key = "   "; return value }(),
		func() CreateInput { value := validCreate; value.Status = "closed"; return value }(),
		func() CreateInput { value := validCreate; value.Priority = "critical"; return value }(),
		func() CreateInput { value := validCreate; value.Assignee.Kind = "bogus"; return value }(),
		func() CreateInput { value := validCreate; value.AttachmentIDs = []uuid.UUID{uuid.Nil}; return value }(),
		func() CreateInput {
			value := validCreate
			file := uuid.Must(uuid.NewV7())
			value.AttachmentIDs = []uuid.UUID{file, file}
			return value
		}(),
		func() CreateInput {
			value := validCreate
			for range maxCreateAttachments + 1 {
				value.AttachmentIDs = append(value.AttachmentIDs, uuid.Must(uuid.NewV7()))
			}
			return value
		}(),
	}
	for _, input := range invalidCreates {
		if _, err := normalizeCreateInput(input, callerID); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("normalizeCreateInput(%+v) error = %v, want %v", input, err, ErrInvalidInput)
		}
	}

	agent := ActorRef{Kind: ActorAgent, ID: uuid.Must(uuid.NewV7())}
	member := ActorRef{Kind: ActorUser, ID: uuid.Must(uuid.NewV7())}
	for _, validUpdate := range []UpdateInput{
		{ExpectedRevision: 3, ExecutedBy: &agent},
		{ExpectedRevision: 3, Assignee: &member},
		{ExpectedRevision: 3, Assignee: &member, ExecutedBy: &member},
		{ExpectedRevision: 3, Assignee: &member, ExecutedBy: &agent, ConfirmHandoff: true},
	} {
		if _, err := normalizeUpdateInput(validUpdate); err != nil {
			t.Fatalf("normalizeUpdateInput(%+v) error = %v", validUpdate, err)
		}
	}
	// A revision outside 1..protocol.MaxJSONSafeInteger is not tested here: it
	// is the `revision` binding rule on updateTaskRequest and
	// startTaskExecutionRequest, exercised by the handler package's
	// TestRevisionBindingRejectsRevisionsOutsideTheRange.
	otherMember := ActorRef{Kind: ActorUser, ID: uuid.Must(uuid.NewV7())}
	invalidUpdates := []UpdateInput{
		{ExpectedRevision: 3},
		// The assignee is always a member (ADR 0014).
		{ExpectedRevision: 3, Assignee: &agent},
		// A user executedBy must be the assignee the update lands on.
		{ExpectedRevision: 3, Assignee: &member, ExecutedBy: &otherMember},
		{ExpectedRevision: 3, ExecutedBy: &ActorRef{Kind: "bogus", ID: agent.ID}},
	}
	for _, input := range invalidUpdates {
		if _, err := normalizeUpdateInput(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("normalizeUpdateInput(%+v) error = %v, want %v", input, err, ErrInvalidInput)
		}
	}
}

func TestNormalizeListOptionsValidatesCompleteCollectionFilters(t *testing.T) {
	t.Parallel()
	resolved := true
	options := ListOptions{
		Statuses:   []Status{StatusTodo, StatusBlocked},
		Priorities: []Priority{PriorityHigh, PriorityUrgent},

		AssigneeUserID:       uuidPointer(uuid.Must(uuid.NewV7())),
		ExecutedByAgentID:    uuidPointer(uuid.Must(uuid.NewV7())),
		DependenciesResolved: &resolved,
	}
	if _, err := normalizeListOptions(options); err != nil {
		t.Fatalf("normalizeListOptions() error = %v", err)
	}
	invalid := []ListOptions{
		{Statuses: []Status{"unknown"}},
		{Priorities: []Priority{"critical"}},
		{AssigneeUserID: uuidPointer(uuid.Nil)},
		{ExecutedByAgentID: uuidPointer(uuid.Nil)},
	}
	for _, input := range invalid {
		if _, err := normalizeListOptions(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("normalizeListOptions(%+v) error = %v, want %v", input, err, ErrInvalidInput)
		}
	}
}

func validCreateInput() CreateInput {
	agent := ActorRef{Kind: ActorAgent, ID: uuid.Must(uuid.NewV7())}
	return CreateInput{
		ProjectID: uuid.New(),
		Key:       "fix_release",

		MilestoneType: MilestoneTypeProduct,
		Title:         "Fix release",
		Description:   "Inspect tests",
		Status:        StatusTodo,
		Priority:      PriorityHigh,
		Assignee:      ActorRef{Kind: ActorUser, ID: callerID},
		ExecutedBy:    &agent,
	}
}

func validResponse() Response {
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	agentID := uuid.Must(uuid.NewV7())
	return Response{
		ID: uuid.Must(uuid.NewV7()), CompanyID: uuid.Must(uuid.NewV7()),
		MilestoneType:  MilestoneTypeProduct,
		ConversationID: uuid.Must(uuid.NewV7()), Number: 7, Key: "fix_release", Title: "Fix release",
		Description: "Inspect tests", Status: StatusTodo, Priority: PriorityHigh,
		Assignee:   ActorRef{Kind: ActorUser, ID: callerID},
		ExecutedBy: ActorRef{Kind: ActorAgent, ID: agentID},
		Creator:    ActorRef{Kind: ActorUser, ID: uuid.Must(uuid.NewV7())},
		CreatedAt:  now, UpdatedAt: now, Revision: 1,
	}
}

func TestTaskFailureStatusesAreReadableButNotClientMutable(t *testing.T) {
	t.Parallel()

	for _, status := range []Status{StatusFailed, StatusError} {
		if !validStatus(status) {
			t.Fatalf("validStatus(%q) = false", status)
		}
		if validMutationStatus(status) {
			t.Fatalf("validMutationStatus(%q) = true", status)
		}
		input := validCreateInput()
		input.Status = status
		if _, err := normalizeCreateInput(input, callerID); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("normalizeCreateInput(status=%q) error = %v, want %v", status, err, ErrInvalidInput)
		}
	}
}

func uuidPointer(value uuid.UUID) *uuid.UUID { return &value }
