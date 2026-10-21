//go:build integration

package company

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestSearchReturnsCanonicalResultsWithoutDisclosingOtherDirectConversations(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	directMessageID := dbtest.NewUUID(t)
	privateConversationID := dbtest.NewUUID(t)
	privateMessageID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE users SET avatar_url = 'https://cdn.example.com/search-owner.png' WHERE id = $1`,
		fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE conversation_items SET content = 'Release plan for the task' WHERE id = $1`,
		fixture.MessageA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE conversation_items SET content = 'Release plan from another company' WHERE id = $1`,
		fixture.MessageB)
	directRunID := seedSearchRun(t, ctx, pool, fixture, fixture.DirectConversationA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items (
			id, company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		) VALUES ($1, $2, $3, $4, 1, 'user_message', 'user', $5, 'Release plan in direct chat')`,
		directMessageID, fixture.CompanyA, fixture.DirectConversationA, directRunID, fixture.UserA)
	// A tool call the same Run produced, matching the same query. Visibility is
	// the item's type now, so this must stay out of the results even though it
	// is the caller's own conversation — the two-branch (kind, author_kind)
	// predicate that used to decide this is gone.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items (
			id, company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		) VALUES ($1, $2, $3, $4, 2, 'tool_call', 'agent', $5, 'Release plan tool invocation')`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.DirectConversationA, directRunID, fixture.AgentA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (
			id, company_id, kind, agent_id, created_by_user_id, title
		) VALUES ($1, $2, 'direct', $3, $4, 'Private release conversation')`,
		privateConversationID, fixture.CompanyA, fixture.AgentA, fixture.UserB)
	privateRunID := seedSearchRun(t, ctx, pool, fixture, privateConversationID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items (
			id, company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		) VALUES ($1, $2, $3, $4, 1, 'user_message', 'user', $5, 'Private release plan')`,
		privateMessageID, fixture.CompanyA, privateConversationID, privateRunID, fixture.UserB)

	service, err := NewSearchService(store)
	if err != nil {
		t.Fatalf("NewSearchService() error = %v", err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA}
	page, err := service.Search(ctx, principal, SearchInput{
		Query: "release", Types: []SearchType{SearchTypeMessage}, Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("search response = %s, want the caller's two messages only", encoded)
	}

	resultsByConversation := make(map[string]map[string]any, len(body.Items))
	for _, result := range body.Items {
		conversationID, _ := result["conversationId"].(string)
		resultsByConversation[conversationID] = result
		if _, exists := result["messageId"]; exists {
			t.Fatalf("legacy messageId in result = %#v", result)
		}
		if _, exists := result["route"]; exists {
			t.Fatalf("legacy route in result = %#v", result)
		}
	}

	taskResult := resultsByConversation[fixture.ConversationA.String()]
	assertCanonicalSearchMessage(
		t,
		taskResult,
		fixture.MessageA.String(),
		fixture.TaskA.String(),
		"task_a",
		"task_conversation",
		fixture.AgentA.String(),
		fixture.UserA.String(),
	)
	if taskResult["number"] != float64(1) {
		t.Fatalf("task-conversation message carries number %v, want the task's 1", taskResult["number"])
	}

	// A person types what they read off the board: the displayed label
	// SCA-1 finds the task whose key is the plan slug task_a and ranks it
	// first, ahead of SCA-12 whose label merely starts with the query; both
	// hits carry the number every surface formats with the prefix.
	twelveID := dbtest.NewUUID(t)
	twelveConversationID := dbtest.NewUUID(t)
	// The task <-> conversation edge is deferrable, so both rows land in one
	// transaction.
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(ctx, `
		INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
		VALUES ($1, $2, 'task', $3, 'Task twelve', (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
		twelveConversationID, fixture.CompanyA, twelveID); err != nil {
		t.Fatalf("seed task twelve conversation: %v", err)
	}
	if _, err := seedTx.Exec(
		ctx,
		`
		INSERT INTO tasks (
			id, company_id, project_id, conversation_id, milestone_type,
			number, key, title, executed_by_agent_id, assignee_user_id, creator_user_id
		) VALUES ($1, $2, $6, $3, 'product', 12, 'task_twelve', 'Task twelve', $4, $5, $5)`,
		twelveID,
		fixture.CompanyA,
		twelveConversationID,
		fixture.AgentA,
		fixture.UserA,
		fixture.ProjectA,
	); err != nil {
		t.Fatalf("seed task twelve: %v", err)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	labelPage, err := service.Search(ctx, principal, SearchInput{
		Query: "sca-1", Types: []SearchType{SearchTypeTask}, Limit: 50,
	})
	if err != nil {
		t.Fatalf("Search(label) error = %v", err)
	}
	if len(labelPage.Items) != 2 {
		t.Fatalf("Search(label) = %+v, want the exact label hit and the prefix hit", labelPage.Items)
	}
	first, ok := labelPage.Items[0].(SearchTaskResult)
	if !ok || first.ID != fixture.TaskA || first.Key != "task_a" || first.Number != 1 {
		t.Fatalf("Search(label) first hit = %+v, want task_a as number 1", labelPage.Items[0])
	}
	second, ok := labelPage.Items[1].(SearchTaskResult)
	if !ok || second.ID != twelveID || second.Number != 12 {
		t.Fatalf("Search(label) second hit = %+v, want task_twelve as number 12", labelPage.Items[1])
	}
	directResult := resultsByConversation[fixture.DirectConversationA.String()]
	assertCanonicalSearchMessage(
		t,
		directResult,
		directMessageID.String(),
		nil,
		nil,
		"direct_conversation",
		fixture.AgentA.String(),
		fixture.UserA.String(),
	)
	if number, present := directResult["number"]; !present || number != nil {
		t.Fatalf("direct-conversation message carries number %v, want null", number)
	}
}

func assertCanonicalSearchMessage(
	t *testing.T,
	result map[string]any,
	wantID string,
	wantTaskID any,
	wantKey any,
	wantRouteKind, wantAgentID, wantAuthorID string,
) {
	t.Helper()
	if result == nil || result["kind"] != "message" || result["id"] != wantID ||
		result["taskId"] != wantTaskID || result["key"] != wantKey ||
		result["routeKind"] != wantRouteKind {
		t.Fatalf("message result = %#v", result)
	}
	agent, _ := result["agent"].(map[string]any)
	if agent["id"] != wantAgentID || agent["name"] != "Agent A" {
		t.Fatalf("message agent = %#v", agent)
	}
	author, _ := result["author"].(map[string]any)
	if author["id"] != wantAuthorID || author["kind"] != "user" ||
		author["name"] != "Schema A" ||
		author["avatarUrl"] != "https://cdn.example.com/search-owner.png" {
		t.Fatalf("message author = %#v", author)
	}
}

// seedSearchRun inserts one completed Run for a direct conversation and
// returns its id. Every conversation item carries a run_id, and the id comes
// back from runs_id_seq rather than being minted: runs.id is a bigserial, and
// seeding one by hand would leave the sequence behind it.
func seedSearchRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	conversationID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var runID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id,
			status
		, run_type) VALUES ($1, $2, $3, $4, 'completed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, conversationID, fixture.RuntimeA,
	).Scan(&runID); err != nil {
		t.Fatalf("seed search Run: %v", err)
	}
	return runID
}

