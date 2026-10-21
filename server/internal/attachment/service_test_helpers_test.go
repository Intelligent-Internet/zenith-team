package attachment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/storage"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var attachmentTestNow = time.Date(2026, time.July, 12, 13, 0, 0, 0, time.UTC)

type storedAttachmentObject struct {
	content     []byte
	contentType string
}

type attachmentObjectStorageFake struct {
	mu         sync.Mutex
	objects    map[string]storedAttachmentObject
	calls      int
	putErr     error
	openErr    error
	deleteErr  error
	presignErr error
	presignKey string
	presignTTL time.Duration
	openBody   io.ReadCloser
	openNil    bool
	putCtx     func(context.Context)
	// openHook runs on every GetReader, which is where DefaultAttachmentService
	// verifies stored content between authorizing a record and opening its
	// lifecycle transaction. Tests use it to serialize a concurrent change
	// into that gap.
	openHook func()
}

func newAttachmentObjectStorageFake() *attachmentObjectStorageFake {
	return &attachmentObjectStorageFake{objects: make(map[string]storedAttachmentObject)}
}

func (o *attachmentObjectStorageFake) Upload(
	ctx context.Context,
	key string,
	source io.Reader,
	sizeBytes int64,
	contentType string,
	_ string,
) error {
	if o.putCtx != nil {
		o.putCtx(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	o.calls++
	if o.putErr != nil {
		err := o.putErr
		o.mu.Unlock()
		return err
	}
	if _, exists := o.objects[key]; exists {
		o.mu.Unlock()
		return storage.ErrAlreadyExists
	}
	o.mu.Unlock()
	content, err := io.ReadAll(source)
	if err != nil {
		return err
	}
	if int64(len(content)) != sizeBytes {
		return storage.ErrSizeMismatch
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.objects[key]; exists {
		return storage.ErrAlreadyExists
	}
	o.objects[key] = storedAttachmentObject{
		content:     append([]byte(nil), content...),
		contentType: contentType,
	}
	return nil
}

func (o *attachmentObjectStorageFake) GetReader(_ context.Context, key string) (io.ReadCloser, error) {
	if o.openHook != nil {
		o.openHook()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.openErr != nil {
		return o.openBody, o.openErr
	}
	if o.openNil {
		return nil, nil
	}
	if o.openBody != nil {
		return o.openBody, nil
	}
	object, ok := o.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), object.content...))), nil
}

func (o *attachmentObjectStorageFake) DeleteObject(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.deleteErr != nil {
		return o.deleteErr
	}
	delete(o.objects, key)
	return nil
}

func (o *attachmentObjectStorageFake) DeleteKeys(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if err := o.DeleteObject(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (o *attachmentObjectStorageFake) PresignGet(
	_ context.Context,
	key string,
	ttl time.Duration,
) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	o.presignKey = key
	o.presignTTL = ttl
	if o.presignErr != nil {
		return "", o.presignErr
	}
	return "https://objects.test/signed", nil
}

func (*attachmentObjectStorageFake) PresignGetWithContentDisposition(
	context.Context,
	string,
	time.Duration,
	string,
) (string, error) {
	return "https://objects.test/signed", nil
}

func (o *attachmentObjectStorageFake) mustObject(t *testing.T, key string) storedAttachmentObject {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	object, ok := o.objects[key]
	if !ok {
		t.Fatalf("object %q not found", key)
	}
	object.content = append([]byte(nil), object.content...)
	return object
}

func (o *attachmentObjectStorageFake) callCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls
}

func (o *attachmentObjectStorageFake) presignCall() (string, time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.presignKey, o.presignTTL
}

func (o *attachmentObjectStorageFake) replaceObject(key string, content []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	object := o.objects[key]
	object.content = append([]byte(nil), content...)
	o.objects[key] = object
}

func (o *attachmentObjectStorageFake) removeObject(key string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.objects, key)
}

func (o *attachmentObjectStorageFake) hasObject(key string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.objects[key]
	return ok
}

func (o *attachmentObjectStorageFake) setDeleteErr(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.deleteErr = err
}

