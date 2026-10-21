package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemonws"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type daemonTranscriptRunServiceStub struct {
	execution.RunService
	appendTranscriptItem func(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		execution.TranscriptItemInput,
	) (execution.TranscriptItemReceipt, error)
}

func (s *daemonTranscriptRunServiceStub) AppendTranscriptItem(
	ctx context.Context,
	runtimeID uuid.UUID,
	runID uuid.UUID,
	input execution.TranscriptItemInput,
) (execution.TranscriptItemReceipt, error) {
	return s.appendTranscriptItem(ctx, runtimeID, runID, input)
}

type daemonTranscriptDBTX struct {
	run      db.Run
	rowErr   error
	rowCalls int
}

func (*daemonTranscriptDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("unexpected Exec")
}

func (*daemonTranscriptDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query")
}

func (d *daemonTranscriptDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	d.rowCalls++
	return daemonTranscriptRow{run: d.run, err: d.rowErr}
}

type daemonTranscriptRow struct {
	run db.Run
	err error
}

// The handler reads three fields off GetRun, and this stub fills them by
// position in the runs table's column order. Naming the positions instead of
// spelling the numbers is what makes the next column addition legible here:
// a column added or dropped in the middle shifts every later position, which
// showed up once as "run not found" rather than as an index.
const (
	runScanID = iota
	runScanCompanyID
	_ // agent_id
	_ // task_id
	_ // conversation_id
	runScanRuntimeID
)

func (r daemonTranscriptRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	*destinations[runScanID].(*uuid.UUID) = r.run.ID
	*destinations[runScanCompanyID].(*uuid.UUID) = r.run.CompanyID
	*destinations[runScanRuntimeID].(*uuid.UUID) = r.run.RuntimeID
	return nil
}

