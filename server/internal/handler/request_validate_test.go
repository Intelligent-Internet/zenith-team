package handler

import (
	"encoding/json"
	"errors"
	"go/ast"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/protocol"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
)

// TestRequestStructValidationDetails pins the rendered per-field messages for
// representative request structs. Substring assertions keep the messages
// evolvable without breaking on rephrasing.
func TestRequestStructValidationDetails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		request  any
		contains []string
	}{
		{
			name:    "agent create missing required fields",
			request: &createAgentRequest{Title: "T"},
			contains: []string{
				"name is required",
				"provider is required",
				"model is required",
			},
		},
		{
			name: "resource update version out of range",
			request: &updateCompanyResourceRequest{
				ExpectedRevision: 0,
				Label:            optional[string]{set: true, value: "repo"},
			},
			contains: []string{"expectedRevision must be a revision between 1 and 9007199254740991"},
		},
		{
			name: "resource update version past the JSON-safe ceiling",
			request: &updateCompanyResourceRequest{
				ExpectedRevision: protocol.MaxJSONSafeInteger + 1,
				Label:            optional[string]{set: true, value: "repo"},
			},
			contains: []string{"expectedRevision must be a revision between 1 and 9007199254740991"},
		},
		{
			name:     "resource update empty patch",
			request:  &updateCompanyResourceRequest{ExpectedRevision: 3},
			contains: []string{"label is required when its alternative is absent"},
		},
		{
			name: "task actor kind outside enum",
			request: &taskActorRequest{
				Kind: "robot",
				ID:   "01980000-0000-7000-8000-000000000002",
			},
			contains: []string{"kind must be one of user, agent"},
		},
		{
			name: "desktop oauth completion with code and error",
			request: &desktopOAuthCompleteRequest{
				FlowID: "01980000-0000-7000-8000-000000000000", RedirectURI: "http://127.0.0.1/cb",
				State: "s", Verifier: "v", Code: "c", ProviderError: "access_denied",
			},
			contains: []string{"code must not be provided together with its alternative"},
		},
		{
			name: "conversation state update without changes",
			request: &updateConversationStateRequest{
				ExpectedRevision: optional[int64]{set: true, value: 1},
			},
			contains: []string{"pinned is required"},
		},
		{
			name:     "refresh client outside enum",
			request:  &refreshRequest{Client: "cli"},
			contains: []string{"client must be one of web, desktop"},
		},
		{
			name:     "run usage batch missing facts",
			request:  &runUsageBatchRequest{},
			contains: []string{"usage is required"},
		},
		{
			name: "run usage fact missing required counters",
			request: &runUsageBatchRequest{Usage: []runUsagePayload{{
				Provider: "codex", Model: "gpt-5", RawJSON: json.RawMessage(`{}`),
			}}},
			contains: []string{
				"inputTokens is required",
				"outputTokens is required",
				"cacheReadTokens is required",
				"cacheWriteTokens is required",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := binding.Validator.ValidateStruct(test.request)
			if err == nil {
				t.Fatal("expected validation failure")
			}
			detail := util.RenderValidationErrors(err, maxValidationDetailBytes)
			for _, fragment := range test.contains {
				if !strings.Contains(detail, fragment) {
					t.Fatalf("detail = %q, want fragment %q", detail, fragment)
				}
			}
		})
	}
}