type failingReadCloser struct {
	readErr  error
	closeErr error
	closed   bool
}

func (r *failingReadCloser) Read([]byte) (int, error) { return 0, r.readErr }

func (r *failingReadCloser) Close() error {
	r.closed = true
	return r.closeErr
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(destination []byte) (int, error) { return f(destination) }

func validAttachmentInitiateInput(payload []byte) InitiateInput {
	return InitiateInput{
		RequestKey: attachmentTestRequestKey, CompanyID: attachmentTestCompanyID,
		UploadedByUserID: attachmentTestUserID,
		Filename:         "design brief.txt", ContentType: "text/plain", SizeBytes: int64(len(payload)),
	}
}

// attachmentInitiateInputFor returns a fresh-request input bound to a seeded
// company fixture, so tests that share one PostgreSQL database never collide.
func attachmentInitiateInputFor(fixture attachmentAuthorityFixture, payload []byte) InitiateInput {
	input := validAttachmentInitiateInput(payload)
	input.RequestKey, input.CompanyID, input.UploadedByUserID = uuid.Must(
		uuid.NewV7(),
	), fixture.companyID, fixture.ownerID
	return input
}

// The lifecycle mutations below are the exact transaction boundaries
// DefaultAttachmentService opens; white-box tests drive the same package
// functions through them rather than reaching for a seam on the service.

func initiatePendingForTest(
	ctx context.Context,
	store *db.Store,
	create pendingAttachmentCreate,
) (pendingAttachmentResult, error) {
	var result pendingAttachmentResult
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		var txErr error
		result, txErr = initiatePendingAttachment(ctx, tx, create)
		return txErr
	})
	if err != nil {
		return pendingAttachmentResult{}, err
	}
	return result, nil
}

// completePendingForTest returns the events the transaction staged so tests
// can assert the post-commit publish set; a failed transaction stages nothing.
func completePendingForTest(
	ctx context.Context,
	store *db.Store,
	completion attachmentCompletion,
) (attachmentCompletionResult, []events.Event, error) {
	var result attachmentCompletionResult
	var event []events.Event
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		event = event[:0]
		var txErr error
		result, txErr = completePendingAttachment(ctx, tx, &event, completion)
		return txErr
	})
	if err != nil {
		return attachmentCompletionResult{}, nil, err
	}
	return result, event, nil
}

func requestDeletionForTest(
	ctx context.Context,
	store *db.Store,
	request attachmentDeletionRequest,
) (attachmentRecord, error) {
	var result attachmentRecord
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		var txErr error
		result, txErr = requestAttachmentDeletion(ctx, tx, request)
		return txErr
	})
	if err != nil {
		return attachmentRecord{}, err
	}
	return result, nil
}

// A single isolated PostgreSQL database backs every Attachment test whose state is
// scoped to its own Company and Attachment identifiers. Tests that mutate the
// cleanup sweeps operate on isolated databases through startAttachmentPostgres.
var (
	sharedAttachmentOnce     sync.Once
	sharedAttachmentDatabase *dbtest.Database
	sharedAttachmentPool     *pgxpool.Pool
	sharedAttachmentStore    *db.Store
	sharedAttachmentErr      error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedAttachmentDatabase != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := sharedAttachmentDatabase.Close(cleanupCtx); err != nil {
			fmt.Fprintf(os.Stderr, "close shared Attachment test database: %v\n", err)
			code = 1
		}
		cleanupCancel()
	}
	os.Exit(code)
}

func sharedAttachmentPostgres(t *testing.T) (*pgxpool.Pool, *db.Store) {
	t.Helper()
	sharedAttachmentOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		database, err := dbtest.OpenDatabase(ctx)
		if err != nil {
			sharedAttachmentErr = err
			return
		}
		sharedAttachmentDatabase = database
		pool := database.Pool
		if err := db.MigrateUp(ctx, pool); err != nil {
			sharedAttachmentErr = err
			return
		}
		store, err := db.NewStore(pool)
		if err != nil {
			sharedAttachmentErr = err
			return
		}
		sharedAttachmentPool, sharedAttachmentStore = pool, store
	})
	if sharedAttachmentErr != nil {
		if errors.Is(sharedAttachmentErr, dbtest.ErrSharedDatabaseUnavailable) &&
			os.Getenv("CI") == "" && os.Getenv("TEST_REQUIRE_DOCKER") != "1" {
			t.Skipf("shared Docker-backed PostgreSQL unavailable locally: %v", sharedAttachmentErr)
		}
		t.Fatalf("prepare shared Attachment test database: %v", sharedAttachmentErr)
	}
	return sharedAttachmentPool, sharedAttachmentStore
}