func TestSearchDiscussionMembershipAndFilters(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	id := dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, f.UserB, nil)
	var root, reply uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,author_id,request_key,send_fingerprint,body,content) VALUES($1,$2,1,'user_message','user',$3,gen_random_uuid(),decode(repeat('00',32),'hex'),'{"version":1,"markdown":"thiết kế","references":[]}','thiết kế') RETURNING id`, f.CompanyA, id, f.UserB).
		Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,author_id,request_key,send_fingerprint,body,content,thread_root_id) VALUES($1,$2,2,'user_message','user',$3,gen_random_uuid(),decode(repeat('00',32),'hex'),'{"version":1,"markdown":"thiết kế mới","references":[]}','thiết kế mới',$4) RETURNING id`, f.CompanyA, id, f.UserB, root).
		Scan(&reply); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSearchService(store)
	if err != nil {
		t.Fatal(err)
	}
	input := SearchInput{Query: "thiết", Types: []SearchType{SearchTypeMessage}, Limit: 50, ConversationID: &id}
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA}
	page, err := service.Search(ctx, p, input)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("outsider search=%+v,%v", page, err)
	}
	p.UserID = f.UserB
	page, err = service.Search(ctx, p, input)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("member search=%+v,%v", page, err)
	}
	input.AuthorID = &f.UserA
	page, err = service.Search(ctx, p, input)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("author filter=%+v,%v", page, err)
	}
	input.AuthorID = nil
	input.HasFile = true
	page, err = service.Search(ctx, p, input)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("file filter=%+v,%v", page, err)
	}
	input.HasFile = false
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE conversation_items SET deleted_at=now(),body=NULL,content='' WHERE id=$1`,
		reply,
	)
	page, err = service.Search(ctx, p, input)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("deleted search=%+v,%v", page, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserB,
	)
	page, err = service.Search(ctx, p, input)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("revoked search=%+v,%v", page, err)
	}
}

// Search finds a Task, and a message in its chat, only for people who read
// the Task's Project (ADR 0017). A member without a grant on a restricted
// Project finds neither; the same member with project.read finds both.
func TestSearchFindsTasksOnlyInReadableProjects(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE conversation_items SET content = 'Release plan for the task' WHERE id = $1`,
		f.MessageA,
	)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSearchService(store)
	if err != nil {
		t.Fatal(err)
	}
	member := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB}
	search := func(query string, searchType SearchType) int {
		t.Helper()
		page, err := service.Search(
			ctx,
			member,
			SearchInput{Query: query, Types: []SearchType{searchType}, Limit: 50},
		)
		if err != nil {
			t.Fatalf("Search(%q) error = %v", query, err)
		}
		return len(page.Items)
	}
	if tasks, messages := search(
		"task_a",
		SearchTypeTask,
	), search(
		"release",
		SearchTypeMessage,
	); tasks != 0 ||
		messages != 0 {
		t.Fatalf("a member outside the Project found %d Task(s) and %d message(s), want none", tasks, messages)
	}
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserB, "project.read")
	if tasks, messages := search(
		"task_a",
		SearchTypeTask,
	), search(
		"release",
		SearchTypeMessage,
	); tasks != 1 ||
		messages != 1 {
		t.Fatalf("a reader found %d Task(s) and %d message(s), want one of each", tasks, messages)
	}
}