func TestConnectorAndPluginRequestBindingsAcceptValidNestedItems(t *testing.T) {
	t.Parallel()

	connector := &connectorDiscoveryRequest{
		SchemaVersion:  1,
		Generation:     "generation",
		ManifestDigest: strings.Repeat("a", 64),
		ObservedAt:     "observed",
		Items: []connectorObservationRequest{{
			Source: "codex", NativeID: strings.Repeat("i", 240),
			Name: strings.Repeat("n", 160), Status: "available", Action: "none",
		}},
	}
	plugin := &pluginDiscoveryRequest{
		SchemaVersion:  1,
		Generation:     "generation",
		ManifestDigest: strings.Repeat("a", 64),
		ObservedAt:     "observed",
		Items: []pluginObservationRequest{{
			NativeID: strings.Repeat("i", 240), Name: strings.Repeat("n", 160),
			Status: "available", Action: "none", ComponentCounts: &pluginCountsRequest{},
			ConnectorIdentities: []pluginConnectorIdentityRequest{{
				Source: "codex", NativeID: strings.Repeat("i", 240),
			}},
		}},
	}
	resolve := &connectorResolveRequest{
		SchemaVersion: 1,
		Identities: []connectorIdentityRequest{{
			Source: "codex", NativeID: strings.Repeat("i", 240),
		}},
	}
	for name, request := range map[string]any{
		"connector discovery": connector,
		"connector resolve":   resolve,
		"plugin discovery":    plugin,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := binding.Validator.ValidateStruct(request); err != nil {
				t.Fatalf("ValidateStruct() error = %v", err)
			}
		})
	}
}

