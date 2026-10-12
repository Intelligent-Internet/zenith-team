package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Project access (ADR 0017) lives in four views and one function
// (migration 000086). These two scans keep every read that acts for a person
// on them: a statement that binds a person and reads a guarded table must
// name one of the access views, and Go code must not reach the Project-scoped
// tables with hand-written SQL. A statement that does neither is listed below
// with the reason, until the step that sweeps it lands. The step names follow
// the implementation plan: B3 Task chats, approvals, questions, deliverables
// and mail (plan rollout step 5, the rest of sweep A); B4 files (rollout 6);
// B5 Inbox and Activity (rollout 7). Step B8 (rollout 10) moved the Company
// Plan's hand-written SQL into sqlc queries that join the views, so no entry
// names it. An entry that stops applying fails the test, so the lists only
// shrink.

// guardedTables are the tables whose rows belong to a Project, directly or
// through a Task or a chat.
var guardedTables = []string{
	"projects", "tasks", "task_dependencies", "project_files", "attachments",
	"conversation_items", "conversations", "inbox_items", "activity_events",
	"task_report_events", "message_attachments",
}

// actorBinding finds every parameter that names a person, in all three sqlc
// spellings: sqlc.arg(x), sqlc.narg(x) and @x. A statement binding one of
// them reads or writes on behalf of that person, unless the parameter only
// stores who did something (storedPersonParams).
var actorBinding = regexp.MustCompile(`(?:sqlc\.n?arg\(\s*'?|@)(\w*user_id|actor_roles)\b`)

// positionalActorBinding finds a person bound as a positional parameter: a
// *user_id column compared with $N, on either side. sqlc gives such a
// parameter no name, so actorBinding cannot see it; without this, writing
// $1 would slip a person-bound read past the scan.
var positionalActorBinding = regexp.MustCompile(
	`\b(\w*user_id)\s*=\s*\$\d+\b|\$\d+\s*=\s*(?:\w+\.)?(\w*user_id)\b`,
)

// storedPersonParams are person parameters a statement only stores or
// matches as a recorded fact (who created, uploaded, decided or authorized
// something); they do not make the statement a read for that person.
var storedPersonParams = map[string]bool{
	"assignee_user_id": true, "authorized_by_user_id": true, "automation_user_id": true,
	"created_by_user_id": true, "creator_user_id": true, "decided_by_user_id": true,
	"execution_authorized_by_user_id": true, "initiated_by_user_id": true,
	"requested_by_user_id": true, "revoked_by_user_id": true, "uploaded_by_user_id": true,
}

// accessViews are the relations that state the Project access rule, plus
// project_permission_held, the same rule as a write fence for one person,
// the delegate rule (D31): delegate_permission_held for a Run or the voice
// assistant, and run_permission_held for one Run from its own row, and
// project_access_paths, every stored path (own grant or Group) by which a
// person holds a permission (000101). The Group tables themselves are not
// guarded tables: they say who may read, not what a Project holds. The views
// are matched on word boundaries so a column such as project_access_version
// never passes for the view.
var accessViews = regexp.MustCompile(
	`\b(project_access|task_access|conversation_readers|attachment_access|output_access|project_permission_held|` +
		`delegate_permission_held|run_permission_held|project_access_paths)\b`,
)

// Reasons shared by several entries below.
const (
	directChats = "not swept: reads only the caller's own direct chat (kind direct, creator = caller), " +
		"which has no Project; conversation_readers states the same creator rule"
	channels = "not swept: a channel or human direct message follows its members, not a Project; " +
		"step B4's fixture test compares conversation_readers with these"
	ownUploads = "not swept: locks the person's own upload to attach, complete, share or register it; it reads nobody else's file, " +
		"and wherever the file is then listed attachment_access decides who sees it"
	liveRunProof = "not swept: locks a live Run's own chat or Run for its proof; the Run stays live only while its authorizer " +
		"holds project.tasks.execute there (claim fence, revocation sweep), and the caller is checked to be that authorizer (the proof's bearer, or the runtime's owner)"
	mergeAuthority = "not swept: queues, re-addresses or records a merge job under the Proactive Mode owner's or the claimant's " +
		"authority; it returns no Task content, and the claim statements check the daemon member's project.tasks.execute"
	usageTotals = "not swept: usage counts carry no Task content and stay Company-wide (D20; plan: unchanged on purpose)"
	schedules   = "not swept: Schedule chats are creator-private and have no Project (plan: unchanged on purpose)"
)

