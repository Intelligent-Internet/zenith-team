//go:build integration

package execution

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

// What Mia holds as a person, the expected side of the table below: her
// own check, before any Run narrows it. She is a member, so the management
// bridge never applies to her.
const (
	// The member's own read (project_access).
	personReadCheck = `
		SELECT EXISTS (SELECT 1 FROM project_access
		               WHERE company_id = $1 AND project_id = $2 AND user_id = $3)`
	// The member's own hold of one permission.
	personHoldsCheck = `SELECT project_permission_held($1, $2, $3, $4, $5, $6, $7)`
)

// delegateRunKind is the chat a Run answers into: a Task chat, a channel it
// was tagged in, or its person's direct chat.
type delegateRunKind string

const (
	taskRunKind   delegateRunKind = "Task Run"
	taggedRunKind delegateRunKind = "tagged Run"
	directRunKind delegateRunKind = "direct Run"
)

// delegateAccess is what one person holds on the Project a row asks about.
type delegateAccess string

const (
	accessNone delegateAccess = "nothing"
	accessRead delegateAccess = "read"
	accessWork delegateAccess = "read, write and execute"
)

// delegateRuleWorld is one Company where Mia runs the fixture Agent on her
// own computer in three chats, Leo shares a channel with her, Project T holds
// the Task chat and Project O does not, and each Project holds one file that
// is also shared in the channel.
type delegateRuleWorld struct {
	ctx            context.Context
	pool           *pgxpool.Pool
	q              *db.Queries
	f              dbtest.Fixture
	mia, leo       uuid.UUID
	runtime        uuid.UUID
	channel        uuid.UUID
	projectT       uuid.UUID
	projectO       uuid.UUID
	runs           map[delegateRunKind]uuid.UUID
	chats          map[delegateRunKind]uuid.UUID
	files          map[uuid.UUID]uuid.UUID
	channelMessage uuid.UUID
}

func newDelegateRuleWorld(t *testing.T) *delegateRuleWorld {
	t.Helper()
	ctx, pool, f := startProjectAccessRunFixture(t)
	w := &delegateRuleWorld{
		ctx: ctx, pool: pool, q: db.New(pool), f: f,
		mia: dbtest.NewUUID(t), leo: dbtest.NewUUID(t), runtime: dbtest.NewUUID(t),
		projectT: f.ProjectA, projectO: dbtest.NewUUID(t),
		runs: map[delegateRunKind]uuid.UUID{}, chats: map[delegateRunKind]uuid.UUID{},
		files: map[uuid.UUID]uuid.UUID{},
	}
	for _, person := range []struct {
		id    uuid.UUID
		email string
	}{{w.mia, "mia@example.com"}, {w.leo, "leo@example.com"}} {
		dbtest.MustExec(t, ctx, pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
			person.id, person.email)
		dbtest.MustExec(t, ctx, pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
			f.CompanyA, person.id)
	}
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, 'other', 'Other')`,
		w.projectO, f.CompanyA)
	// Mia's own computer, where her binding of the Agent runs (ADR 0014).
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-mia', 'Mia Mac', 'codex', 'online', $3, now())`, w.runtime, f.CompanyA, w.mia)
	dbtest.SeedAgentBinding(t, ctx, pool, f.CompanyA, f.AgentA, w.mia, w.runtime, "codex")

	w.channel = dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, w.mia, nil)
	dbtest.AddDiscussionMember(t, ctx, pool, f.CompanyA, w.channel, w.leo)
	direct := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, $2, 'direct', $3, $4, 'Mia and the Agent')`, direct, f.CompanyA, f.AgentA, w.mia)

	w.runs[taskRunKind] = dbtest.SeedRun(t, ctx, pool, dbtest.RunSeed{
		CompanyID: f.CompanyA, AgentID: f.AgentA, RuntimeID: w.runtime,
		ConversationID: f.ConversationA, TaskID: f.TaskA, AuthorizerID: w.mia,
	})
	w.runs[directRunKind] = dbtest.SeedRun(t, ctx, pool, dbtest.RunSeed{
		CompanyID: f.CompanyA, AgentID: f.AgentA, RuntimeID: w.runtime,
		ConversationID: direct, AuthorizerID: w.mia,
	})
	w.runs[taggedRunKind] = dbtest.SeedTaggedRun(t, ctx, pool, f.CompanyA, w.channel, f.AgentA, w.runtime, w.mia)
	for kind, runID := range w.runs {
		var chat uuid.UUID
		if err := pool.QueryRow(
			ctx, `SELECT conversation_id FROM runs WHERE id = $1`, runID,
		).Scan(&chat); err != nil {
			t.Fatal(err)
		}
		w.chats[kind] = chat
	}

	// One file in each Project, both also shared in the channel.
	if err := pool.QueryRow(ctx, `
		INSERT INTO conversation_items
			(company_id, conversation_id, seq, type, author_kind, author_id,
			 content, body, request_key, send_fingerprint)
		SELECT $1, $2, COALESCE(max(item.seq), 0) + 1, 'user_message', 'user', $3, 'files',
		       '{"markdown":"files"}'::jsonb, gen_random_uuid(), decode(repeat('22', 32), 'hex')
		FROM conversation_items AS item WHERE item.conversation_id = $2
		RETURNING id`, f.CompanyA, w.channel, w.mia).Scan(&w.channelMessage); err != nil {
		t.Fatal(err)
	}
	for position, project := range []uuid.UUID{w.projectT, w.projectO} {
		var file uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO attachments (company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
			VALUES ($1, $2, gen_random_uuid(), $3, 'text/plain', 1, 'ready') RETURNING id`,
			f.CompanyA, f.UserA, fmt.Sprintf("file-%d.txt", position)).Scan(&file); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO project_files (company_id, project_id, attachment_id, file_type) VALUES ($1, $2, $3, 'document')`,
			f.CompanyA, project, file)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO message_attachments (company_id, message_id, attachment_id, position) VALUES ($1, $2, $3, $4)`,
			f.CompanyA, w.channelMessage, file, position)
		w.files[project] = file
	}
	return w
}