// newSharedAttachmentStack seeds a private Company fixture on the shared
// database and returns a DefaultAttachmentService wired to an in-memory
// object store.
func newSharedAttachmentStack(t *testing.T) (
	*pgxpool.Pool,
	*db.Store,
	attachmentAuthorityFixture,
	*DefaultAttachmentService,
	*attachmentObjectStorageFake,
	*attachmentEventLog,
) {
	t.Helper()
	pool, store := sharedAttachmentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAttachmentAuthority(t, ctx, pool)
	service, objects, eventLog := newAttachmentCleanupStack(t, store)
	return pool, store, fixture, service, objects, eventLog
}

// completedEvent and deletedEvent are one recorded attachment.* event with
// its payload already re-typed. The envelope on the bus carries an untyped
// payload, so the subscription types it once and every assertion reads it
// typed.
type completedEvent struct {
	Name    string
	Actor   events.Actor
	Payload Response
}

type deletedEvent struct {
	Name    string
	Actor   events.Actor
	Payload Removal
}

// attachmentEventLog collects what the Attachment topic publishes post-commit,
// replacing the deleted realtime_outbox table as the assertion surface for
// service-driven flows. Each wire name is recorded as itself, so a payload
// arrives typed and a test reads the routing facts straight off it.
type attachmentEventLog struct {
	mu        sync.Mutex
	completed []completedEvent
	deleted   []deletedEvent
}

func (l *attachmentEventLog) completedSnapshot() []completedEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]completedEvent(nil), l.completed...)
}

func (l *attachmentEventLog) deletedSnapshot() []deletedEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]deletedEvent(nil), l.deleted...)
}

func attachmentTestBus(t *testing.T) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct test bus: %v", err)
	}
	return bus
}

func createdAttachmentBus(t *testing.T) (*events.Bus, *attachmentEventLog) {
	t.Helper()
	bus := attachmentTestBus(t)
	return bus, recordAttachmentTopics(t, bus)
}

func recordAttachmentTopics(t *testing.T, bus *events.Bus) *attachmentEventLog {
	t.Helper()
	log := &attachmentEventLog{}
	if err := bus.Subscribe(Topic,
		func(_ context.Context, event events.Event) error {
			switch event.Name {
			case EventCompleted:
				payload, err := events.PayloadOf[Response](event)
				if err != nil {
					return err
				}
				log.mu.Lock()
				defer log.mu.Unlock()
				log.completed = append(log.completed, completedEvent{
					Name: event.Name, Actor: event.Actor, Payload: payload,
				})
			case EventDeleted:
				payload, err := events.PayloadOf[Removal](event)
				if err != nil {
					return err
				}
				log.mu.Lock()
				defer log.mu.Unlock()
				log.deleted = append(log.deleted, deletedEvent{
					Name: event.Name, Actor: event.Actor, Payload: payload,
				})
			}
			return nil
		}); err != nil {
		t.Fatalf("subscribe to the attachment topic: %v", err)
	}
	return log
}

// publishStagedAttachmentEvents replays what a transaction event onto a
// throwaway recorded bus. Staged is opaque, so what a subscriber receives is
// the only surface a test can read.
func publishStagedAttachmentEvents(
	t *testing.T,
	event []events.Event,
) *attachmentEventLog {
	t.Helper()
	bus := attachmentTestBus(t)
	log := recordAttachmentTopics(t, bus)
	for _, group := range events.GroupByTopic(event) {
		bus.Publish(context.Background(), group.Topic, group.Events...)
	}
	return log
}