// unguardedActorQueries are statements that bind a person, read a guarded
// table and name no access view: query name -> why, for now.
var unguardedActorQueries = map[string]string{
	// Direct chats: the caller's own, with no Project.
	"GetDirectConversationByAgent":             directChats,
	"GetDirectConversationByRequestKey":        directChats,
	"GetDirectConversationForCreator":          directChats,
	"GetDirectConversationReplay":              directChats,
	"GetDirectConversationUserState":           directChats,
	"InsertDirectConversation":                 directChats,
	"ListDirectConversationAgentRefsBulk":      directChats,
	"ListDirectConversationLatestMessagesBulk": directChats,
	"ListDirectConversationLatestRunsBulk":     directChats,
	"ListDirectConversationSummaryBasesBulk":   directChats,
	"ListDirectConversationsForCreator":        directChats,
	"UpdateDirectConversationUserState":        directChats,
	"ResolveProactiveModeConversation":         directChats,
	"HasProactiveCompanyPlanApprovalAuthority": directChats,
	"GetInitializationTarget":                  directChats,
	// Channels and human direct messages.
	"ChannelInvocationAuthorized":   channels,
	"CanShareOutputToConversation":  channels,
	"GetDiscussionCreationReceipt":  channels,
	"ListDiscussionAccess":          channels,
	"RefreshDiscussionRootUnread":   channels,
	"RefreshDiscussionThreadUnread": channels,
	"InsertChannelInvocation":       channels,
	"ListChannelAgentActivity": "not swept: lists the items of the viewer's own tagged Runs (their Authorizer) in a " +
		"channel Agent's chat, which has no Project, after the service checks that the viewer sees the channel; " +
		"a tagged Run reads only what its Authorizer reads (D31, Access TECH B11)",
	"ToggleHuddleAI": "not swept: stores who granted the Huddle voice assistant Company context, a caller the service has " +
		"checked is in the call (HuddleChatParticipantActive: connected and an active member of the Huddle chat), and reads " +
		"only the Huddle chat's message head; its Project reads are bounded by the delegate rule for that granter " +
		"(delegate_permission_held), and stop when the granter leaves the call (HuddleContextGrantActive)",
	// Files: the uploader's own upload.
	"AuthorizeAttachmentCompletion":       ownUploads,
	"GetReadyAttachmentForOutputForShare": ownUploads,
	"LinkDiscussionAttachment":            ownUploads,
	"LinkMessageAttachment":               ownUploads,
	"LockProjectUpload":                   ownUploads,
	"LockCompanyAvatarUpload":             ownUploads,
	"RegisterCompanyReferenceDocument":    ownUploads,
	"ReserveRunQueueAttachment":           ownUploads,
	// Run proofs, merge jobs and Task report batches.
	"LockHumanInputOriginConversation": liveRunProof,
	"LockStudioProjectRun": liveRunProof + "; a channel tag's chat (channel_agent, one per channel and Agent) has no Project, " +
		"and its Studio binding is read only by Runs answering into that same channel, the group-chat audience",
	"InsertNextProactiveTaskMergeJob":         mergeAuthority,
	"InsertProactiveTaskMergeJobForRun":       mergeAuthority,
	"LockTaskMergeJobForResult":               mergeAuthority,
	"RefreshNextPendingTaskMergeJobAuthority": mergeAuthority,
	"GetTaskReportConversationTarget":         directChats,
	"LockProactiveTaskReportTarget":           directChats,
	// Never swept: these read no content for the person they bind.
	"CountActiveTaskLaneRunsByAgentForBinder":  "not swept: counts the binder's own Run slots for capacity; returns no Task content",
	"GetActivityInsight":                       usageTotals,
	"GetCompanyUsageTotals":                    usageTotals,
	"ListCompanyUsageAgents":                   usageTotals,
	"ListScheduleFiringsForCreator":            schedules,
	"GetScheduleFiringWithResult":              schedules,
	"UpdateScheduleConversationDefinition":     schedules,
	"LockCalendarConversation":                 "not swept: the creator's own direct chat; calendar is unchanged on purpose",
	"DeleteCompanyProjectGrantsForUser":        "not swept: removes a departing member's grants; it decides access, it reads nothing for them",
	"ListActiveRunsForProjectAuthorizer":       "not swept: the revocation sweep's read of one person's live Runs on a Project they are losing",
	"ListQueuedRunQueueConversationIDsForUser": "not swept: a revocation lists the chats holding the person's own queued items to remove them; it reads no content",
	"LockAccountCompaniesForDeletion":          "not swept: account deletion locks the person's own Companies; it reads no Task content",
	"LockDiscussionsForUserRevocation":         "not swept: membership revocation locks the chats the person is leaving",
	"CloseTaskAssignedInboxItems": "not swept: closes the stale assignment notices of the member a Task moved away from, inside the Task write; " +
		"reads stay hidden for a non-reader, and stageClosedItems sends no frame to a recipient who no longer reads the Project",
}