// arrange resets Mia's and Leo's access to the two Projects and gives the
// row's: Project X Company-visible or restricted, and what each holds on it.
func (w *delegateRuleWorld) arrange(t *testing.T, project uuid.UUID, visible bool, mia, leo delegateAccess) {
	t.Helper()
	dbtest.MustExec(t, w.ctx, w.pool, `
		DELETE FROM project_grants WHERE company_id = $1 AND user_id IN ($2, $3)`, w.f.CompanyA, w.mia, w.leo)
	dbtest.MustExec(t, w.ctx, w.pool, `
		UPDATE projects SET visibility = CASE WHEN id = $2 AND $3 THEN 'company' ELSE 'restricted' END
		WHERE company_id = $1 AND id IN ($4, $5)`, w.f.CompanyA, project, visible, w.projectT, w.projectO)
	for person, access := range map[uuid.UUID]delegateAccess{w.mia: mia, w.leo: leo} {
		switch access {
		case accessRead:
			dbtest.GrantProject(t, w.ctx, w.pool, w.f.CompanyA, project, person, "project.read")
		case accessWork:
			dbtest.GrantProject(t, w.ctx, w.pool, w.f.CompanyA, project, person)
		}
	}
}

func (w *delegateRuleWorld) scalar(t *testing.T, sql string, args ...any) bool {
	t.Helper()
	var value bool
	if err := w.pool.QueryRow(w.ctx, sql, args...).Scan(&value); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return value
}

