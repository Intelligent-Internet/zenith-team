package handler

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemonws"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// DaemonWSRunTranscriptAppend is the daemonws RPC adapter for one completed
// provider item. The service returns only after its PostgreSQL transaction
// commits, so a successful response is the durable acknowledgement.
func (h *Handler) DaemonWSRunTranscriptAppend(
	ctx context.Context,
	identity daemonws.ClientIdentity,
	_ string,
	body json.RawMessage,
) (int, json.RawMessage, error) {
	var request protocol.RunTranscriptAppendRequest
	if err := util.BindStrictJSON(bytes.NewReader(body), &request); err != nil {
		return http.StatusBadRequest, nil, errors.New("run transcript append request is invalid")
	}
	// JSON binding parses the Run UUID; a zero identity is not a persisted Run.
	if request.RunID == uuid.Nil {
		return http.StatusBadRequest, nil, errors.New("run transcript append request is invalid")
	}

	run, err := h.Queries.GetRun(ctx, request.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return http.StatusNotFound, nil, errors.New("run not found")
	}
	if err != nil {
		slog.Warn("load run for transcript append failed", "run_id", request.RunID, "error", err)
		return http.StatusInternalServerError, nil, errors.New("run transcript append failed")
	}
	if !daemonIdentityContainsID(identity.CompanyIDs, run.CompanyID) ||
		!daemonIdentityContainsID(identity.RuntimeIDs, run.RuntimeID) {
		return http.StatusNotFound, nil, errors.New("run not found")
	}

	receipt, err := h.RunService.AppendTranscriptItem(
		ctx,
		run.RuntimeID,
		run.ID,
		execution.TranscriptItemInput{
			ItemKey: request.ItemKey,
			Role:    request.Role,
			Parts:   request.Parts,
		},
	)
	if err != nil {
		status, mappedErr := daemonTranscriptAppendError(request.RunID, err)
		return status, nil, mappedErr
	}

	response, err := json.Marshal(protocol.RunTranscriptAppendReceipt{
		ID:          receipt.ID.String(),
		RunID:       receipt.RunID,
		ItemKey:     receipt.ItemKey,
		Seq:         receipt.Seq,
		ThroughSeq:  receipt.ThroughSeq,
		ItemDigest:  hex.EncodeToString(receipt.ItemDigest[:]),
		CommittedAt: receipt.CommittedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return http.StatusInternalServerError, nil, errors.New("run transcript acknowledgement failed")
	}
	return http.StatusOK, response, nil
}

func daemonIdentityContainsID(values []string, wanted uuid.UUID) bool {
	wantedValue := wanted.String()
	for _, value := range values {
		if value == wantedValue {
			return true
		}
	}
	return false
}

func daemonTranscriptAppendError(runID uuid.UUID, err error) (int, error) {
	switch {
	case errors.Is(err, execution.ErrTranscriptItemInvalid):
		// The reply stays generic; the log names the field so a refused item
		// can be traced.
		slog.Warn("run transcript item refused", "run_id", runID, "error", err)
		return http.StatusUnprocessableEntity, errors.New("run transcript item is invalid")
	case errors.Is(err, execution.ErrTranscriptItemConflict):
		return http.StatusConflict, errors.New("run transcript item conflicts with existing item")
	case errors.Is(err, execution.ErrTranscriptRunUnavailable):
		return http.StatusNotFound, errors.New("run not found")
	default:
		slog.Warn("append run transcript item failed", "run_id", runID, "error", err)
		return http.StatusInternalServerError, errors.New("run transcript append failed")
	}
}
