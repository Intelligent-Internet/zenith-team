//go:build integration

package inbox

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// seedInboxProjectTask seeds a restricted Project with one Task and its chat,
// and returns the three ids.
func seedInboxProjectTask(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	owner auth.UserPrincipal,
	key string,
) (projectID, taskID, conversationID uuid.UUID) {
	t.Helper()
	projectID, taskID, conversationID = uuid.New(), uuid.New(), uuid.New()
	mustInboxExec(
		t,
		ctx,
		pool,
		`INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, $3, 'Inbox Project')`,
		projectID,
		owner.CompanyID,
		key,
	)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO conversations (id, company_id, kind, task_id, project_id, title)
		  VALUES ($1, $2, 'task', $3, $4, 'Inbox Task')`, []any{conversationID, owner.CompanyID, taskID, projectID}},
		{`INSERT INTO tasks (id, company_id, conversation_id, project_id, milestone_type, number, key, title,
		                     assignee_user_id, creator_user_id)
		  SELECT $1, $2, $3, $4, 'product', coalesce(max(number), 0) + 1, $5, 'Inbox Task', $6, $6
		  FROM tasks WHERE company_id = $2`, []any{taskID, owner.CompanyID, conversationID, projectID, key + "_task", owner.UserID}},
	} {
		if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed Project Task: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return projectID, taskID, conversationID
}

func grantInboxProjectRead(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, projectID, userID uuid.UUID,
) {
	t.Helper()
	mustInboxExec(t, ctx, pool, `
		INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
		VALUES ($1, $2, $3, 'project.read', $3)`, companyID, projectID, userID)
}

// An Inbox item stores the Project it is about and reaches only the
// Project's readers (ADR 0017): CreateInTx skips a recipient who cannot read
// it, and every read and set write leaves out an item whose Project the
// recipient no longer reads, while items about no Project stay.
func TestInboxItemsFollowTheirProject(t *testing.T) {
	pool := startInboxPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner, member := seedInboxCompany(t, ctx, pool)
	projectID, taskID, conversationID := seedInboxProjectTask(t, ctx, pool, owner, "inbox_project")
	grantInboxProjectRead(t, ctx, pool, owner.CompanyID, projectID, owner.UserID)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}

	create := func(recipient auth.UserPrincipal, key string, conversation *uuid.UUID, entity Entity) CreateResult {
		t.Helper()
		var result CreateResult
		if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			var pending []events.Event
			var err error
			result, err = CreateInTx(ctx, q, &pending, CreateInput{
				CompanyID:       owner.CompanyID,
				RecipientUserID: recipient.UserID,
				ConversationID:  conversation,
				Type:            TypeBlocker,
				Entity:          entity,
				Title:           key,
				Body:            key,
				DedupeKey:       key,
			})
			return err
		}); err != nil {
			t.Fatalf("CreateInTx(%s) error = %v", key, err)
		}
		return result
	}
	storedProject := func(itemID uuid.UUID) uuid.NullUUID {
		t.Helper()
		var project uuid.NullUUID
		if err := pool.QueryRow(ctx, `SELECT project_id FROM inbox_items WHERE id = $1`, itemID).
			Scan(&project); err != nil {
			t.Fatal(err)
		}
		return project
	}

	byChat := create(
		owner,
		"by-chat",
		&conversationID,
		Entity{Type: protocol.EntityConversation, ID: conversationID.String()},
	)
	byTask := create(owner, "by-task", nil, Entity{Type: protocol.EntityTask, ID: taskID.String()})
	elsewhere := create(
		owner,
		"no-project",
		nil,
		Entity{Type: protocol.EntityCompany, ID: owner.CompanyID.String()},
	)
	if !byChat.Created || !byTask.Created || !elsewhere.Created {
		t.Fatalf(
			"created = %t %t %t, want every item for a reader",
			byChat.Created,
			byTask.Created,
			elsewhere.Created,
		)
	}
	for name, item := range map[string]CreateResult{"chat": byChat, "task": byTask} {
		if got := storedProject(item.Item.ID); !got.Valid || got.UUID != projectID {
			t.Errorf("item named by its %s stores Project %v, want %s", name, got, projectID)
		}
	}
	if got := storedProject(elsewhere.Item.ID); got.Valid {
		t.Errorf("item about no Project stores %v", got)
	}
	// A recipient who cannot read the Project is not told about it.
	if skipped := create(
		member,
		"member-by-task",
		nil,
		Entity{Type: protocol.EntityTask, ID: taskID.String()},
	); skipped.Created ||
		skipped.Item.ID != uuid.Nil {
		t.Fatalf("CreateInTx for a non-reader = %+v, want nothing", skipped)
	}
	var memberRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE recipient_user_id = $1`,
		member.UserID).Scan(&memberRows); err != nil || memberRows != 0 {
		t.Fatalf("non-reader Inbox rows = %d, %v; want 0", memberRows, err)
	}
	// A replay of a create stays a replay for a reader.
	if replay := create(
		owner,
		"by-task",
		nil,
		Entity{Type: protocol.EntityTask, ID: taskID.String()},
	); replay.Created ||
		replay.Item.ID != byTask.Item.ID {
		t.Fatalf("replayed create = %+v, want the stored item", replay)
	}

	page, err := service.List(ctx, owner, ListOptions{Status: StatusAll})
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("reader List = %d items, %v; want 3", len(page.Items), err)
	}
	counts, err := service.Counts(ctx, owner)
	if err != nil || counts.UnreadCount != 3 {
		t.Fatalf("reader Counts = %+v, %v; want 3 unread", counts, err)
	}

	// The owner loses the Project: its items disappear from every read.
	mustInboxExec(t, ctx, pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		projectID, owner.UserID)
	page, err = service.List(ctx, owner, ListOptions{Status: StatusAll})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != elsewhere.Item.ID {
		t.Fatalf(
			"List after losing the Project = %+v, %v; want only the item about no Project",
			page.Items,
			err,
		)
	}
	if counts, err = service.Counts(ctx, owner); err != nil || counts.UnreadCount != 1 {
		t.Fatalf("Counts after losing the Project = %+v, %v; want 1 unread", counts, err)
	}
	if _, err := service.Get(ctx, owner, byTask.Item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(hidden item) error = %v, want ErrNotFound", err)
	}
	if _, err := service.Mutate(
		ctx,
		owner,
		byTask.Item.ID,
		MutationRead,
		byTask.Item.Revision,
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("Mutate(hidden item) error = %v, want ErrNotFound", err)
	}
	if _, err := service.ReadConversation(ctx, owner, conversationID); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("ReadConversation(unreadable Task chat) error = %v, want ErrConversationNotFound", err)
	}
	readAll, err := service.ReadAll(ctx, owner)
	if err != nil || len(readAll.Items) != 1 || readAll.Items[0].ID != elsewhere.Item.ID {
		t.Fatalf("ReadAll = %+v, %v; want only the item about no Project", readAll.Items, err)
	}
	var hiddenUnread int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE project_id = $1 AND read_at IS NULL`,
		projectID).Scan(&hiddenUnread); err != nil || hiddenUnread != 2 {
		t.Fatalf("hidden items still unread = %d, %v; want 2 untouched", hiddenUnread, err)
	}
	// Closing the Task's items still closes the hidden one, but sends its
	// recipient no frame naming it.
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var pending []events.Event
		if err := CloseEntityInTx(ctx, q, &pending, owner.CompanyID,
			Entity{Type: protocol.EntityTask, ID: taskID.String()}, Actor{Kind: "system"}); err != nil {
			return err
		}
		if len(pending) != 0 {
			t.Errorf("closing a hidden item staged %d frames, want none", len(pending))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var closed bool
	if err := pool.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM inbox_items WHERE id = $1`,
		byTask.Item.ID).Scan(&closed); err != nil || !closed {
		t.Fatalf("hidden item closed = %t, %v; want closed", closed, err)
	}
	// Losing the Project is not deleting the items: a grant brings them back.
	grantInboxProjectRead(t, ctx, pool, owner.CompanyID, projectID, owner.UserID)
	if page, err = service.List(ctx, owner, ListOptions{Status: StatusAll}); err != nil || len(page.Items) != 3 {
		t.Fatalf("List after the grant returns = %d items, %v; want 3", len(page.Items), err)
	}
}

