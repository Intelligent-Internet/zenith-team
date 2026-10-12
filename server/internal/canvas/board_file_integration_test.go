//go:build integration

package canvas

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// boardMemoryObjects is object storage held in memory for Board file tests.
type boardMemoryObjects struct {
	attachment.ObjectStorage
	mu       sync.Mutex
	contents map[string][]byte
}

func (s *boardMemoryObjects) Upload(_ context.Context, key string, reader io.Reader, _ int64, _, _ string) error {
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contents[key] = body
	return nil
}

func (s *boardMemoryObjects) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return io.NopCloser(bytes.NewReader(s.contents[key])), nil
}

func boardMediaFixture(t *testing.T, pool *pgxpool.Pool) (*DefaultBoardMediaService, attachment.AttachmentService) {
	t.Helper()
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	files, err := attachment.NewAttachmentService(attachment.ServiceOptions{
		Store: store, Bus: bus, Objects: &boardMemoryObjects{contents: map[string][]byte{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewBoardMediaService(store, files), files
}

func uploadBoardTestFile(
	t *testing.T,
	files attachment.AttachmentService,
	p auth.UserPrincipal,
	name, contentType string,
	data []byte,
) uuid.UUID {
	t.Helper()
	initiated, err := files.Initiate(
		t.Context(),
		attachment.InitiateInput{
			RequestKey: uuid.New(), CompanyID: p.CompanyID, UploadedByUserID: p.UserID,
			Filename: name, ContentType: contentType, SizeBytes: int64(len(data)),
		},
		bytes.NewReader(data),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = files.Complete(
		t.Context(),
		attachment.CompleteInput{
			ID:               initiated.Attachment.ID,
			CompanyID:        p.CompanyID,
			UploadedByUserID: p.UserID,
		},
	); err != nil {
		t.Fatal(err)
	}
	return initiated.Attachment.ID
}

func boardTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func placeBoardFile(
	t *testing.T,
	boards *DefaultBoardService,
	p auth.UserPrincipal,
	board uuid.UUID,
	itemType string,
	file uuid.UUID,
) (BoardMutation, error) {
	t.Helper()
	return boards.Mutate(t.Context(), p, BoardScope{BoardID: board}, BoardBatch{
		RequestKey: uuid.New(),
		Commands: []BoardCommand{{
			Type: "create_item", DraftID: "file", ItemType: itemType,
			Geometry:   &BoardGeometry{X: 10, Y: 20, Width: 320, Height: 200},
			Properties: &BoardProperties{AttachmentID: &file},
		}},
	})
}

func TestBoardAttachFilesPreviewsAndPlacements(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	media, files := boardMediaFixture(t, pool)
	ctx := t.Context()
	board := createTestBoard(t, boards, owner, member)

	large := uploadBoardTestFile(t, files, member, "diagram.png", "image/png", boardTestPNG(t, 2048, 1024))
	attached, err := media.AttachFile(ctx, member, board.ID, BoardFileInput{AttachmentID: large})
	if err != nil {
		t.Fatal(err)
	}
	if !attached.HasPreview || attached.Width == nil || *attached.Width != 2048 || *attached.Height != 1024 {
		t.Fatalf("large image = %+v, want 2048x1024 with preview", attached)
	}
	again, err := media.AttachFile(ctx, member, board.ID, BoardFileInput{AttachmentID: large})
	if err != nil || !reflect.DeepEqual(again, attached) {
		t.Fatalf("retry = %+v, %v; want %+v", again, err, attached)
	}
	preview, err := media.FileDownload(ctx, member, BoardScope{BoardID: board.ID}, large, true, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	config, _, err := image.DecodeConfig(preview.Body)
	if err != nil || max(config.Width, config.Height) > boardPreviewSize {
		t.Fatalf("preview = %dx%d, %v; want bounded preview", config.Width, config.Height, err)
	}

	small := uploadBoardTestFile(t, files, member, "dot.png", "image/png", boardTestPNG(t, 40, 20))
	if file, err := media.AttachFile(ctx, member, board.ID, BoardFileInput{AttachmentID: small}); err != nil ||
		file.HasPreview || *file.Width != 40 {
		t.Fatalf("small image = %+v, %v; want its own preview", file, err)
	}

	pdf := uploadBoardTestFile(t, files, member, "brief.pdf", "application/pdf", []byte("%PDF-1.7 brief"))
	file, err := media.AttachFile(ctx, member, board.ID, BoardFileInput{AttachmentID: pdf})
	if err != nil || file.HasPreview || file.Width != nil || file.Filename != "brief.pdf" {
		t.Fatalf("pdf = %+v, %v; want plain file reference", file, err)
	}
	if _, err := placeBoardFile(t, boards, member, board.ID, "image", pdf); !errors.Is(err, ErrBoardInvalid) {
		t.Fatalf("pdf as image = %v, want invalid", err)
	}
	placed, err := placeBoardFile(t, boards, member, board.ID, "file", pdf)
	if err != nil {
		t.Fatal(err)
	}
	item := placed.Items[0]
	if item.File == nil || item.File.Filename != "brief.pdf" || item.File.SizeBytes != 14 {
		t.Fatalf("placed file metadata = %+v", item.File)
	}
	// Properties replace the previous set; a file card must keep its file.
	if _, err := boards.Mutate(ctx, member, BoardScope{BoardID: board.ID}, BoardBatch{
		RequestKey: uuid.New(),
		Commands: []BoardCommand{{
			Type: "content", ID: &item.ID, ExpectedRevision: item.ContentRevision,
			Properties: &BoardProperties{Title: "renamed"},
		}},
	}); !errors.Is(err, ErrBoardInvalid) {
		t.Fatalf("content without file = %v, want invalid", err)
	}
	unregistered := uploadBoardTestFile(t, files, member, "loose.pdf", "application/pdf", []byte("%PDF loose"))
	if _, err := placeBoardFile(
		t,
		boards,
		member,
		board.ID,
		"file",
		unregistered,
	); !errors.Is(
		err,
		ErrBoardNotFound,
	) {
		t.Fatalf("unregistered placement = %v, want not found", err)
	}

	corrupt := uploadBoardTestFile(t, files, member, "broken.png", "image/png", []byte("not an image"))
	if _, err := media.AttachFile(ctx, member, board.ID, BoardFileInput{AttachmentID: corrupt}); !errors.Is(
		err,
		ErrBoardInvalid,
	) {
		t.Fatalf("corrupt image = %v, want invalid", err)
	}
	foreign := uploadBoardTestFile(t, files, owner, "owner.pdf", "application/pdf", []byte("%PDF owner"))
	if _, err := media.AttachFile(ctx, member, board.ID, BoardFileInput{AttachmentID: foreign}); !errors.Is(
		err,
		ErrBoardNotFound,
	) {
		t.Fatalf("another member's upload = %v, want not found", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE board_member_access SET access='read' WHERE board_id=$1 AND user_id=$2`,
		board.ID, member.UserID)
	readOnly := uploadBoardTestFile(t, files, member, "late.pdf", "application/pdf", []byte("%PDF late"))
	if _, err := media.AttachFile(ctx, member, board.ID, BoardFileInput{AttachmentID: readOnly}); !errors.Is(
		err,
		ErrBoardForbidden,
	) {
		t.Fatalf("read-only attach = %v, want forbidden", err)
	}
}

func TestBoardFilesCopyBetweenReadableBoards(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	media, files := boardMediaFixture(t, pool)
	ctx := t.Context()
	source := createTestBoard(t, boards, member)
	target := createTestBoard(t, boards, owner, member)
	photo := uploadBoardTestFile(t, files, member, "photo.png", "image/png", boardTestPNG(t, 1600, 900))
	original, err := media.AttachFile(ctx, member, source.ID, BoardFileInput{AttachmentID: photo})
	if err != nil {
		t.Fatal(err)
	}
	// The owner cannot read the member's private source Board.
	if _, err := media.AttachFile(ctx, owner, target.ID, BoardFileInput{AttachmentID: photo}); !errors.Is(
		err,
		ErrBoardNotFound,
	) {
		t.Fatalf("copy without source access = %v, want not found", err)
	}
	copied, err := media.AttachFile(ctx, member, target.ID, BoardFileInput{AttachmentID: photo})
	if err != nil || !reflect.DeepEqual(copied, original) {
		t.Fatalf("copy = %+v, %v; want %+v", copied, err, original)
	}
	if _, err := placeBoardFile(t, boards, member, target.ID, "image", photo); err != nil {
		t.Fatal(err)
	}
	// Target readers reach the file and its preview through the target Board.
	if _, err := media.FileDownload(ctx, owner, BoardScope{BoardID: target.ID}, photo, true, uuid.Nil); err != nil {
		t.Fatalf("target reader preview: %v", err)
	}
	if _, err := media.FileDownload(ctx, owner, BoardScope{BoardID: source.ID}, photo, false, uuid.Nil); !errors.Is(
		err,
		ErrBoardNotFound,
	) {
		t.Fatalf("source download without access = %v, want not found", err)
	}
}

func TestBoardRetentionPurgesTombstonesAndReleasesFiles(t *testing.T) {
	t.Parallel()
	boards, pool, owner, member := boardFixture(t)
	media, files := boardMediaFixture(t, pool)
	ctx := t.Context()
	board := createTestBoard(t, boards, owner, member)
	kept := uploadBoardTestFile(t, files, member, "kept.pdf", "application/pdf", []byte("%PDF kept"))
	loose := uploadBoardTestFile(t, files, member, "loose.pdf", "application/pdf", []byte("%PDF loose"))
	for _, file := range []uuid.UUID{kept, loose} {
		if _, err := media.AttachFile(ctx, member, board.ID, BoardFileInput{AttachmentID: file}); err != nil {
			t.Fatal(err)
		}
	}
	placed, err := placeBoardFile(t, boards, member, board.ID, "file", kept)
	if err != nil {
		t.Fatal(err)
	}
	item := placed.Items[0]
	removed, err := boards.Mutate(ctx, member, BoardScope{BoardID: board.ID}, BoardBatch{
		RequestKey: uuid.New(),
		Commands: []BoardCommand{{
			Type: "delete_item", ID: &item.ID, ExpectedRevision: item.ContentRevision,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	before := removed.Sequence - 1

	// Within the undo horizon nothing changes.
	if sweep, err := boards.Sweep(ctx, time.Now()); err != nil || sweep != (BoardSweep{}) {
		t.Fatalf("early sweep = %+v, %v", sweep, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE board_items SET deleted_at=now()-interval '31 days' WHERE id=$1`, item.ID)
	// A registered file waits as long as the local recovery that may still
	// place it: two days on it is kept, past the retry horizon it is released.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE board_files SET created_at=now()-interval '2 days' WHERE board_id=$1`,
		board.ID,
	)
	if sweep, err := boards.Sweep(ctx, time.Now()); err != nil || sweep.ReleasedFiles != 0 || sweep.Items != 1 {
		t.Fatalf("sweep two days on = %+v, %v; want the tombstone purged and the files kept", sweep, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE board_files SET created_at=now()-interval '31 days' WHERE board_id=$1`,
		board.ID,
	)
	sweep, err := boards.Sweep(ctx, time.Now())
	if err != nil || sweep.ReleasedFiles != 2 {
		t.Fatalf("sweep = %+v, %v; want both files released", sweep, err)
	}
	// A client catching up across the purge still learns the item left.
	delta, err := boards.Snapshot(ctx, member, BoardScope{BoardID: board.ID}, before)
	if err != nil {
		t.Fatal(err)
	}
	if delta.Full || len(delta.Removed) != 1 || delta.Removed[0] != item.ID {
		t.Fatalf("delta after purge = %+v, want removed %s", delta, item.ID)
	}
	// Released files become ordinary unreferenced uploads.
	var references int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM board_files WHERE board_id=$1`, board.ID).
		Scan(&references); err != nil || references != 0 {
		t.Fatalf("remaining references = %d, %v", references, err)
	}
}

// A public download reads its authority in one short transaction and opens the
// file after it, so it never holds a connection while asking for another.
func TestBoardPublicFileDownloadNeedsOneConnection(t *testing.T) {
	t.Parallel()
	boards, pool, owner, _ := boardFixture(t)
	ctx := t.Context()
	objects := &boardMemoryObjects{contents: map[string][]byte{}}
	newMedia := func(pool *pgxpool.Pool) (*DefaultBoardMediaService, attachment.AttachmentService) {
		store, _ := db.NewStore(pool)
		bus, _ := events.NewBus(events.BusOptions{})
		files, err := attachment.NewAttachmentService(attachment.ServiceOptions{Store: store, Bus: bus, Objects: objects})
		if err != nil {
			t.Fatal(err)
		}
		return NewBoardMediaService(store, files), files
	}
	media, files := newMedia(pool)
	board := createTestBoard(t, boards, owner)
	photo := uploadBoardTestFile(t, files, owner, "photo.png", "image/png", boardTestPNG(t, 64, 64))
	if _, err := media.AttachFile(ctx, owner, board.ID, BoardFileInput{AttachmentID: photo}); err != nil {
		t.Fatal(err)
	}
	if _, err := placeBoardFile(t, boards, owner, board.ID, "image", photo); err != nil {
		t.Fatal(err)
	}
	access, err := boards.Access(ctx, owner, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = boards.SetVisibility(ctx, owner, board.ID, BoardVisibilityInput{
		ExpectedRevision: access.Revision, Visibility: "public", CompanyAccess: "read",
	}); err != nil {
		t.Fatal(err)
	}
	access, err = boards.Access(ctx, owner, board.ID)
	if err != nil || access.PublicToken == nil {
		t.Fatalf("public token = %+v, %v", access, err)
	}
	config := pool.Config()
	config.MaxConns, config.MinConns = 1, 0
	single, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	singleMedia, _ := newMedia(single)
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	file, err := singleMedia.PublicFileDownload(bounded, *access.PublicToken, photo, false)
	if err != nil {
		t.Fatalf("public download on a one-connection pool = %v", err)
	}
	if err = file.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

// Placing a file on a Board and filing the same upload into a Project would
// leave the Project file readable by the Board's audience; the Project refuses.
func TestBoardPlacedUploadCannotBecomeAProjectFile(t *testing.T) {
	t.Parallel()
	boards, pool, owner, _ := boardFixture(t)
	media, files := boardMediaFixture(t, pool)
	ctx := t.Context()
	board := createTestBoard(t, boards, owner)
	report := uploadBoardTestFile(t, files, owner, "report.pdf", "application/pdf", []byte("%PDF-1.4"))
	if _, err := media.AttachFile(ctx, owner, board.ID, BoardFileInput{AttachmentID: report}); err != nil {
		t.Fatal(err)
	}
	store, _ := db.NewStore(pool)
	bus, _ := events.NewBus(events.BusOptions{})
	projects := newCanvasServices(store, bus, files)
	var project uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects WHERE company_id=$1 ORDER BY created_at LIMIT 1`, owner.CompanyID).
		Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.Mutate(ctx, owner, project, Command{
		RequestKey: uuid.New(), Type: "add_artifact", AttachmentID: &report, Title: "Report",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("filing a Board file into a Project = %v, want ErrForbidden", err)
	}
}

// One pass drains a backlog larger than a batch, and a tombstone restored while
// the sweep waits for it is never purged.
func TestBoardRetentionDrainsBacklogsAndSparesRestoredItems(t *testing.T) {
	t.Parallel()
	boards, pool, owner, _ := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, boards, owner)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO board_items(company_id,board_id,type,x,y,width,height,created_by,updated_by,deleted_at)
		SELECT $1,$2,'shape',0,0,10,10,$3,$3,now()-interval '31 days' FROM generate_series(1,2500)`,
		owner.CompanyID, board.ID, owner.UserID)
	sweep, err := boards.Sweep(ctx, time.Now())
	if err != nil || sweep.Items != 2500 {
		t.Fatalf("sweep = %+v, %v; want the whole backlog in one pass", sweep, err)
	}

	created, err := boards.Mutate(ctx, owner, BoardScope{BoardID: board.ID}, BoardBatch{
		RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("a", "back")},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.IDs["a"]
	dbtest.MustExec(t, ctx, pool, `UPDATE board_items SET deleted_at=now()-interval '31 days' WHERE id=$1`, id)
	restore, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restore.Rollback(ctx) }()
	if _, err = restore.Exec(ctx, `UPDATE board_items SET deleted_at=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan BoardSweep, 1)
	go func() {
		result, _ := boards.Sweep(ctx, time.Now())
		done <- result
	}()
	time.Sleep(200 * time.Millisecond)
	if err = restore.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sweep did not finish")
	}
	var live bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM board_items WHERE id=$1 AND deleted_at IS NULL)`, id).
		Scan(&live); err != nil || !live {
		t.Fatalf("restored item purged = %t, %v", !live, err)
	}
}