// TestBindRequestWritesAggregatedValidationDetail exercises the full HTTP
// path: strict decode, struct-tag validation, and the 422 contract with a
// human-readable aggregated detail.
func TestBindRequestWritesAggregatedValidationDetail(t *testing.T) {
	t.Parallel()

	router := gin.New()
	router.POST("/agents", func(c *gin.Context) {
		if _, err := bindRequest[createAgentRequest](c, 1<<20); err != nil {
			writeRequestError(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	body := `{"title":"CEO","reportsToAgentId":"01980000-0000-7000-8000-000000000000",` +
		`"runtimeId":"also-not-a-uuid","model":"\"gpt\"","thinkingLevel":"\"high\""}`
	request := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"status = %d, want %d (body %s)",
			response.Code,
			http.StatusUnprocessableEntity,
			response.Body.String(),
		)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["code"] != "validation_failed" {
		t.Fatalf("code = %q", payload["code"])
	}
	detail, _ := payload["detail"].(string)
	if !strings.Contains(detail, "runtimeId must be a canonical UUID") ||
		!strings.Contains(detail, "name is required") {
		t.Fatalf("detail = %q", detail)
	}
}

// TestBindJSONKeepsMalformedBodyContract confirms the 400 invalid_request
// shape contract is untouched by the validation layer.
func TestBindJSONKeepsMalformedBodyContract(t *testing.T) {
	t.Parallel()

	router := gin.New()
	router.POST("/agents", func(c *gin.Context) {
		if _, err := bindRequest[createAgentRequest](c, 1<<20); err != nil {
			writeRequestError(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	request := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(`{"id":`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["code"] != "invalid_request" || payload["detail"] != "request body is invalid" {
		t.Fatalf("body = %#v", payload)
	}
}

// A 422 body is rendered from the validator's own error type. Anything else
// reaching the writer is a programming error, not something the caller can fix
// by sending different bytes, so it must surface as a plain 500 rather than a
// validation failure with an empty or invented detail.
func TestWriteRequestErrorRejectsNonValidationErrors(t *testing.T) {
	t.Parallel()

	err := errors.New("not a field failure")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	util.SetRequestID(c, "misuse-request-id")
	writeRequestError(c, err)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"internal_error"`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

// Field rules reach the wire only through gin's validator, which reads the
// `binding` tag. A `validate` tag is the shape this package used before the
// migration and it is the dangerous mistake to repeat: it compiles, it reads as
// enforced, and it does nothing at all.
//
// Struct tags are checked in the source rather than through a request because a
// rule that never runs produces no failure to assert on.
func TestRequestDTOsTagRulesWhereGinReadsThem(t *testing.T) {
	t.Parallel()

	var stale []string
	for name, file := range parseProductionHandlerFiles(t) {
		ast.Inspect(file, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok || field.Tag == nil {
				return true
			}
			tag, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				return true
			}
			if _, tagged := reflect.StructTag(tag).Lookup("validate"); !tagged {
				return true
			}
			label := "<embedded>"
			if len(field.Names) > 0 {
				label = field.Names[0].Name
			}
			stale = append(stale, name+": "+label)
			return true
		})
	}
	sort.Strings(stale)
	if len(stale) != 0 {
		t.Errorf(
			"fields tagged `validate:` = %v; gin's validator reads `binding:`, so these rules never run",
			stale,
		)
	}
}

// One rule, spelt one way. An optimistic-concurrency field is the same
// contract on every route, so it must carry the same bound on every route —
// and the bound is what stops a caller sending 0 and matching a row nobody
// has written yet.
//
// Two int64 fields reached this test with no tag at all and one with a
// redundant `required` in front of `min=1`. Both read as fine at a glance,
// which is why it is checked mechanically rather than by eye.
func TestRevisionFieldsShareOneBinding(t *testing.T) {
	t.Parallel()

	// Two shapes, one rule. A plain int64 carries the bound in its tag as the
	// named `revision` rule rather than as a copy of the number. An
	// optional[int64] now carries the same rule: optionalValue unwraps the
	// field before the validator reads it, so the tag applies to the int64
	// and the bound has stopped being the service's to restate.
	//
	// The wrapped shape says two more things, and both are about the key
	// rather than the value. `required` is the presence rule, enforced by
	// bindRequest's walk; the leading `omitnil` is what keeps the validator
	// from answering presence too, by stopping at the typed nil an absent key
	// unwraps to.
	const boundedRule = revisionRule
	const wrappedRule = "omitnil,required," + revisionRule

	// One field takes a different bound rather than a different spelling: a
	// Conversation's user state is created on first write, so revision 0 names
	// a row that does not exist yet and the rule there is `>= 0`, which the
	// OpenAPI schema states as minimum 0. There is no tag for it, so
	// conversation.normalizeUpdateUserStateInput stays its sole enforcement.
	const zeroAllowedRule = "omitnil,required"
	zeroAllowed := map[string]bool{
		"updateConversationStateRequest": true,
		"updateDiscussionStateRequest":   true,
		"updateDiscussionThreadRequest":  true,
	}

	bounded, wrapped := revisionRequestTags(t)
	var wrong []string
	for name, got := range bounded {
		if got != boundedRule {
			wrong = append(wrong, name+" = "+strconv.Quote(got)+", want "+strconv.Quote(boundedRule))
		}
	}
	for name, got := range wrapped {
		want := wrappedRule
		if zeroAllowed[name] {
			want = zeroAllowedRule
		}
		// Wiki pages exist at revision zero until their first publication.
		switch name {
		case "wikiPageRevisionRequest", "wikiPublishRequest", "wikiRestoreRevisionRequest":
			want = "omitnil,required,min=0,max=9007199254740991"
		}
		if got != want {
			wrong = append(wrong, name+" = "+strconv.Quote(got)+", want "+strconv.Quote(want))
		}
	}
	for name := range zeroAllowed {
		if _, declared := wrapped[name]; !declared {
			wrong = append(wrong, name+" is excepted here and declares no optional[int64] revision")
		}
	}
	sort.Strings(wrong)
	if len(wrong) != 0 {
		t.Errorf("revision bindings:\n  %s", strings.Join(wrong, "\n  "))
	}
	// Without these the test passes by matching nothing, which is how it spent
	// the `Expected*Version` era green: every field had been renamed to
	// ExpectedRevision and the filter quietly stopped selecting any of them.
	if len(bounded) == 0 {
		t.Error("matched no plain int64 revision field; the filter has drifted off the DTOs")
	}
	if len(wrapped) == 0 {
		t.Error("matched no optional[int64] revision field; the filter has drifted off the DTOs")
	}
}

// revisionRequestTags reads every ExpectedRevision field declared in the
// package's production sources and returns, keyed by the type declaring it,
// the binding tag it carries — split by the two shapes the field takes, a
// plain int64 and an optional[int64]. Both carry the bound in the tag; the
// wrapped one needs two more rules to say so.
func revisionRequestTags(t *testing.T) (bounded, wrapped map[string]string) {
	t.Helper()

	bounded, wrapped = map[string]string{}, map[string]string{}
	for _, file := range parseProductionHandlerFiles(t) {
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok {
				return true
			}
			structure, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range structure.Fields.List {
				if field.Tag == nil || len(field.Names) == 0 ||
					field.Names[0].Name != "ExpectedRevision" {
					continue
				}
				tag, err := strconv.Unquote(field.Tag.Value)
				if err != nil {
					continue
				}
				rule := reflect.StructTag(tag).Get("binding")
				switch typed := field.Type.(type) {
				case *ast.Ident:
					if typed.Name == "int64" {
						bounded[spec.Name.Name] = rule
					}
				case *ast.IndexExpr:
					// optional[int64]
					wrapped[spec.Name.Name] = rule
				}
			}
			return true
		})
	}
	return bounded, wrapped
}

// requestOptionalField is one optional[T] field declared by a request DTO.
type requestOptionalField struct {
	where    string // file: type.Field
	argument string // the T in optional[T]
	binding  string
}

// requestOptionalFields reads every optional[T] field out of the package's
// production sources. Both scanners below need the same three facts about
// each: where it is, what it wraps, and what its tag says.
func requestOptionalFields(t *testing.T) []requestOptionalField {
	t.Helper()

	var found []requestOptionalField
	for name, file := range parseProductionHandlerFiles(t) {
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok {
				return true
			}
			structure, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range structure.Fields.List {
				index, isGeneric := field.Type.(*ast.IndexExpr)
				if !isGeneric {
					continue
				}
				if wrapper, ok := index.X.(*ast.Ident); !ok || wrapper.Name != "optional" {
					continue
				}
				label := spec.Name.Name
				if len(field.Names) > 0 {
					label += "." + field.Names[0].Name
				}
				argument, ok := index.Index.(*ast.Ident)
				if !ok {
					t.Fatalf("%s: %s wraps a type this scan cannot name", name, label)
				}
				rule := ""
				if field.Tag != nil {
					if tag, err := strconv.Unquote(field.Tag.Value); err == nil {
						rule = reflect.StructTag(tag).Get("binding")
					}
				}
				found = append(found, requestOptionalField{
					where: name + ": " + label, argument: argument.Name, binding: rule,
				})
			}
			return true
		})
	}
	// An empty scan would turn both scanners green at once.
	if len(found) == 0 {
		t.Fatal("matched no optional[T] request field; the scan has drifted off the DTOs")
	}
	return found
}

// The validator reads an optional[T] only through the unwrapper registered for
// that exact instantiation. A new one — optional[float64], say — compiles,
// binds, and then quietly means something else than its tag says: a value rule
// on it panics at the first request that carries it, and `required` is
// answered from the wrapper's zero struct rather than from the key.
//
// Nothing in the type system connects the list to the DTOs, so this does.
func TestEveryRequestOptionalIsRegistered(t *testing.T) {
	t.Parallel()

	registered := map[string]bool{}
	for _, entry := range requestOptionalTypes {
		name := reflect.TypeOf(entry.zero).String()
		open := strings.IndexByte(name, '[')
		if open < 0 || !strings.HasSuffix(name, "]") {
			t.Fatalf("registered optional %q is not an instantiation", name)
		}
		registered[name[open+1:len(name)-1]] = true
	}

	declared := map[string]bool{}
	var missing []string
	for _, field := range requestOptionalFields(t) {
		declared[field.argument] = true
		if !registered[field.argument] {
			missing = append(missing, field.where+" wraps "+field.argument)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf(
			"optional instantiations the validator cannot read through:\n  %s",
			strings.Join(missing, "\n  "),
		)
	}
	for argument := range registered {
		if !declared[argument] {
			t.Errorf("optional[%s] is registered and no request body declares it", argument)
		}
	}
}

// `required` on an optional[T] is a statement about the key, and bindRequest's
// walk is what answers it — see bindJSONBody for why it, and not the
// validator.
//
// The leading `omitnil` is what keeps that true. Without it the validator
// fails the typed nil an absent key unwraps to, and one rule ends up enforced
// in two places with two different ideas of which shapes it reaches: the walk
// descends into every slice, the validator only into the ones a `dive` tag
// opens.
func TestRequiredOptionalsLeadWithOmitnil(t *testing.T) {
	t.Parallel()

	var wrong []string
	for _, field := range requestOptionalFields(t) {
		if !declaresRequired(field.binding) {
			continue
		}
		first, _, _ := strings.Cut(field.binding, ",")
		if strings.TrimSpace(first) != "omitnil" {
			wrong = append(wrong, field.where+" = "+strconv.Quote(field.binding))
		}
	}
	sort.Strings(wrong)
	if len(wrong) != 0 {
		t.Errorf(
			"optional fields declaring `required` behind no leading `omitnil`:\n  %s",
			strings.Join(wrong, "\n  "),
		)
	}
}

// The `revision` rule is the only thing between a caller and a revision the
// database would compare against happily: the domain guards that used to
// restate it — auth.normalizeProfileInput, agent.normalizeRevisionInput,
// task.normalizeUpdateInput and normalizeStartExecutionInput,
// approval.normalizeQuestionAnswerInput and normalizeApprovalDecisionInput,
// company.normalizeRevisionInput — are gone, so this is where their coverage
// lives now.
//
// It runs the rule at both ends of the range on every request body carrying
// it, and cross-checks the list against the sources so a new DTO cannot slip
// in uncovered.
func TestRevisionBindingRejectsRevisionsOutsideTheRange(t *testing.T) {
	t.Parallel()

	requests := map[string]any{
		"submitHuddleNotesRequest":       &submitHuddleNotesRequest{ProjectID: "00000000-0000-0000-0000-000000000001", Title: "Do work", MilestoneType: "product", Confirm: true},
		"editHuddleNotesRecapRequest":    &editHuddleNotesRecapRequest{},
		"setHuddleNotesRequest":          &setHuddleNotesRequest{Enabled: new(bool), RequestKey: "00000000-0000-0000-0000-000000000001"},
		"huddleNotesPolicyRequest":       &huddleNotesPolicyRequest{Enabled: new(bool), RetentionDays: 30},
		"taskScheduleRequest":            &taskScheduleRequest{},
		"taskOccurrenceRequest":          &taskOccurrenceRequest{OccurrenceAt: time.Now()},
		"taskAssignmentRequest":          &taskAssignmentRequest{Accept: new(bool)},
		"calendarResponseRequest":        &calendarResponseRequest{},
		"calendarUpdateRequest":          &calendarUpdateRequest{},
		"calendarOccurrenceRequest":      &calendarOccurrenceRequest{},
		"invitationMutationRequest":      &invitationMutationRequest{},
		"membershipMutationRequest":      &membershipMutationRequest{},
		"changeCompanyMemberRoleRequest": &changeCompanyMemberRoleRequest{Role: "admin"},
		"resetShareLinkRequest":          &resetShareLinkRequest{},
		"deleteCompanyRequest":           &deleteCompanyRequest{},
		"updateAgentRequest":             &updateAgentRequest{},
		"agentVersionRequest":            &agentVersionRequest{},
		"skillUpdateRequest":             &skillUpdateRequest{Enabled: new(bool)},
		"skillReplacementRequest":        &skillReplacementRequest{},
		"approvalDecisionRequest":        &approvalDecisionRequest{},
		"questionAnswerRequest":          &questionAnswerRequest{},
		"revisionRequest":                &revisionRequest{},
		"renameConversationRequest":      &renameConversationRequest{Title: "Review"},
		"respondHuddleInvitationRequest": &respondHuddleInvitationRequest{},
		"setHuddleAIRequest": &setHuddleAIRequest{
			Enabled:    new(bool),
			RequestKey: "00000000-0000-0000-0000-000000000001",
		},
		"updateHuddleProposalRequest":  &updateHuddleProposalRequest{Action: "reject"},
		"submitHuddleProposalRequest":  &submitHuddleProposalRequest{Confirm: true},
		"updateCompanyResourceRequest": &updateCompanyResourceRequest{},
		"deleteCompanyResourceRequest": &deleteCompanyResourceRequest{},
		"inboxMutationRequest":         &inboxMutationRequest{},
		"updateProfileRequest":         &updateProfileRequest{},
		"updateTaskRequest":            &updateTaskRequest{},
		"finalizeTaskResultsRequest":   &finalizeTaskResultsRequest{OutputIDs: []uuid.UUID{uuid.New()}},
		"startTaskExecutionRequest":    &startTaskExecutionRequest{},
		"archiveProjectRequest":        &archiveProjectRequest{},
		"updateProjectRequest":         &updateProjectRequest{},
		"setProjectGrantRequest":       &setProjectGrantRequest{Permissions: []string{"project.read"}},
		"projectAccessRevisionRequest": &projectAccessRevisionRequest{},
		"replaceProjectFilesRequest":   &replaceProjectFilesRequest{DocumentIDs: []uuid.UUID{}},
		"updateGroupRequest": &updateGroupRequest{
			Name: optional[string]{set: true, value: "Finance"},
		},
		"groupRevisionRequest":        &groupRevisionRequest{},
		"updateMailboxDraftRequest":   &updateMailboxDraftRequest{},
		"mailboxDraftRevisionRequest": &mailboxDraftRevisionRequest{},
		"mailboxDraftVersionRequest":  &mailboxDraftVersionRequest{},
		"updateScheduleRequest":       &updateScheduleRequest{},
		"scheduleLifecycleRequest":    &scheduleLifecycleRequest{},
		"updateProactiveModeRequest":  &updateProactiveModeRequest{},
		"taskMergeManualClaimRequest": &taskMergeManualClaimRequest{},
		"publicationUnpublishRequest": &publicationUnpublishRequest{},
		"publicationSubdomainRequest": &publicationSubdomainRequest{},
		"artifactGrantRequest":        &artifactGrantRequest{Permission: "view"},
		"wikiRenameCollectionRequest": &wikiRenameCollectionRequest{Name: "Handbook"},
		"wikiRevisionRequest":         &wikiRevisionRequest{},
		"changeGroupMembersRequest": &changeGroupMembersRequest{
			AddUserIDs: []uuid.UUID{}, RemoveUserIDs: []uuid.UUID{},
		},
	}
	// The wrapped shape carries the same rule now that optionalValue unwraps
	// the field for the validator. These three are where the bound used to be
	// a service guard and nothing else.
	wrappedRequests := map[string]any{
		"updateConversationModelSettingsRequest": &updateConversationModelSettingsRequest{},
		"updateChannelRequest":                   &updateChannelRequest{},
		"editDiscussionRequest":                  &editDiscussionRequest{},
		"channelRevisionRequest":                 &channelRevisionRequest{},
		"updateCompanyProfileRequest":            &updateCompanyProfileRequest{},
		"reorderRunQueueItemRequest":             &reorderRunQueueItemRequest{},
		"deleteRunQueueItemRequest":              &deleteRunQueueItemRequest{},
	}

	bounded, wrapped := revisionRequestTags(t)
	for name := range bounded {
		if _, listed := requests[name]; !listed {
			t.Errorf("%s carries binding:%q and is not exercised here", name, revisionRule)
		}
	}
	for name := range requests {
		if _, declared := bounded[name]; !declared {
			t.Errorf("%s is exercised here and no longer declares a plain int64 revision", name)
		}
	}
	for name, rule := range wrapped {
		_, listed := wrappedRequests[name]
		if carries := strings.Contains(rule, revisionRule); carries && !listed {
			t.Errorf("%s carries binding:%q and is not exercised here", name, revisionRule)
		} else if !carries && listed {
			t.Errorf("%s is exercised here and no longer declares binding:%q", name, revisionRule)
		}
	}
	for name := range wrappedRequests {
		if _, declared := wrapped[name]; !declared {
			t.Errorf("%s is exercised here and no longer declares an optional[int64] revision", name)
		}
	}

	// Both shapes answer the same way; only the assignment differs, because a
	// wrapped field must be set as supplied for the value rules to run at all.
	setPlain := func(field reflect.Value, revision int64) { field.SetInt(revision) }
	setWrapped := func(field reflect.Value, revision int64) {
		field.Set(reflect.ValueOf(optional[int64]{set: true, value: revision}))
	}
	for _, shape := range []struct {
		requests map[string]any
		set      func(reflect.Value, int64)
	}{{requests, setPlain}, {wrappedRequests, setWrapped}} {
		for name, request := range shape.requests {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				field := reflect.ValueOf(request).Elem().FieldByName("ExpectedRevision")
				for _, revision := range []int64{
					0, -1, math.MinInt64,
					protocol.MaxJSONSafeInteger + 1, math.MaxInt64,
				} {
					shape.set(field, revision)
					if !revisionRejected(t, request) {
						t.Errorf("expectedRevision %d accepted", revision)
					}
				}
				for _, revision := range []int64{1, 2, protocol.MaxJSONSafeInteger} {
					shape.set(field, revision)
					if revisionRejected(t, request) {
						t.Errorf("expectedRevision %d rejected", revision)
					}
				}
			})
		}
	}
}

// revisionRejected reports whether the validator faulted expectedRevision on
// request. Other fields of the same request may fault too — these are zero
// values — and are deliberately ignored: the question is only whether the
// revision rule ran and held.
func revisionRejected(t *testing.T, request any) bool {
	t.Helper()

	err := binding.Validator.ValidateStruct(request)
	if err == nil {
		return false
	}
	var validationErrors validator.ValidationErrors
	if !errors.As(err, &validationErrors) {
		t.Fatalf("ValidateStruct() error = %v (%T), want validator.ValidationErrors", err, err)
	}
	for _, fieldError := range validationErrors {
		if fieldError.Field() == "expectedRevision" && fieldError.ActualTag() == revisionRule {
			return true
		}
	}
	return false
}

// The `maxConcurrentRuns` rule is the agent capacity knob's only guard — the
// column carries no CHECK and the service restates nothing — so both ends of
// the 1..10 range are exercised here, boundary values included.
func TestMaxConcurrentRunsBindingHoldsItsBounds(t *testing.T) {
	t.Parallel()

	request := func(runs int32) *updateAgentRequest {
		return &updateAgentRequest{
			ExpectedRevision:  1,
			MaxConcurrentRuns: optional[int32]{set: true, value: runs},
		}
	}
	for _, runs := range []int32{0, -1, 11, 100, math.MinInt32, math.MaxInt32} {
		if !concurrentRunsRejected(t, request(runs)) {
			t.Errorf("maxConcurrentRuns %d accepted", runs)
		}
	}
	for _, runs := range []int32{1, 3, 10} {
		if concurrentRunsRejected(t, request(runs)) {
			t.Errorf("maxConcurrentRuns %d rejected", runs)
		}
	}
	// Absent means "leave the cap alone"; the leading omitnil must keep the
	// rule off the typed nil an absent key unwraps to.
	if concurrentRunsRejected(t, &updateAgentRequest{
		ExpectedRevision: 1, Title: json.RawMessage(`"t"`),
	}) {
		t.Error("absent maxConcurrentRuns tripped the rule")
	}
}

// concurrentRunsRejected reports whether the validator faulted
// maxConcurrentRuns on request under its named rule. Other zero-valued fields
// may fault too and are deliberately ignored.
func concurrentRunsRejected(t *testing.T, request *updateAgentRequest) bool {
	t.Helper()

	err := binding.Validator.ValidateStruct(request)
	if err == nil {
		return false
	}
	var validationErrors validator.ValidationErrors
	if !errors.As(err, &validationErrors) {
		t.Fatalf("ValidateStruct() error = %v (%T), want validator.ValidationErrors", err, err)
	}
	for _, fieldError := range validationErrors {
		if fieldError.Field() == "maxConcurrentRuns" &&
			fieldError.ActualTag() == maxConcurrentRunsRule {
			return true
		}
	}
	return false
}

func TestCrossFieldRulesHoldThroughBinding(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		request any
		valid   bool
	}{
		"resource patch with nothing set":  {&updateCompanyResourceRequest{ExpectedRevision: 1}, false},
		"resource patch with label":        {&updateCompanyResourceRequest{ExpectedRevision: 1, Label: optional[string]{set: true, value: "x"}}, true},
		"resource patch with position":     {&updateCompanyResourceRequest{ExpectedRevision: 1, Position: optional[int32]{set: true}}, true},
		"conversation patch with nothing":  {&updateConversationStateRequest{ExpectedRevision: optional[int64]{set: true, value: 1}}, false},
		"conversation patch with pinned":   {&updateConversationStateRequest{ExpectedRevision: optional[int64]{set: true, value: 1}, Pinned: optional[bool]{set: true, value: true}}, true},
		"conversation patch with archived": {&updateConversationStateRequest{ExpectedRevision: optional[int64]{set: true, value: 1}, Archived: optional[bool]{set: true, value: true}}, true},
		"agent patch with nothing":         {&updateAgentRequest{ExpectedRevision: 1}, false},
		"agent patch with title only":      {&updateAgentRequest{ExpectedRevision: 1, Title: json.RawMessage(`"t"`)}, true},
		"agent patch with last field only": {&updateAgentRequest{ExpectedRevision: 1, ThinkingLevel: json.RawMessage(`"high"`)}, true},
		"agent patch with capacity only":   {&updateAgentRequest{ExpectedRevision: 1, MaxConcurrentRuns: optional[int32]{set: true, value: 3}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := binding.Validator.ValidateStruct(testCase.request)
			if testCase.valid && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !testCase.valid && err == nil {
				t.Fatal("expected a cross-field failure")
			}
		})
	}
}

// Exactly-one needs both halves of the tag pair: required_without catches
// neither, excluded_with catches both. Dropping either one fails open in a
// direction no single-sided test would notice.
func TestDesktopOAuthAcceptsExactlyOneOutcome(t *testing.T) {
	t.Parallel()

	base := func() desktopOAuthCompleteRequest {
		return desktopOAuthCompleteRequest{
			FlowID:      "01980000-0000-7000-8000-000000000000",
			RedirectURI: "http://127.0.0.1/cb",
			State:       "s",
			Verifier:    "v",
		}
	}
	neither := base()
	if err := binding.Validator.ValidateStruct(&neither); err == nil {
		t.Error("neither code nor error: expected a failure")
	}
	both := base()
	both.Code, both.ProviderError = "c", "access_denied"
	if err := binding.Validator.ValidateStruct(&both); err == nil {
		t.Error("both code and error: expected a failure")
	}
	codeOnly := base()
	codeOnly.Code = "c"
	if err := binding.Validator.ValidateStruct(&codeOnly); err != nil {
		t.Errorf("code only: %v", err)
	}
	errorOnly := base()
	errorOnly.ProviderError = "access_denied"
	if err := binding.Validator.ValidateStruct(&errorOnly); err != nil {
		t.Errorf("error only: %v", err)
	}
}

func TestPageCreationReviewAcceptsDocumentSizedGroups(t *testing.T) {
	t.Parallel()
	for _, count := range []int{41, 10000, 10001} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			t.Parallel()
			indexes := make([]int, count)
			for i := range indexes {
				indexes[i] = i
			}
			input := canvas.PageChangeReviewInput{
				RequestKey:  uuid.New(),
				Fingerprint: strings.Repeat("a", 64),
				Action:      "apply",
				Indexes:     indexes,
			}
			err := binding.Validator.ValidateStruct(input)
			if (err == nil) != (count <= 10000) {
				t.Fatalf("review %d indexes = %v", count, err)
			}
		})
	}
}