// hasFile counts only a file the caller may open (ADR 0017): a channel
// message whose only file is a Project file of a Project the caller cannot
// read is no file match for them, a plain chat file is, and so is the
// Project file once they read its Project. A file whose bytes never became
// ready counts for nobody, as the message list shows no such file.
func TestSearchHasFileCountsOnlyFilesTheCallerMayOpen(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	channel := dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, f.UserA, nil)
	dbtest.AddDiscussionMember(t, ctx, pool, f.CompanyA, channel, f.UserB)
	message := func(seq int, text, status string) uuid.UUID {
		t.Helper()
		var messageID, file uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,author_id,request_key,send_fingerprint,body,content)
			VALUES($1,$2,$3,'user_message','user',$4,gen_random_uuid(),decode(repeat('00',32),'hex'),jsonb_build_object('version',1,'markdown',$5::text,'references','[]'::jsonb),$5) RETURNING id`,
			f.CompanyA, channel, seq, f.UserA, text).
			Scan(&messageID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO attachments
			(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
			VALUES ($1, $2, gen_random_uuid(), 'notes.txt', 'text/plain', 4, $3) RETURNING id`,
			f.CompanyA, f.UserA, status).Scan(&file); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
			VALUES ($1, $2, $3, 0)`,
			f.CompanyA,
			messageID,
			file,
		)
		return file
	}
	projectFile := message(1, "payroll salaries", "ready")
	// ProjectA is restricted and only the fixture owner reads it.
	dbtest.MustExec(t, ctx, pool, `INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
		VALUES ($1, $2, $3, 'document')`, f.CompanyA, f.ProjectA, projectFile)
	message(2, "payroll launch notes", "ready")
	message(3, "payroll draft upload", "pending")

	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewSearchService(store)
	if err != nil {
		t.Fatal(err)
	}
	hits := func(userID uuid.UUID) []int64 {
		t.Helper()
		page, err := service.Search(ctx, auth.UserPrincipal{CompanyID: f.CompanyA, UserID: userID}, SearchInput{
			Query:          "payroll",
			Types:          []SearchType{SearchTypeMessage},
			Limit:          50,
			ConversationID: &channel,
			HasFile:        true,
		})
		if err != nil {
			t.Fatal(err)
		}
		seqs := []int64{}
		for _, item := range page.Items {
			if result, ok := item.(SearchMessageResult); ok {
				seqs = append(seqs, result.Seq)
			}
		}
		slices.Sort(seqs)
		return seqs
	}
	if got := hits(f.UserB); !slices.Equal(got, []int64{2}) {
		t.Fatalf(
			"a channel member outside the Project got hasFile hits %v, want only the plain chat file [2]",
			got,
		)
	}
	if got := hits(f.UserA); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("the Project reader got hasFile hits %v, want [1 2]", got)
	}
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserB, "project.read")
	if got := hits(f.UserB); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("the member granted the Project got hasFile hits %v, want [1 2]", got)
	}
}

// searchRecordingDBTX runs every statement against the pool and keeps the
// last one's text and arguments, so a test can EXPLAIN what sqlc sent.
type searchRecordingDBTX struct {
	pool *pgxpool.Pool
	sql  string
	args []any
}

func (d *searchRecordingDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.sql, d.args = sql, args
	return d.pool.Exec(ctx, sql, args...)
}

func (d *searchRecordingDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	d.sql, d.args = sql, args
	return d.pool.Query(ctx, sql, args...)
}

func (d *searchRecordingDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	d.sql, d.args = sql, args
	return d.pool.QueryRow(ctx, sql, args...)
}

// Search decides Project access on the tasks row it already reads, through
// the Task's own Project: its plan reaches tasks once in the Task arm and
// once in the message arm, and never again through the task_access view.
// Joined through that view, a message search once scanned every Task in the
// database to find the caller's readable Task chats.
func TestSearchReachesTasksOnce(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	recorder := &searchRecordingDBTX{pool: pool}
	if _, err := generated.New(recorder).SearchCompany(ctx, generated.SearchCompanyParams{
		PageSize: 20, Query: "task", TsQuery: "task:*", ActorUserID: f.UserA, CompanyID: f.CompanyA,
		IncludeTasks: true, IncludeConversations: true, IncludeMessages: true,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := pool.Query(ctx, `EXPLAIN (COSTS OFF) `+recorder.sql, recorder.args...)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(lines, "\n")
	scans := regexp.MustCompile(`(?m)Scan (?:using \S+ )?on tasks(?: \w+)?$`).FindAllString(text, -1)
	if len(scans) != 2 {
		t.Fatalf("search reaches tasks %d times, want once per arm that names them:\n%s", len(scans), text)
	}
}
