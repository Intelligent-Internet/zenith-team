//go:build integration

package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"
	"golang.org/x/sync/errgroup"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestCanvasFilePermissionIsIndependent(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	s := newCanvasServices(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM project_grants WHERE project_id=$1 AND user_id=$2 AND permission NOT IN ('project.read','project.files.manage')`,
		f.ProjectA,
		f.UserA,
	)
	var file uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status) VALUES($1,$2,gen_random_uuid(),'research.txt','text/plain',4,'ready') RETURNING id`, f.CompanyA, f.UserA).
		Scan(&file); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "add_artifact", AttachmentID: &file, Title: "Research"},
	); err != nil {
		t.Fatal(err)
	}
	for _, attachment := range []*uuid.UUID{nil, &file} {
		_, err := s.Mutate(
			ctx,
			p,
			f.ProjectA,
			Command{
				RequestKey:   uuid.New(),
				Type:         "frame",
				AttachmentID: attachment,
				Title:        "Unauthorized frame",
			},
		)
		want := auth.ErrProjectPermissionRequired
		if attachment != nil {
			want = ErrInvalid
		}
		if !errors.Is(err, want) {
			t.Fatalf("frame = %v, want %v", err, want)
		}
	}
	snapshot, err := s.Snapshot(ctx, p, f.ProjectA, 0)
	if err != nil || snapshot.CanEdit || !snapshot.CanManageFiles {
		t.Fatalf("files-only snapshot = %+v, %v", snapshot, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM project_grants WHERE project_id=$1 AND permission='project.files.manage'`,
		f.ProjectA,
	)
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserA, "project.tasks.write")
	snapshot, err = s.Snapshot(ctx, p, f.ProjectA, 0)
	if err != nil || !snapshot.CanEdit || snapshot.CanManageFiles {
		t.Fatalf("tasks-only snapshot = %+v, %v", snapshot, err)
	}
	if _, err = s.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "add_artifact", AttachmentID: &file},
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Fatalf("upload = %v", err)
	}
}

func TestCanvasTaskArtifactCreation(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	service := newCanvasServices(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	command := Command{
		RequestKey: uuid.New(), Type: "create_document", TaskID: &f.TaskA,
		Title: "Task notes", Body: "# Notes\nCreated in the task canvas.",
	}
	first, err := service.Mutate(ctx, p, f.ProjectA, command)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Mutate(ctx, p, f.ProjectA, command)
	if err != nil || replay != first {
		t.Fatalf("task creation replay = %+v, %v; want %+v", replay, err, first)
	}
	got, err := service.Snapshot(ctx, p, f.ProjectA, 0)
	if err != nil || len(got.Items) != 1 || len(got.Artifacts) != 1 ||
		got.Artifacts[0].TaskID == nil || *got.Artifacts[0].TaskID != f.TaskA {
		t.Fatalf("task canvas after creation = %+v, %v; want one artifact linked to %s", got, err, f.TaskA)
	}
	artifact := got.Artifacts[0]
	if _, err := service.Mutate(ctx, p, f.ProjectA, Command{
		RequestKey: uuid.New(), Type: "checkpoint", ArtifactID: &artifact.ID, VersionID: &first.ID,
		WikiPageID: artifact.LatestVersion.WikiPageID, ExpectedRevision: artifact.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := service.Snapshot(ctx, p, f.ProjectA, first.Sequence)
	if err != nil || len(updated.Artifacts) != 1 || updated.Artifacts[0].LatestVersion.Number != 2 ||
		updated.Artifacts[0].TaskID == nil || *updated.Artifacts[0].TaskID != f.TaskA {
		t.Fatalf(
			"task association after a new version = %+v, %v; want Task %s on version 2",
			updated,
			err,
			f.TaskA,
		)
	}
	command.RequestKey = uuid.New()
	command.TaskID = &f.TaskB
	if _, err := service.Mutate(ctx, p, f.ProjectA, command); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-company task = %v; want ErrNotFound", err)
	}
	var otherProject uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects(company_id,key,title) VALUES($1,'other-canvas','Other') RETURNING id`, f.CompanyA).
		Scan(&otherProject); err != nil {
		t.Fatal(err)
	}
	command.TaskID = &f.TaskA
	if _, err := service.Mutate(ctx, p, otherProject, command); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project task = %v; want ErrNotFound", err)
	}
}