// countsVersion is a monotonic ordering token (inbox.md): an empty Inbox is
// version 1, and a grant change that hides the recipient's newest item, or
// shows it again, moves the version forward, never back, so the counts read
// after the change outrank any counts frame built before it (ADR 0017). The
// account-wide read agrees, and the read inside a transaction participant
// (GetInboxCountsForRecipient) follows the same token.
func TestInboxCountsVersionMovesForwardAcrossProjectAccessChanges(t *testing.T) {
	pool := startInboxPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	owner, member := seedInboxCompany(t, ctx, pool)
	projectID, taskID, _ := seedInboxProjectTask(t, ctx, pool, owner, "inbox_version")
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	queries := generated.New(pool)
	counts := func(name string) Counts {
		t.Helper()
		company, err := service.Counts(ctx, member)
		if err != nil {
			t.Fatalf("%s: Counts error = %v", name, err)
		}
		account := member
		account.CompanyID = uuid.Nil
		everywhere, err := service.Counts(ctx, account)
		if err != nil {
			t.Fatalf("%s: account Counts error = %v", name, err)
		}
		if everywhere != company {
			t.Fatalf("%s: account counts %+v, Company counts %+v; want the same", name, everywhere, company)
		}
		recipient, err := queries.GetInboxCountsForRecipient(ctx, generated.GetInboxCountsForRecipientParams{
			CompanyID: member.CompanyID, UserID: member.UserID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := countsVersionFromOrderingToken(recipient.OrderingToken); got != company.CountsVersion ||
			recipient.UnreadCount != company.UnreadCount {
			t.Fatalf("%s: transaction counts {%d %d}, read counts %+v; want the same", name,
				recipient.UnreadCount, got, company)
		}
		return company
	}
	accessRevision := func() int64 {
		t.Helper()
		var revision int64
		if err := pool.QueryRow(ctx, `SELECT access_version FROM projects WHERE id = $1`, projectID).
			Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	// The grant statements a grant change runs: the rows, then the revision.
	grant := func() {
		t.Helper()
		grantInboxProjectRead(t, ctx, pool, owner.CompanyID, projectID, member.UserID)
		if _, err := queries.BumpProjectAccessVersion(ctx, generated.BumpProjectAccessVersionParams{
			CompanyID: owner.CompanyID, ProjectID: projectID, ExpectedRevision: accessRevision(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	revoke := func() {
		t.Helper()
		if _, err := queries.DeleteProjectGrantsForUser(ctx, generated.DeleteProjectGrantsForUserParams{
			CompanyID: owner.CompanyID, ProjectID: projectID, UserID: member.UserID,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := queries.BumpProjectAccessVersion(ctx, generated.BumpProjectAccessVersionParams{
			CompanyID: owner.CompanyID, ProjectID: projectID, ExpectedRevision: accessRevision(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The version has millisecond grain. Before each access change the
	// stored times are aged by a second and the counts read again, so the
	// change, even in the same millisecond, lands after that read.
	age := func(name string) Counts {
		t.Helper()
		mustInboxExec(t, ctx, pool, `UPDATE inbox_items SET updated_at = updated_at - interval '1 second'
			WHERE recipient_user_id = $1`, member.UserID)
		mustInboxExec(
			t,
			ctx,
			pool,
			`UPDATE projects SET access_changed_at = access_changed_at - interval '1 second'
			WHERE id = $1`,
			projectID,
		)
		return counts(name)
	}

	if empty := counts("empty Inbox"); empty != (Counts{UnreadCount: 0, CountsVersion: 1}) {
		t.Fatalf("empty Inbox counts = %+v, want {0 1}", empty)
	}
	grant()
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var pending []events.Event
		created, err := CreateInTx(ctx, q, &pending, CreateInput{
			CompanyID:       owner.CompanyID,
			RecipientUserID: member.UserID,
			Type:            TypeBlocker,
			Entity: Entity{
				Type: protocol.EntityTask,
				ID:   taskID.String(),
			},
			Title:     "t",
			Body:      "t",
			DedupeKey: "version",
		})
		if err == nil && !created.Created {
			err = errors.New("the reader got no item")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	seen := age("item seen")
	if seen.UnreadCount != 1 || seen.CountsVersion <= 1 {
		t.Fatalf("counts with the item = %+v, want 1 unread and a real version", seen)
	}
	revoke()
	hidden := counts("item hidden")
	if hidden.UnreadCount != 0 || hidden.CountsVersion <= seen.CountsVersion {
		t.Fatalf(
			"counts after the revoke = %+v, want 0 unread and a version above %d",
			hidden,
			seen.CountsVersion,
		)
	}
	stillHidden := age("item still hidden")
	grant()
	back := counts("item back")
	if back.UnreadCount != 1 || back.CountsVersion <= stillHidden.CountsVersion {
		t.Fatalf(
			"counts after the regrant = %+v, want 1 unread and a version above %d",
			back,
			stillHidden.CountsVersion,
		)
	}
}

// inboxExplainRecorder runs every statement against the pool and keeps the
// last one's text and arguments, so a test can EXPLAIN what sqlc sent.
type inboxExplainRecorder struct {
	pool *pgxpool.Pool
	sql  string
	args []any
}

func (r *inboxExplainRecorder) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.sql, r.args = sql, args
	return r.pool.Exec(ctx, sql, args...)
}

func (r *inboxExplainRecorder) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r.sql, r.args = sql, args
	return r.pool.Query(ctx, sql, args...)
}

func (r *inboxExplainRecorder) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	r.sql, r.args = sql, args
	return r.pool.QueryRow(ctx, sql, args...)
}

// The Inbox list and counts decide Project access per row: one probe of the
// recipient's grants by index for each row (never a hashed subplan that
// builds their whole readable set), and never a scan of every grant or every
// Inbox row.
func TestInboxReadsProbeProjectAccessPerRow(t *testing.T) {
	pool := startInboxPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	owner, member := seedInboxCompany(t, ctx, pool)
	mustInboxExec(t, ctx, pool, `
		INSERT INTO projects (company_id, key, title)
		SELECT $1, 'bulk_' || n, 'Bulk ' || n FROM generate_series(1, 40) AS n`, owner.CompanyID)
	// Colleagues with grants and Inbox items on every Project, so a scan of
	// project_grants or of inbox_items would show.
	mustInboxExec(t, ctx, pool, `
		WITH colleague AS (
			INSERT INTO users (email, display_name)
			SELECT 'colleague-' || n || '@example.com', 'Colleague ' || n FROM generate_series(1, 40) AS n
			RETURNING id
		), membership AS (
			INSERT INTO company_members (company_id, user_id, role)
			SELECT $1, colleague.id, 'member' FROM colleague
			RETURNING user_id
		), granted AS (
			INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
			SELECT $1, project.id, membership.user_id, 'project.read', $2
			FROM membership CROSS JOIN projects AS project
			WHERE project.company_id = $1
			RETURNING user_id, project_id
		)
		INSERT INTO inbox_items (company_id, recipient_user_id, kind, entity_type, entity_id, title, body,
		                         dedupe_key, project_id)
		SELECT $1::uuid, granted.user_id, 'blocker', 'company', $1::uuid::text, 'Bulk', 'Bulk',
		       'bulk-' || granted.user_id || '-' || granted.project_id || '-' || n, granted.project_id
		FROM granted CROSS JOIN generate_series(1, 5) AS n`, owner.CompanyID, owner.UserID)
	// The member's own items, one Project of forty readable.
	mustInboxExec(t, ctx, pool, `
		INSERT INTO inbox_items (company_id, recipient_user_id, kind, entity_type, entity_id, title, body,
		                         dedupe_key, project_id)
		SELECT $1::uuid, $2, 'blocker', 'company', $1::uuid::text, 'Mine', 'Mine',
		       'mine-' || project.id || '-' || n, project.id
		FROM projects AS project CROSS JOIN generate_series(1, 5) AS n
		WHERE project.company_id = $1`, owner.CompanyID, member.UserID)
	var readable uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects WHERE company_id = $1 AND key = 'bulk_7'`,
		owner.CompanyID).Scan(&readable); err != nil {
		t.Fatal(err)
	}
	grantInboxProjectRead(t, ctx, pool, owner.CompanyID, readable, member.UserID)
	// Forty Groups of forty more colleagues, each Group holding grants on
	// every Project, so a scan of company_group_members or
	// project_group_grants would show too.
	dbtest.SeedGroupColleagues(t, ctx, pool, owner.CompanyID, "inbox-group-colleague", 40)
	mustInboxExec(t, ctx, pool, `ANALYZE`)

	recorder := &inboxExplainRecorder{pool: pool}
	queries := generated.New(recorder)
	grantScan := regexp.MustCompile(
		`(?m)Seq Scan on (project_grants|company_group_members|project_group_grants)( |$)`,
	)
	perRowProject := regexp.MustCompile(`\(project_id IS NULL\) OR COALESCE\(\(SubPlan \d+\), false\)`)
	itemScan := regexp.MustCompile(`(?m)Seq Scan on inbox_items( |$)`)
	assertPerRow := func(name string) {
		t.Helper()
		plan, err := pool.Query(ctx, `EXPLAIN (COSTS OFF) `+recorder.sql, recorder.args...)
		if err != nil {
			t.Fatalf("%s explain: %v", name, err)
		}
		lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(lines, "\n")
		if grantScan.MatchString(text) {
			t.Errorf("%s scans all of a grant or Group table:\n%s", name, text)
		}
		// The older membership and deliverable arms may be hashed; the Project
		// arm must stay a per-row subplan.
		if !perRowProject.MatchString(text) {
			t.Errorf("%s does not probe the Project per row:\n%s", name, text)
		}
		if itemScan.MatchString(text) {
			t.Errorf("%s scans all of inbox_items:\n%s", name, text)
		}
	}

	page, err := listPage(ctx, queries, member.CompanyID, member.UserID, ListOptions{Status: StatusAll, Limit: 50})
	if err != nil || len(page.Items) != 5 {
		t.Fatalf("member list = %d items, %v; want the readable Project's 5", len(page.Items), err)
	}
	assertPerRow("the Inbox list")
	counts, err := queries.GetInboxCounts(ctx, generated.GetInboxCountsParams{
		CompanyID: member.CompanyID, UserID: member.UserID,
	})
	if err != nil || counts.UnreadCount != 5 {
		t.Fatalf("member counts = %+v, %v; want 5 unread", counts, err)
	}
	assertPerRow("the Inbox counts")
}