func TestDaemonWSRunTranscriptAppendCommitsOneAuthenticatedRuntimeItem(t *testing.T) {
	t.Parallel()

	runID := uuid.MustParse("00000000-0000-4000-8000-000000004711")
	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2802")
	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2803")
	messageID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2804")
	committedAt := time.Date(2026, time.August, 21, 3, 4, 5, 600_000_000, time.FixedZone("test", 7*60*60))
	itemDigest := sha256.Sum256([]byte("item"))
	database := &daemonTranscriptDBTX{run: db.Run{
		ID: runID, CompanyID: companyID, RuntimeID: runtimeID,
	}}
	var (
		calls           int
		gotRuntimeID    uuid.UUID
		gotRunID        uuid.UUID
		gotTranscriptIn execution.TranscriptItemInput
	)
	service := &daemonTranscriptRunServiceStub{
		appendTranscriptItem: func(
			_ context.Context,
			runtimeID uuid.UUID,
			runID uuid.UUID,
			input execution.TranscriptItemInput,
		) (execution.TranscriptItemReceipt, error) {
			calls++
			gotRuntimeID = runtimeID
			gotRunID = runID
			gotTranscriptIn = input
			return execution.TranscriptItemReceipt{
				ID:          messageID,
				RunID:       runID,
				ItemKey:     input.ItemKey,
				Seq:         4,
				ThroughSeq:  7,
				ItemDigest:  itemDigest,
				CommittedAt: committedAt,
			}, nil
		},
	}
	h := &Handler{Queries: db.New(database), RunService: service}
	body := json.RawMessage(`{
		"runId":"00000000-0000-4000-8000-000000004711",
		"itemKey":"provider-item-4",
		"role":"agent",
		"parts":[{"type":"text","text":"done"}]
	}`)
	identity := daemonws.ClientIdentity{
		CompanyIDs: []string{companyID.String()},
		RuntimeIDs: []string{
			"019fa834-a515-79b2-b1bd-19e6bd4f2899",
			runtimeID.String(),
		},
	}

	status, responseBody, err := h.DaemonWSRunTranscriptAppend(
		context.Background(), identity, protocol.DaemonRPCMethodRunTranscriptAppend, body,
	)
	if err != nil {
		t.Fatalf("DaemonWSRunTranscriptAppend() error = %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if database.rowCalls != 1 {
		t.Fatalf("GetRun calls = %d, want 1", database.rowCalls)
	}
	if calls != 1 {
		t.Fatalf("AppendTranscriptItem calls = %d, want 1", calls)
	}
	if gotRuntimeID != runtimeID || gotRunID != runID {
		t.Fatalf(
			"AppendTranscriptItem runtime/run = %s/%d, want %s/%d",
			gotRuntimeID,
			gotRunID,
			runtimeID,
			runID,
		)
	}
	if gotTranscriptIn.ItemKey != "provider-item-4" || gotTranscriptIn.Role != protocol.RunItemRoleAgent ||
		len(gotTranscriptIn.Parts) != 1 || gotTranscriptIn.Parts[0].Text != "done" {
		t.Fatalf("AppendTranscriptItem input = %+v", gotTranscriptIn)
	}

	var got protocol.RunTranscriptAppendReceipt
	if err := json.Unmarshal(responseBody, &got); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	want := protocol.RunTranscriptAppendReceipt{
		ID:          messageID.String(),
		RunID:       runID,
		ItemKey:     "provider-item-4",
		Seq:         4,
		ThroughSeq:  7,
		ItemDigest:  "4a33eacd5fa65f2b2e2871cd131286b53c415b131666d71173bb6e3fe59361b3",
		CommittedAt: "2026-08-20T20:04:05.6Z",
	}
	if got != want {
		t.Fatalf("receipt = %+v, want %+v", got, want)
	}
}

func TestDaemonWSRunTranscriptAppendRejectsUnownedRunBeforeWrite(t *testing.T) {
	t.Parallel()

	runID := uuid.MustParse("00000000-0000-4000-8000-000000004712")
	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2812")
	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2813")
	body := json.RawMessage(
		`{"runId":"00000000-0000-4000-8000-000000004712","itemKey":"item-1","role":"agent","parts":[{"type":"text","text":"done"}]}`,
	)

	tests := []struct {
		name     string
		identity daemonws.ClientIdentity
	}{
		{
			name: "runtime is outside socket scope",
			identity: daemonws.ClientIdentity{
				CompanyIDs: []string{companyID.String()},
				RuntimeIDs: []string{"019fa834-a515-79b2-b1bd-19e6bd4f2814"},
			},
		},
		{
			name: "company is outside socket scope",
			identity: daemonws.ClientIdentity{
				CompanyIDs: []string{"019fa834-a515-79b2-b1bd-19e6bd4f2815"},
				RuntimeIDs: []string{runtimeID.String()},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := &daemonTranscriptRunServiceStub{
				appendTranscriptItem: func(
					context.Context,
					uuid.UUID,
					uuid.UUID,
					execution.TranscriptItemInput,
				) (execution.TranscriptItemReceipt, error) {
					t.Fatal("AppendTranscriptItem must not run for an unowned Run")
					return execution.TranscriptItemReceipt{}, nil
				},
			}
			h := &Handler{
				Queries: db.New(&daemonTranscriptDBTX{run: db.Run{
					ID: runID, CompanyID: companyID, RuntimeID: runtimeID,
				}}),
				RunService: service,
			}

			status, responseBody, err := h.DaemonWSRunTranscriptAppend(
				context.Background(), test.identity, protocol.DaemonRPCMethodRunTranscriptAppend, body,
			)
			if status != http.StatusNotFound || err == nil || len(responseBody) != 0 {
				t.Fatalf(
					"status/body/error = %d/%s/%v, want 404/empty/error",
					status,
					responseBody,
					err,
				)
			}
		})
	}
}

func TestDaemonWSRunTranscriptAppendUsesStrictRequestAndDomainErrorMapping(t *testing.T) {
	t.Parallel()

	runID := uuid.MustParse("00000000-0000-4000-8000-000000004713")
	companyID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2822")
	runtimeID := uuid.MustParse("019fa834-a515-79b2-b1bd-19e6bd4f2823")
	validBody := `{"runId":"00000000-0000-4000-8000-000000004713","itemKey":"item-1","role":"agent","parts":[{"type":"text","text":"done"}]}`
	tests := []struct {
		name       string
		body       string
		serviceErr error
		wantStatus int
		wantRows   int
		wantCalls  int
	}{
		{
			name:       "snake case alias",
			body:       validBody[:len(validBody)-1] + `,"run_id":4713}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			// runId is a number on the wire now; the uuid a stale daemon sends
			// is not one, so the binder refuses the frame before any lookup.
			name:       "run id is still numeric",
			body:       `{"runId":4713,"itemKey":"item-1","role":"agent","parts":[]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			// Well-formed JSON naming an id the bigserial cannot have issued.
			name:       "nil run UUID",
			body:       `{"runId":"00000000-0000-0000-0000-000000000000","itemKey":"item-1","role":"agent","parts":[]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid normalized item",
			body:       validBody,
			serviceErr: fmt.Errorf("append transcript: %w", execution.ErrTranscriptItemInvalid),
			wantStatus: http.StatusUnprocessableEntity,
			wantRows:   1,
			wantCalls:  1,
		},
		{
			name:       "item key conflict",
			body:       validBody,
			serviceErr: execution.ErrTranscriptItemConflict,
			wantStatus: http.StatusConflict,
			wantRows:   1,
			wantCalls:  1,
		},
		{
			name:       "run no longer writable",
			body:       validBody,
			serviceErr: execution.ErrTranscriptRunUnavailable,
			wantStatus: http.StatusNotFound,
			wantRows:   1,
			wantCalls:  1,
		},
		{
			name:       "storage failure",
			body:       validBody,
			serviceErr: errors.New("database offline"),
			wantStatus: http.StatusInternalServerError,
			wantRows:   1,
			wantCalls:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			database := &daemonTranscriptDBTX{run: db.Run{
				ID: runID, CompanyID: companyID, RuntimeID: runtimeID,
			}}
			calls := 0
			service := &daemonTranscriptRunServiceStub{
				appendTranscriptItem: func(
					context.Context,
					uuid.UUID,
					uuid.UUID,
					execution.TranscriptItemInput,
				) (execution.TranscriptItemReceipt, error) {
					calls++
					return execution.TranscriptItemReceipt{}, test.serviceErr
				},
			}
			h := &Handler{Queries: db.New(database), RunService: service}
			identity := daemonws.ClientIdentity{
				CompanyIDs: []string{companyID.String()},
				RuntimeIDs: []string{runtimeID.String()},
			}

			status, responseBody, err := h.DaemonWSRunTranscriptAppend(
				context.Background(), identity, protocol.DaemonRPCMethodRunTranscriptAppend,
				json.RawMessage(test.body),
			)
			if status != test.wantStatus || err == nil || len(responseBody) != 0 {
				t.Fatalf(
					"status/body/error = %d/%s/%v, want %d/empty/error",
					status,
					responseBody,
					err,
					test.wantStatus,
				)
			}
			if database.rowCalls != test.wantRows {
				t.Fatalf("GetRun calls = %d, want %d", database.rowCalls, test.wantRows)
			}
			if calls != test.wantCalls {
				t.Fatalf("AppendTranscriptItem calls = %d, want %d", calls, test.wantCalls)
			}
		})
	}
}

func TestDaemonWSRunTranscriptAppendMapsRunLookupFailures(t *testing.T) {
	t.Parallel()

	body := json.RawMessage(
		`{"runId":"00000000-0000-4000-8000-000000004714","itemKey":"item-1","role":"agent","parts":[{"type":"text","text":"done"}]}`,
	)
	tests := []struct {
		name       string
		lookupErr  error
		wantStatus int
	}{
		{name: "missing", lookupErr: pgx.ErrNoRows, wantStatus: http.StatusNotFound},
		{
			name:       "database failure",
			lookupErr:  errors.New("database offline"),
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := &Handler{
				Queries: db.New(&daemonTranscriptDBTX{rowErr: test.lookupErr}),
				RunService: &daemonTranscriptRunServiceStub{
					appendTranscriptItem: func(
						context.Context,
						uuid.UUID,
						uuid.UUID,
						execution.TranscriptItemInput,
					) (execution.TranscriptItemReceipt, error) {
						t.Fatal("AppendTranscriptItem must not run after a lookup failure")
						return execution.TranscriptItemReceipt{}, nil
					},
				},
			}
			status, _, err := h.DaemonWSRunTranscriptAppend(
				context.Background(), daemonws.ClientIdentity{},
				protocol.DaemonRPCMethodRunTranscriptAppend, body,
			)
			if status != test.wantStatus || err == nil {
				t.Fatalf("status/error = %d/%v, want %d/error", status, err, test.wantStatus)
			}
		})
	}
}