// One product flow covers the durable contract: replay, conflict, discussion,
// exact version acceptance, and tenant isolation. Broader UI tests are deferred.
func TestCanvasReviewFlow(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var user, companyID, project uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,display_name) VALUES ('canvas@test.invalid','Reviewer') RETURNING id`).
		Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug,task_prefix) VALUES ('Canvas','canvas','CV') RETURNING id`).
		Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO company_members(company_id,user_id,role) VALUES ($1,$2,'owner')`,
		companyID,
		user,
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects(company_id,key,title) VALUES ($1,'launch','Launch') RETURNING id`, companyID).
		Scan(&project); err != nil {
		t.Fatal(err)
	}
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	var notifications []Changed
	var notificationBeforeCommit bool
	var documentNotifications atomic.Int32
	if err := bus.Subscribe(Topic, func(ctx context.Context, event events.Event) error {
		if event.Name == EventDocumentChanged {
			change, err := events.PayloadOf[DocumentChanged](event)
			if err != nil {
				return err
			}
			var committed bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wiki_canvas_drafts WHERE page_id=$1 AND head_seq >= $2)`, change.PageID, change.Sequence).
				Scan(&committed); err != nil {
				return err
			}
			if !committed {
				t.Error("document notification published before commit")
			}
			documentNotifications.Add(1)
			return nil
		}
		change, ok := event.Payload.(Changed)
		if !ok {
			t.Errorf("canvas payload = %T, want Changed", event.Payload)
			return nil
		}
		var committed bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_canvas_operations WHERE project_id=$1 AND seq=$2)`, change.ProjectID, change.Sequence).
			Scan(&committed)
		notificationBeforeCommit = notificationBeforeCommit || err != nil || !committed
		notifications = append(notifications, change)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	service := newCanvasServices(store, bus, nil)
	dbtest.GrantProject(
		t,
		ctx,
		pool,
		companyID,
		project,
		user,
		auth.ProjectPermissionStrings(auth.ProjectPermissions())...)
	p := auth.UserPrincipal{CompanyID: companyID, UserID: user, Role: auth.CompanyRoleOwner}
	command := Command{
		RequestKey: uuid.New(),
		Type:       "create_document",
		Title:      "Launch brief",
		Body:       "# Launch\nOur first direction.",
		X:          40,
		Y:          60,
	}
	first, err := service.Mutate(ctx, p, project, command)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Mutate(ctx, p, project, command)
	if err != nil || replay != first {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, first)
	}
	if notificationBeforeCommit || len(notifications) != 1 || notifications[0].Sequence != first.Sequence {
		t.Fatalf(
			"notifications = %+v, before commit = %v; want one committed change",
			notifications,
			notificationBeforeCommit,
		)
	}
	snapshot, err := service.Snapshot(ctx, p, project, 0)
	if err != nil || len(snapshot.Items) != 1 || len(snapshot.Artifacts) != 1 {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	item := snapshot.Items[0]
	if snapshot.Artifacts[0].ReviewState != "needs_review" || snapshot.Artifacts[0].TaskID != nil {
		t.Fatalf("new project document projection = %+v", snapshot.Artifacts[0])
	}
	_, err = service.Mutate(
		ctx,
		p,
		project,
		Command{
			RequestKey: uuid.New(),
			Type:       "move",
			Moves: []Move{
				{
					ID:               item.ID,
					ExpectedRevision: item.GeometryRevision,
					X:                100,
					Y:                200,
					Width:            item.Width,
					Height:           item.Height,
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Mutate(
		ctx,
		p,
		project,
		Command{
			RequestKey: uuid.New(),
			Type:       "move",
			Moves: []Move{
				{
					ID:               item.ID,
					ExpectedRevision: item.GeometryRevision,
					X:                0,
					Y:                0,
					Width:            item.Width,
					Height:           item.Height,
				},
			},
		},
	)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale move = %v", err)
	}
	moved, err := service.Snapshot(ctx, p, project, snapshot.Sequence)
	if err != nil || moved.Full || len(moved.Items) != 1 || len(moved.Artifacts) != 0 ||
		len(moved.ReviewVersionIDs) != 0 {
		t.Fatalf("layout delta = %+v, %v; want geometry only", moved, err)
	}
	if len(notifications) != 2 {
		t.Fatalf("notifications after rejected move = %d, want 2", len(notifications))
	}
	version := snapshot.Artifacts[0].LatestVersionID
	root, err := service.Mutate(
		ctx,
		p,
		project,
		Command{
			RequestKey: uuid.New(),
			Type:       "comment",
			VersionID:  &version,
			Body:       "Make the outcome clearer.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Mutate(
		ctx,
		p,
		project,
		Command{RequestKey: uuid.New(), Type: "reply", ThreadID: &root.ID, Body: "Agreed."},
	)
	if err != nil {
		t.Fatal(err)
	}
	artifact := snapshot.Artifacts[0]
	discussion, err := service.Threads(ctx, p, project, version)
	if err != nil || len(discussion) != 1 || len(discussion[0].Comments) != 2 {
		t.Fatalf("discussion = %+v, %v", discussion, err)
	}
	// Catch up after several missed notifications without losing the exact review
	// version, and avoid invalidating it again when the cursor is current.
	recovered, err := service.Snapshot(ctx, p, project, moved.Sequence)
	if err != nil || recovered.Full || !slices.Equal(recovered.ReviewVersionIDs, []uuid.UUID{version}) ||
		len(recovered.Artifacts) != 1 {
		t.Fatalf("recovered delta = %+v, %v; want only the changed review version/artifact", recovered, err)
	}
	quiet, err := service.Snapshot(ctx, p, project, recovered.Sequence)
	if err != nil || quiet.Full || len(quiet.ReviewVersionIDs) != 0 || len(quiet.Artifacts) != 0 ||
		len(quiet.Items) != 0 {
		t.Fatalf("current cursor = %+v, %v; want empty delta", quiet, err)
	}
	other := p
	other.CompanyID = uuid.New()
	if _, err = service.Snapshot(ctx, other, project, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign read = %v", err)
	}

	// Two independently edited snapshots converge after concurrent persistence,
	// and reopening the service recovers the acknowledged text.
	draft, err := service.Document(ctx, p, project, version, -1)
	if err != nil {
		t.Fatal(err)
	}
	if draft.SchemaVersion != 1 {
		t.Fatalf("new document uses schema %d, want native blocks", draft.SchemaVersion)
	}
	if _, err = pool.Exec(
		ctx,
		`UPDATE wiki_canvas_drafts SET schema_version=0 WHERE page_id=$1`,
		draft.PageID,
	); err == nil {
		t.Fatal("fresh schema accepted a legacy Markdown draft")
	}
	updates := make([][]byte, 2)
	for i, word := range []string{"First editor. ", "Second editor. "} {
		client := crdt.New()
		fragment := client.GetXmlFragment("content")
		if err = crdt.ApplyUpdateV1(client, draft.State, nil); err != nil {
			t.Fatal(err)
		}
		clientText := fragment.Children()[0].(*crdt.YXmlElement).Children()[0].(*crdt.YXmlText)
		vector := client.StateVector()
		client.Transact(func(tx *crdt.Transaction) { clientText.Insert(tx, 0, word, nil) })
		updates[i] = crdt.EncodeStateAsUpdateV1(client, vector)
		client.Destroy()
	}
	var edits errgroup.Group
	for _, update := range updates {
		edits.Go(func() error {
			_, err := service.SyncDocument(
				ctx,
				p,
				project,
				version,
				DocumentSyncInput{Update: update, BaseSequence: draft.Sequence},
			)
			return err
		})
	}
	if err = edits.Wait(); err != nil {
		t.Fatal(err)
	}
	if documentNotifications.Load() != 2 {
		t.Fatalf("document notifications = %d, want 2", documentNotifications.Load())
	}
	reopened, err := newCanvasServices(store, bus, nil).Document(ctx, p, project, version, -1)
	if err != nil || !strings.Contains(reopened.Markdown, "First editor.") ||
		!strings.Contains(reopened.Markdown, "Second editor.") {
		t.Fatalf("reopened draft = %q, %v", reopened.Markdown, err)
	}
	quietDocument, err := service.Document(ctx, p, project, version, reopened.Sequence)
	if err != nil || len(quietDocument.State) != 0 || len(quietDocument.Content) != 0 {
		t.Fatalf("unchanged document = %+v, %v", quietDocument, err)
	}
	projection, err := service.ReadDocument(
		ctx,
		p,
		project,
		DocumentReadInput{VersionID: version, Since: -1, Projection: true, Preview: true},
	)
	if err != nil || len(projection.State) != 0 || len(projection.Content) == 0 {
		t.Fatalf("projection = %+v, %v", projection, err)
	}
	// Unused reservations cannot turn an otherwise small page permanently read-only.
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO wiki_block_ids(company_id,page_id) SELECT $1,$2 FROM generate_series(1,100000)`,
		companyID,
		draft.PageID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReserveBlockIDs(ctx, p, project, version, 1); err != nil {
		t.Fatalf("unused reservation quota = %v", err)
	}
	current, err := service.Snapshot(ctx, p, project, 0)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := service.Mutate(
		ctx,
		p,
		project,
		Command{
			RequestKey:       uuid.New(),
			Type:             "checkpoint",
			ArtifactID:       &artifact.ID,
			VersionID:        &version,
			ExpectedRevision: current.Artifacts[0].Revision,
			WikiPageID:       &reopened.PageID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := service.Snapshot(ctx, p, project, 0)
	if err != nil || latest.Artifacts[0].LatestVersionID != checkpoint.ID ||
		latest.Artifacts[0].ReviewState != "needs_review" {
		t.Fatalf("new version must need review: %+v, %v", latest, err)
	}
	body, err := service.VersionBody(ctx, p, project, checkpoint.ID)
	if err != nil || body != reopened.Markdown {
		t.Fatalf("checkpoint body = %q, %v", body, err)
	}
	old, err := service.VersionBody(ctx, p, project, version)
	if err != nil || old != command.Body {
		t.Fatalf("old version changed = %q, %v", old, err)
	}
	// Project checkpoints remain Project work; Company Wiki publication has its own boundary.
	var pageVersion int64
	if err = pool.QueryRow(ctx, `SELECT version FROM wiki_pages WHERE id=$1`, reopened.PageID).
		Scan(&pageVersion); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []uuid.UUID{checkpoint.ID, version} {
		_, err = service.Mutate(
			ctx,
			p,
			project,
			Command{
				RequestKey:       uuid.New(),
				Type:             "publish_document",
				VersionID:        &selected,
				ExpectedRevision: pageVersion,
			},
		)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("removed Canvas publication command=%v", err)
		}
	}
	// Human uploads retain human provenance and are pinned by immutable versions.
	var upload uuid.UUID
	if err = pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status) VALUES($1,$2,gen_random_uuid(),'design.png','image/png',128,'ready') RETURNING id`, companyID, user).
		Scan(&upload); err != nil {
		t.Fatal(err)
	}
	file, err := service.Mutate(
		ctx,
		p,
		project,
		Command{RequestKey: uuid.New(), Type: "add_artifact", AttachmentID: &upload, Title: "Design"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var human bool
	if err = pool.QueryRow(ctx, `SELECT av.source_type='human' AND av.source_run_id IS NULL AND av.created_by_user_id=$2 FROM artifact_snapshots v JOIN asset_versions av ON av.id=v.asset_version_id WHERE v.id=$1`, file.ID, user).
		Scan(&human); err != nil ||
		!human {
		t.Fatalf("upload provenance: %v, %v", human, err)
	}
	var assetVersion uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT asset_version_id FROM artifact_snapshots WHERE id=$1`, file.ID).
		Scan(&assetVersion); err != nil {
		t.Fatal(err)
	}
	anchor, _ := json.Marshal(map[string]any{
		"type":          "media",
		"schemaVersion": 1,
		"surface":       "image",
		"target":        map[string]any{"assetVersionId": assetVersion},
		"shapes": []any{
			map[string]any{
				"type":   "freehand",
				"color":  "amber",
				"points": [][]float64{{0.1, 0.2}, {0.3, 0.4}},
			},
		},
	})
	mediaComment := Command{
		RequestKey: uuid.New(),
		Type:       "comment",
		VersionID:  &file.ID,
		Body:       "Change this area",
		Anchor:     anchor,
	}
	marked, err := service.Mutate(ctx, p, project, mediaComment)
	if err != nil {
		t.Fatal(err)
	}
	markedAgain, err := service.Mutate(ctx, p, project, mediaComment)
	if err != nil || markedAgain != marked {
		t.Fatalf("annotation replay = %v, %v; want %v", markedAgain, err, marked)
	}
	rows, err := service.Threads(ctx, p, project, file.ID)
	if err != nil || len(rows) != 1 || len(rows[0].Comments) != 1 {
		t.Fatalf("annotation read = %v, %v", rows, err)
	}
	var got, want any
	_ = json.Unmarshal(rows[0].Anchor, &got)
	_ = json.Unmarshal(anchor, &want)
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("anchor = %s, want %s", gotJSON, wantJSON)
	}
	for _, tc := range []struct {
		name    string
		version uuid.UUID
		body    string
		anchor  []byte
		want    error
	}{
		{"wrong version", version, "Invalid", anchor, ErrNotFound},
		{"wrong file", file.ID, "Invalid", []byte(strings.ReplaceAll(string(anchor), assetVersion.String(), uuid.NewString())), ErrNotFound},
		{"wrong output item", file.ID, "Invalid", []byte(strings.Replace(string(anchor), `"target":{`, `"target":{"outputItemId":"`+uuid.NewString()+`",`, 1)), ErrNotFound},
		{"wrong media", file.ID, "Invalid", []byte(strings.Replace(string(anchor), `"surface":"image"`, `"surface":"pdf","pageIndex":0`, 1)), ErrInvalid},
		{"atomic failure", file.ID, "", anchor, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, e := service.Mutate(
				ctx,
				p,
				project,
				Command{
					RequestKey: uuid.New(),
					Type:       "comment",
					VersionID:  &tc.version,
					Body:       tc.body,
					Anchor:     tc.anchor,
				},
			)
			if !errors.Is(e, tc.want) {
				t.Fatalf("error = %v, want %v", e, tc.want)
			}
		})
	}
	rows, err = service.Threads(ctx, p, project, file.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("failed annotation left a thread: %d, %v", len(rows), err)
	}
	unchanged, err := service.Document(ctx, p, project, version, reopened.Sequence)
	if err != nil || len(unchanged.State) != 0 {
		t.Fatalf("unchanged draft read returned state: %v", err)
	}
	var runs int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE company_id=$1`, companyID).
		Scan(&runs); err != nil ||
		runs != 0 {
		t.Fatalf("comments created Runs: %d, %v", runs, err)
	}
	// Historical receipts are durable data, so a pre-upgrade payload or a gap
	// must recover with a snapshot rather than silently advancing past changes.
	if _, err = pool.Exec(
		ctx,
		`UPDATE project_canvas_operations SET changes=changes-'reviewVersionIds' WHERE project_id=$1 AND seq=$2`,
		project,
		root.Sequence,
	); err != nil {
		t.Fatal(err)
	}
	legacy, err := service.Snapshot(ctx, p, project, moved.Sequence)
	if err != nil || !legacy.Full || len(legacy.Artifacts) != 2 {
		t.Fatalf("older receipt recovery = %+v, %v; want full snapshot", legacy, err)
	}
	if _, err = pool.Exec(
		ctx,
		`DELETE FROM project_canvas_operations WHERE project_id=$1 AND seq=$2`,
		project,
		marked.Sequence,
	); err != nil {
		t.Fatal(err)
	}
	gap, err := service.Snapshot(ctx, p, project, marked.Sequence-1)
	if err != nil || !gap.Full || gap.Sequence != marked.Sequence {
		t.Fatalf("gap recovery = %+v, %v; want full snapshot at head", gap, err)
	}
	// One end-to-end extension covers independent alternatives, database block
	// identities, templates and placement recovery without expanding the matrix.
	var currentArtifact Artifact
	for _, row := range gap.Artifacts {
		if row.ID == artifact.ID {
			currentArtifact = row
		}
	}
	branch, err := service.Mutate(
		ctx,
		p,
		project,
		Command{
			RequestKey:       uuid.New(),
			Type:             "branch_document",
			ArtifactID:       &artifact.ID,
			VersionID:        &checkpoint.ID,
			ExpectedRevision: currentArtifact.Revision,
			Title:            "Alternative",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	alternative, err := service.Document(ctx, p, project, branch.ID, -1)
	if err != nil || alternative.PageID == draft.PageID {
		t.Fatalf("independent branch: %v", err)
	}
	ids, err := service.ReserveBlockIDs(ctx, p, project, branch.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	client := crdt.New()
	fragment := client.GetXmlFragment("content")
	if err = crdt.ApplyUpdateV1(client, alternative.State, nil); err != nil {
		t.Fatal(err)
	}
	client.Transact(func(tx *crdt.Transaction) {
		node := crdt.NewYXmlElement("paragraph")
		fragment.InsertElement(tx, fragment.Len(), node)
		node.SetAttribute(tx, "blockId", ids[0].String())
		text := crdt.NewYXmlText()
		node.InsertText(tx, 0, text)
		text.Insert(tx, 0, "Only the alternative", nil)
	})
	_, err = service.SyncDocument(
		ctx,
		p,
		project,
		branch.ID,
		DocumentSyncInput{Update: client.EncodeStateAsUpdate(), BaseSequence: alternative.Sequence},
	)
	client.Destroy()
	if err != nil {
		t.Fatal(err)
	}
	main, err := service.Document(ctx, p, project, version, -1)
	if err != nil || strings.Contains(main.Markdown, "Only the alternative") {
		t.Fatalf("branch leaked into main: %v", err)
	}
	hits, err := service.Search(ctx, p, project, "Only the alternative")
	if err != nil || len(hits) != 1 || hits[0].BlockID != ids[0].String() {
		t.Fatalf("block search: %+v, %v", hits, err)
	}
	config, err := json.Marshal(
		TemplateContent{
			Items: []TemplateItem{
				{
					Type:           "artifact",
					VersionID:      &checkpoint.ID,
					Title:          "Pinned checkpoint",
					Representation: "preview",
					ViewConfig:     json.RawMessage(`{}`),
					Width:          400,
					Height:         300,
				},
			},
			Edges: []TemplateEdge{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	template, err := service.Mutate(
		ctx,
		p,
		project,
		Command{RequestKey: uuid.New(), Type: "save_template", Title: "Review", ViewConfig: config},
	)
	if err != nil {
		t.Fatal(err)
	}
	placed, err := service.Mutate(
		ctx,
		p,
		project,
		Command{RequestKey: uuid.New(), Type: "use_template", ID: &template.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	scene, err := service.Snapshot(ctx, p, project, 0)
	if err != nil {
		t.Fatal(err)
	}
	var placement Item
	for _, row := range scene.Items {
		if row.ID == placed.ID {
			placement = row
		}
	}
	if placement.Version == nil || placement.Version.ID != checkpoint.ID {
		t.Fatal("template changed its pinned source")
	}
	if _, err = service.Mutate(
		ctx,
		p,
		project,
		Command{
			RequestKey:       uuid.New(),
			Type:             "remove",
			ID:               &placed.ID,
			ExpectedRevision: placement.ContentRevision,
		},
	); err != nil {
		t.Fatal(err)
	}
	library, err := service.Library(ctx, p, project)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range library.Removed {
		if row.ID == placed.ID {
			_, err = service.Mutate(
				ctx,
				p,
				project,
				Command{
					RequestKey:       uuid.New(),
					Type:             "restore_item",
					ID:               &row.ID,
					ExpectedRevision: row.Revision,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCanvasReviewChangesRespectSourceAccess(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	); err != nil {
		t.Fatal(err)
	}
	var output uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Private','Private review','canvas-sync',$3,'private', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserA).
		Scan(&output); err != nil {
		t.Fatal(err)
	}
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	service := newCanvasServices(store, bus, nil)
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserB, "project.read")
	reader := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	version, err := service.Mutate(ctx, owner, f.ProjectA, Command{
		RequestKey: uuid.New(), Type: "add_artifact", OutputID: &output, Title: "Private",
	})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := service.Mutate(ctx, owner, f.ProjectA, Command{
		RequestKey: uuid.New(), Type: "comment", VersionID: &version.ID, Body: "Private comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("Project editing does not grant private snapshot review", func(t *testing.T) {
		dbtest.GrantProject(t, ctx, pool, f.CompanyA, f.ProjectA, f.UserB, "project.tasks.write")
		_, err := service.Mutate(ctx, reader, f.ProjectA, Command{
			RequestKey: uuid.New(), Type: "reply", ThreadID: &comment.ID, Body: "Unauthorized reply",
		})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("private snapshot reply = %v, want %v", err, ErrNotFound)
		}
	})
	for _, tc := range []struct {
		name      string
		principal auth.UserPrincipal
		want      []uuid.UUID
	}{
		{"owner", owner, []uuid.UUID{version.ID}},
		{"other member", reader, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := service.Snapshot(ctx, tc.principal, f.ProjectA, version.Sequence)
			if err != nil || got.Full || got.Sequence != comment.Sequence ||
				!slices.Equal(got.ReviewVersionIDs, tc.want) ||
				len(got.Artifacts) != len(tc.want) {
				t.Fatalf("review delta = %+v, %v; want version IDs %v", got, err, tc.want)
			}
		})
	}
	visible, err := service.Snapshot(ctx, owner, f.ProjectA, 0)
	if err != nil || len(visible.Artifacts) != 1 || visible.Artifacts[0].TaskID == nil ||
		*visible.Artifacts[0].TaskID != f.TaskA {
		t.Fatalf("task artifact projection = %+v, %v", visible, err)
	}
	a := visible.Artifacts[0]
	// A single arrange operation restores an unplaced artifact and replay is safe.
	for _, item := range visible.Items {
		_, err = service.Mutate(
			ctx,
			owner,
			f.ProjectA,
			Command{
				RequestKey:       uuid.New(),
				Type:             "remove",
				ID:               &item.ID,
				ExpectedRevision: item.ContentRevision,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	arrange := Command{RequestKey: uuid.New(), Type: "place_all"}
	placed, err := service.Mutate(ctx, owner, f.ProjectA, arrange)
	if err != nil {
		t.Fatal(err)
	}
	again, err := service.Mutate(ctx, owner, f.ProjectA, arrange)
	if err != nil || placed != again {
		t.Fatalf("arrange replay = %+v, %v", again, err)
	}
	arrange.RequestKey = uuid.New()
	if _, err = service.Mutate(ctx, owner, f.ProjectA, arrange); err != nil {
		t.Fatal(err)
	}
	arranged, err := service.Snapshot(ctx, owner, f.ProjectA, 0)
	if err != nil || len(arranged.Items) != 1 {
		t.Fatalf("duplicate placements: %+v, %v", arranged, err)
	}
	// Tied timestamps must not skip or repeat events across seek pages.
	_, err = pool.Exec(
		ctx,
		`INSERT INTO artifact_review_decisions(company_id,project_id,artifact_id,version_id,action,feedback,actor_user_id,created_at)
	SELECT $1,$2,$3,$4,'request_changes','Review',$5,now() FROM generate_series(1,55)`,
		f.CompanyA,
		f.ProjectA,
		a.ID,
		version.ID,
		f.UserA,
	)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Timeline(ctx, owner, f.ProjectA, "")
	if err != nil || len(page.Items) != 50 || page.NextCursor == nil {
		t.Fatalf("timeline page = %+v, %v", page, err)
	}
	next, err := service.Timeline(ctx, owner, f.ProjectA, *page.NextCursor)
	if err != nil || len(next.Items) != 6 || next.NextCursor != nil {
		t.Fatalf("timeline continuation = %+v, %v", next, err)
	}
	seen := make(map[uuid.UUID]bool)
	for _, event := range append(page.Items, next.Items...) {
		if seen[event.ID] {
			t.Fatalf("duplicate timeline event %v", event.ID)
		}
		seen[event.ID] = true
	}
	hidden, err := service.Timeline(ctx, reader, f.ProjectA, "")
	if err != nil || len(hidden.Items) != 0 || hidden.NextCursor != nil {
		t.Fatalf("private timeline leaked: %+v, %v", hidden, err)
	}
}

func TestCanvasResultStatusFollowsDeliverableReview(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	var output uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO outputs(company_id,run_id,title,summary,presentation_fingerprint,owner_user_id,visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1,$2,'Result','Findings','overview-result',$3,'private', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`, f.CompanyA, f.RunA, f.UserA).
		Scan(&output); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO deliverables(company_id,output_id,submitted_at) VALUES($1,$2,now())`,
		f.CompanyA,
		output,
	)
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	service := newCanvasServices(store, bus, nil)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	if _, err := service.Mutate(
		ctx,
		p,
		f.ProjectA,
		Command{RequestKey: uuid.New(), Type: "add_artifact", OutputID: &output, Title: "Result"},
	); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"ready_for_review", "approved", "changes_requested", "superseded"} {
		dbtest.MustExec(t, ctx, pool, `UPDATE deliverables SET status=$2 WHERE output_id=$1`, output, status)
		snapshot, err := service.Snapshot(ctx, p, f.ProjectA, 0)
		if err != nil || len(snapshot.Artifacts) != 1 {
			t.Fatalf("snapshot = %+v, %v", snapshot, err)
		}
		got := snapshot.Artifacts[0].ResultStatus
		if got == nil || *got != status {
			t.Fatalf("result status = %v, want %q", got, status)
		}
		if snapshot.Artifacts[0].ReviewState != "needs_review" {
			t.Fatal("Task result review changed independent Canvas acceptance")
		}
	}
}
