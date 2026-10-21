package main

import (
	"context"
	"log/slog"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// connectorRunInvalidator is the composition-level reaction from committed
// Connector capability changes to Run lifecycle transitions. The Connector
// domain decides which frozen scopes became invalid; execution alone owns the
// cancellation write and its events/queue promotion.
type connectorRunInvalidator struct {
	queries *generated.Queries
	runs    execution.RunService
	logger  *slog.Logger
}

func (i connectorRunInvalidator) InvalidateConnectorRuns(
	ctx context.Context,
	change agent.ConnectorRunChange,
) {
	runIDs, err := agent.ListRunIDsWithInvalidConnectorSnapshots(ctx, i.queries, change)
	if err != nil {
		i.log().Warn(
			"resolve Connector-invalidated Runs failed",
			"component", "connector-run-invalidation",
			"error", err,
		)
		return
	}
	for _, runID := range runIDs {
		if _, err := i.runs.CancelRun(
			ctx,
			runID,
			execution.CancellationConnectorUnavailable,
		); err != nil {
			i.log().Warn(
				"cancel Connector-invalidated Run failed",
				"component", "connector-run-invalidation",
				"run_id", runID,
				"error", err,
			)
		}
	}
}

func (i connectorRunInvalidator) log() *slog.Logger {
	if i.logger != nil {
		return i.logger
	}
	return slog.Default()
}

var _ agent.ConnectorRunInvalidator = connectorRunInvalidator{}