// runPermits asks the new rule for one Run through the query RunScope uses.
func (w *delegateRuleWorld) runPermits(t *testing.T, runID, project uuid.UUID, permission auth.ProjectPermission) bool {
	t.Helper()
	ids, err := w.q.ListRunReadableProjects(w.ctx, db.ListRunReadableProjectsParams{
		ProjectIds: []uuid.UUID{project}, CompanyID: w.f.CompanyA, RunID: runID,
		Permission: string(permission), ReadPermission: string(auth.ProjectPermissionRead),
		CompanyDefaultPermissions: auth.ProjectPermissionHeldSets().CompanyDefaultPermissions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return slices.Contains(ids, project)
}

// claimable says whether the Run is a claim candidate on Mia's computer
// through both real claim queries. Only this Run is queued while it is
// asked.
func (w *delegateRuleWorld) claimable(t *testing.T, runID uuid.UUID) bool {
	t.Helper()
	dbtest.MustExec(t, w.ctx, w.pool, `UPDATE runs SET status = 'queued' WHERE id = $1`, runID)
	defer dbtest.MustExec(t, w.ctx, w.pool, `UPDATE runs SET status = 'running' WHERE id = $1`, runID)
	candidates, err := w.q.ListQueuedClaimCandidatesByRuntimes(w.ctx, claimCandidateParams(w.runtime))
	if err != nil {
		t.Fatal(err)
	}
	fence := claimCandidateParams()
	selected, err := w.q.SelectClaimableRun(w.ctx, db.SelectClaimableRunParams{
		AgentID: w.f.AgentA, RuntimeID: w.runtime, TaskLaneOpen: true,
		ExecutePermission: fence.ExecutePermission, ReadPermission: fence.ReadPermission,
		CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if listed, claimed := len(candidates) == 1, err == nil && selected.ID == runID; listed != claimed {
		t.Fatalf("the candidate listing says %t and the claim %t", listed, claimed)
	}
	return len(candidates) == 1
}

// holds says whether Mia, as a person, holds the permission and read on the
// Project: what the delegate rule lets her Runs use there.
func (w *delegateRuleWorld) holds(t *testing.T, project uuid.UUID, permission auth.ProjectPermission) bool {
	t.Helper()
	sets := auth.ProjectPermissionHeldSets()
	held := func(permission auth.ProjectPermission) bool {
		return w.scalar(t, personHoldsCheck, w.f.CompanyA, project, w.mia, string(permission),
			sets.CompanyDefaultPermissions, sets.ManagementPermissions, sets.ManagementRoles)
	}
	return held(auth.ProjectPermissionRead) && held(permission)
}

// A Run, or the voice assistant, uses its Authorizer's Project access, never
// more, and nothing else narrows it (D31, revised 2026-10-06): there is no
// chat audience. A Task chat's answer stays in that chat's Project. On every
// row of the table, a Task Run, a tagged Run and a direct Run × the Task
// chat's own Project or another × restricted or Company-visible × what Mia,
// the Authorizer, holds × whether Leo, who shares the channel, reads it,
// every place answers what Mia holds, and a Task Run only in its Task chat's
// Project:
//
//   - enqueue: RunConversationExecutePermitted;
//   - claim: the real claim queries;
//   - Run-proof reads and Task creates: ListRunReadableProjects;
//   - the voice reads, with Mia as the Authorizer: ListVoiceProjects and
//     ReadVoiceTasks;
//   - the Run file download (AuthorizeRunAttachmentDownload) and the channel
//     file freeze (ListRunChannelAttachments).
//
// Leo reading the Project or not never changes an answer, which pins that
// the audience term is gone.
func TestDelegateRuleUsesTheAuthorizersAccess(t *testing.T) {
	t.Parallel()
	w := newDelegateRuleWorld(t)
	sets := auth.ProjectPermissionHeldSets()
	read, execute := string(auth.ProjectPermissionRead), string(auth.ProjectPermissionTasksExecute)
	outcomes := map[string]map[bool]int{}
	// answers holds each place's answer per row without Leo's access, so the
	// second Leo row must repeat the first.
	answers := map[string]bool{}
	expect := func(t *testing.T, row, place string, want, got bool) {
		t.Helper()
		if outcomes[place] == nil {
			outcomes[place] = map[bool]int{}
		}
		outcomes[place][want]++
		if want != got {
			t.Errorf("%s: want %t, the delegate rule says %t", place, want, got)
		}
		key := row + " / " + place
		if earlier, seen := answers[key]; seen && earlier != got {
			t.Errorf("%s: Leo's access changed the answer from %t to %t", place, earlier, got)
		}
		answers[key] = got
	}

	for _, kind := range []delegateRunKind{taskRunKind, taggedRunKind, directRunKind} {
		for _, project := range []uuid.UUID{w.projectT, w.projectO} {
			for _, visible := range []bool{false, true} {
				for _, mia := range []delegateAccess{accessNone, accessRead, accessWork} {
					for _, leo := range []delegateAccess{accessNone, accessRead} {
						own := map[bool]string{true: "the Task chat's Project", false: "another Project"}[project == w.projectT]
						row := fmt.Sprintf(
							"%s, %s, company-visible %t, Mia holds %s",
							kind,
							own,
							visible,
							mia,
						)
						t.Run(row+", Leo holds "+string(leo), func(t *testing.T) {
							w.arrange(t, project, visible, mia, leo)
							runID, chat := w.runs[kind], w.chats[kind]
							// A Task Run reads and files work only in its Task
							// chat's Project; any other Run wherever Mia may.
							inScope := kind != taskRunKind || project == w.projectT
							// Enqueue and claim ask execute on the Task chat's
							// Project and pass any other chat.
							mayExecute := kind != taskRunKind ||
								w.holds(
									t,
									w.projectT,
									auth.ProjectPermissionTasksExecute,
								)

							// 1. Enqueue, in the chat the Run answers into.
							permitted, err := w.q.RunConversationExecutePermitted(
								w.ctx,
								db.RunConversationExecutePermittedParams{
									CompanyID:                 w.f.CompanyA,
									ConversationID:            chat,
									AuthorizerUserID:          w.mia,
									ExecutePermission:         execute,
									ReadPermission:            read,
									CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
								},
							)
							if err != nil {
								t.Fatal(err)
							}
							expect(t, row, "enqueue", mayExecute, permitted)

							// 2. Claim.
							expect(t, row, "claim", mayExecute, w.claimable(t, runID))

							// 3. Run-proof reads and creates: the member's own
							// check, then the Run's scope.
							expect(
								t,
								row,
								"Run-proof read",
								w.holds(t, project, auth.ProjectPermissionRead) &&
									inScope,
								w.scalar(t, personReadCheck, w.f.CompanyA, project, w.mia) &&
									w.runPermits(t, runID, project, auth.ProjectPermissionRead),
							)
							holdsWrite := w.holds(
								t,
								project,
								auth.ProjectPermissionTasksWrite,
							)
							expect(
								t,
								row,
								"Run Task create",
								holdsWrite && inScope,
								holdsWrite &&
									w.runPermits(
										t,
										runID,
										project,
										auth.ProjectPermissionTasksWrite,
									),
							)

							// 4. Voice, with Mia as the Authorizer.
							miaReads := w.holds(t, project, auth.ProjectPermissionRead)
							projects, err := w.q.ListVoiceProjects(
								w.ctx,
								db.ListVoiceProjectsParams{
									CompanyID:                 w.f.CompanyA,
									ContextGrantedByUserID:    w.mia,
									ReadPermission:            read,
									CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
								},
							)
							if err != nil {
								t.Fatal(err)
							}
							expect(t, row, "voice Projects", miaReads, slices.ContainsFunc(
								projects,
								func(listed db.ListVoiceProjectsRow) bool { return listed.ID == project },
							))
							if project == w.projectT {
								tasks, err := w.q.ReadVoiceTasks(
									w.ctx,
									db.ReadVoiceTasksParams{
										CompanyID:                 w.f.CompanyA,
										ContextGrantedByUserID:    w.mia,
										ReadPermission:            read,
										CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
										TaskID:                    w.f.TaskA,
									},
								)
								if err != nil {
									t.Fatal(err)
								}
								expect(t, row, "voice Tasks", miaReads, len(tasks) == 1)
							}

							// 5. The Run's download of the Project's file.
							_, err = w.q.AuthorizeRunAttachmentDownload(
								w.ctx,
								db.AuthorizeRunAttachmentDownloadParams{
									RunID:            runID,
									AuthorizerUserID: w.mia,
									AttachmentID:     w.files[project],
									CompanyID:        w.f.CompanyA,
								},
							)
							if err != nil && !errors.Is(err, pgx.ErrNoRows) {
								t.Fatal(err)
							}
							expect(
								t,
								row,
								"Run file download",
								miaReads && inScope,
								err == nil,
							)

							// 6. The files a tagged Run freezes from its channel.
							if kind == taggedRunKind {
								frozen, err := w.q.ListRunChannelAttachments(
									w.ctx,
									db.ListRunChannelAttachmentsParams{
										ActorUserID: w.mia,
										CompanyID:   w.f.CompanyA,
										MessageIds: []uuid.UUID{
											w.channelMessage,
										},
									},
								)
								if err != nil {
									t.Fatal(err)
								}
								expect(
									t,
									row,
									"channel file freeze",
									miaReads,
									slices.ContainsFunc(
										frozen,
										func(file db.ListRunChannelAttachmentsRow) bool {
											return file.Attachment.ID == w.files[project]
										},
									),
								)
							}
						})
					}
				}
			}
		}
	}
	// A table where a place always answers the same proves nothing about it.
	for _, place := range []string{
		"enqueue", "claim", "Run-proof read", "Run Task create", "voice Projects", "voice Tasks",
		"Run file download", "channel file freeze",
	} {
		if outcomes[place][true] == 0 || outcomes[place][false] == 0 {
			t.Errorf(
				"%s answered %v on every row; the table must reach both answers",
				place,
				outcomes[place],
			)
		}
	}

	t.Run("deliberately narrower", func(t *testing.T) {
		w.arrange(t, w.projectO, false, accessWork, accessRead)
		// The voice assistant's Authorizer must read the Project (D31; B5
		// then requires them in the call). An owner who cannot read Project O
		// cannot share it with the call, though Mia and Leo read it.
		projects, err := w.q.ListVoiceProjects(w.ctx, db.ListVoiceProjectsParams{
			CompanyID: w.f.CompanyA, ContextGrantedByUserID: w.f.UserA,
			ReadPermission: read, CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		})
		if err != nil || slices.ContainsFunc(projects, func(row db.ListVoiceProjectsRow) bool {
			return row.ID == w.projectO
		}) {
			t.Fatalf(
				"voice Projects for an Authorizer who cannot read Project O = %v, %v; want it left out",
				projects,
				err,
			)
		}

		// A delegate never manages: the owner's management bridge holds
		// project.settings.update on Project O without read, and no Run may
		// use it.
		if !w.scalar(t, personHoldsCheck, w.f.CompanyA, w.projectO, w.f.UserA,
			string(auth.ProjectPermissionSettingsUpdate),
			sets.CompanyDefaultPermissions, sets.ManagementPermissions, sets.ManagementRoles) {
			t.Fatal("the owner should manage Project O through projects.manage")
		}
		if w.scalar(t, `SELECT delegate_permission_held($1, $2, NULL, $3, $4, $5, $6)`,
			w.f.CompanyA, w.f.UserA, w.projectO, string(auth.ProjectPermissionSettingsUpdate), read,
			sets.CompanyDefaultPermissions) {
			t.Fatal("a delegate used the management bridge")
		}
	})

	t.Run("fails closed", func(t *testing.T) {
		// Both Projects Company-visible, so everyone reads both and only
		// the term under test can refuse.
		w.arrange(t, w.projectT, true, accessNone, accessNone)
		dbtest.MustExec(t, w.ctx, w.pool, `UPDATE projects SET visibility = 'company' WHERE id = $1`,
			w.projectO)
		ask := func(sql string, args ...any) bool { return w.scalar(t, sql, args...) }
		delegate := `SELECT delegate_permission_held($1, $2, $3, $4, $5, $5, $6)`
		for name, args := range map[string][]any{
			"no chat":                      {w.f.CompanyA, w.mia, nil, w.projectT},
			"the Task chat's own Project":  {w.f.CompanyA, w.mia, w.f.ConversationA, w.projectT},
			"the channel it was tagged in": {w.f.CompanyA, w.mia, w.chats[taggedRunKind], w.projectO},
		} {
			if !ask(delegate, append(args, read, sets.CompanyDefaultPermissions)...) {
				t.Errorf("%s: the delegate rule refuses a Project everyone reads", name)
			}
		}
		for name, args := range map[string][]any{
			"no Authorizer":               {w.f.CompanyA, nil, nil, w.projectT},
			"another Company":             {w.f.CompanyB, w.mia, nil, w.projectT},
			"a Task chat with no Project": {w.f.CompanyA, w.mia, seedLegacyTaskChat(t, w.ctx, w.pool, w.f.CompanyA), w.projectT},
			"another Project's Task chat": {w.f.CompanyA, w.mia, w.f.ConversationA, w.projectO},
		} {
			if ask(delegate, append(args, read, sets.CompanyDefaultPermissions)...) {
				t.Errorf("%s: the delegate rule admits", name)
			}
		}
		runHeld := `SELECT run_permission_held($1, $2, $3, $4, $4, $5)`
		if ask(runHeld, w.f.CompanyA, uuid.New(), w.projectT, read, sets.CompanyDefaultPermissions) {
			t.Error("a Run that does not exist may read")
		}
		if ask(runHeld, w.f.CompanyB, w.runs[directRunKind], w.projectT, read, sets.CompanyDefaultPermissions) {
			t.Error("a Run read through another Company may read")
		}
		// The fixture's own Runs predate authorizers.
		if ask(runHeld, w.f.CompanyA, w.f.RunA, w.projectT, read, sets.CompanyDefaultPermissions) {
			t.Error("a Run with no Authorizer may read")
		}
	})
}