// rawGuardedSQL are hand-written statements in production Go code that reach
// a Project-scoped table: file:line (from server/) -> why, for now.
var rawGuardedSQL = map[string]string{
	// The usage report window (visibleRunsCTE) joins each Run's chat only for
	// its kind; the reads built on it return token and Run totals, models and
	// Agents, never a Task, Project or chat.
	"internal/usage/service.go:509": usageTotals,
	"internal/usage/service.go:781": "swept: the top Tasks list joins project_access for the caller (ADR 0017); " +
		"its share denominator counts every Task Run's tokens and names no Task",
	"internal/testutil/dbtest/dbtest.go:348": "not swept: the test fixture grants its owners every Project permission",
	"internal/testutil/dbtest/groups.go:90": "not swept: the test fixture grants its Group colleagues' Groups every " +
		"Project, for the EXPLAIN pins",
}

func TestActorQueriesReadGuardedTablesThroughAccessViews(t *testing.T) {
	t.Parallel()

	guarded := make([]*regexp.Regexp, len(guardedTables))
	for i, table := range guardedTables {
		guarded[i] = regexp.MustCompile(`\b(from|join|update)\s+` + table + `\b`)
	}
	queryName := regexp.MustCompile(`(?m)^-- name: (\w+)`)
	files, err := filepath.Glob(filepath.Join("..", "..", "pkg", "db", "queries", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("list query files: %v (%d files)", err, len(files))
	}
	found := map[string]string{}
	guardedThroughViews := map[string]bool{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, match := range queryName.FindAllStringSubmatch(source, -1) {
			name := match[1]
			statement := normalizedQuery(t, source, name)
			if !bindsActor(statement) || !matchesAny(statement, guarded) {
				continue
			}
			if accessViews.MatchString(statement) {
				guardedThroughViews[name] = true
				continue
			}
			found[name] = filepath.Base(file)
		}
	}
	// The scan must recognize the statements already swept, or it proves
	// nothing about the rest.
	for _, name := range []string{
		"GetTaskForActiveMember", "ListTaskProjectionsForActiveMember", "GetTaskWorkspaceAnchor",
		"ListTaskDependencyProjections", "ListCompanyTaskDependencyGraph", "SearchCompany",
		"ResolveTaskReference", "UpdateTask",
		"SetDirectConversationTitle",
		"ListConversationMessages", "ResolveMessageTargetForActiveUser",
		"InsertRequestedUserMessageAfterConversationLock", "ListRunProjections",
		"ListPinnedConversationStatesForActiveMember", "LockMailboxDraftForMember",
		"AuthorizeAttachmentDownload", "AuthorizeRunAttachmentDownload", "ListProjectFiles",
		"ListTaskProjectDocuments", "ListDiscussionAttachments", "ListRunChannelAttachments", "ListMessageAttachments",
		"ListMessageAttachmentsBulk", "ListTaskResultSources", "ListRunQueueItemAttachments", "ListSocialMedia",
		"ListActivity", "CountActivityByLocalDay",
		"ListInboxItems", "GetInboxCounts", "GetInboxItemForUpdate", "MarkAllInboxItemsRead",
		"GetVisibleConversationForInboxRead", "ResolveInboxItemProject", "ListInboxCompaniesForUser",
		"ListPendingTaskReportEvents", "CountPendingTaskReportEvents", "LockPendingTaskReportEvents",
		"GetNextProactiveTaskReportTarget",
		"LockCompanyPlanProjects", "LockCompanyPlanTaskConversations", "LockCompanyPlanTasks",
		"LockCompanyPlanTaskDependencies", "DeleteCompanyPlanTask", "RenameCompanyPlanTaskConversation",
		"CanViewRunWikiReads", "ListWikiPageAgentReads",
		"ListAnalyticsGroups", "GetTaskUsageTaskForReader",
		// The delegate rule (D31): the enqueue fence and the voice reads.
		"RunConversationExecutePermitted", "ListVoiceProjects", "ReadVoiceTasks",
	} {
		if !guardedThroughViews[name] {
			t.Errorf(
				"%s is not recognized as an actor read of a guarded table through an access view",
				name,
			)
		}
	}
	for name, file := range found {
		if _, ok := unguardedActorQueries[name]; !ok {
			t.Errorf("%s (%s) binds a person and reads a Project-scoped table without project_access, "+
				"task_access, conversation_readers, attachment_access or output_access", name, file)
		}
	}
	for name := range unguardedActorQueries {
		if _, ok := found[name]; !ok {
			t.Errorf(
				"%s is allowlisted but no longer reads a guarded table unguarded; remove the entry",
				name,
			)
		}
	}
}

// The detector must see a person in every sqlc spelling and as a positional
// $N compared with a person column, and must not see
// one in a parameter that only records who did something, or a view in a
// column that merely starts with its name.
func TestActorBindingDetection(t *testing.T) {
	t.Parallel()

	for statement, want := range map[string]bool{
		"where m.user_id = sqlc.arg(user_id)::uuid":                true,
		"where m.user_id = sqlc.arg('actor_user_id')::uuid":        true,
		"where m.user_id = sqlc.narg(viewer_user_id)::uuid":        true,
		"where m.user_id = @user_id::uuid":                         true,
		"where m.role = any(@actor_roles::text[])":                 true,
		"values (sqlc.arg(created_by_user_id)::uuid)":              false,
		"values (@uploaded_by_user_id::uuid, @assignee_user_id)":   false,
		"where tasks.company_id = sqlc.arg(company_id)::uuid":      false,
		"where labels @> sqlc.arg(labels)::text[] and user_id = 1": false,
		"where i.recipient_user_id=$1":                             true,
		"where $2 = m.user_id":                                     true,
		"and created_by_user_id=$3 for update":                     false,
		"where company_id = $1 and id = $2":                        false,
	} {
		if got := bindsActor(statement); got != want {
			t.Errorf("bindsActor(%q) = %v, want %v", statement, got, want)
		}
	}
	if accessViews.MatchString("select projects.access_version as project_access_version") {
		t.Error("a project_access_version column passes for the project_access view")
	}
	if !accessViews.MatchString("join project_access on true") {
		t.Error("the project_access view is not recognized")
	}
}

func TestGoCodeHasNoRawSQLOverProjectScopedTables(t *testing.T) {
	t.Parallel()

	raw := regexp.MustCompile(
		`(?i)\b(from|join|update|delete\s+from)\s+(projects|tasks|task_dependencies|conversations)\b`,
	)
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING || !strings.HasPrefix(literal.Value, "`") {
				return true
			}
			text, err := strconv.Unquote(literal.Value)
			if err != nil || !raw.MatchString(text) {
				return true
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				t.Fatal(err)
			}
			position := fset.Position(literal.Pos())
			found[filepath.ToSlash(relative)+":"+strconv.Itoa(position.Line)] = true
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan internal: %v", err)
	}
	var unexpected []string
	for location := range found {
		if _, ok := rawGuardedSQL[location]; !ok {
			unexpected = append(unexpected, location)
		}
	}
	sort.Strings(unexpected)
	for _, location := range unexpected {
		t.Errorf("%s: hand-written SQL reaches a Project-scoped table; write it as a sqlc query "+
			"that joins the access views", location)
	}
	for location := range rawGuardedSQL {
		if !found[location] {
			t.Errorf("%s is allowlisted but holds no such SQL any more; remove or move the entry", location)
		}
	}
}

func matchesAny(text string, patterns []*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(text) {
			return true
		}
	}
	return false
}

// bindsActor reports whether a normalized statement binds a person it acts
// for: an actorBinding match that is not one of storedPersonParams.
func bindsActor(statement string) bool {
	for _, match := range actorBinding.FindAllStringSubmatch(statement, -1) {
		if !storedPersonParams[match[1]] {
			return true
		}
	}
	for _, match := range positionalActorBinding.FindAllStringSubmatch(statement, -1) {
		column := match[1] + match[2]
		if !storedPersonParams[column] {
			return true
		}
	}
	return false
}
