package task

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	proactiveReportPollInterval = 5 * time.Second
	proactiveReportDrainLimit   = 100
)

// ReportDispatcher periodically drains eligible proactive report targets.
// Polling is the durable correctness path; event wakes are only an optimization.
type ReportDispatcher struct {
	service ReportService
	logger  *slog.Logger
	wake    chan struct{}
}

func NewReportDispatcher(service ReportService, logger *slog.Logger) (*ReportDispatcher, error) {
	if service == nil {
		return nil, errors.New("Task report dispatcher service is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ReportDispatcher{service: service, logger: logger, wake: make(chan struct{}, 1)}, nil
}

func (d *ReportDispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *ReportDispatcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(proactiveReportPollInterval)
	defer ticker.Stop()
	for {
		d.drain(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-d.wake:
		}
	}
}

func (d *ReportDispatcher) drain(ctx context.Context) {
	for range proactiveReportDrainLimit {
		if ctx.Err() != nil {
			return
		}
		delivered, err := d.service.DispatchProactiveOnce(ctx)
		if err != nil {
			d.logger.WarnContext(
				ctx,
				"Proactive Task report dispatch failed",
				"component", "task-report-dispatcher",
				"error", err,
			)
			return
		}
		if !delivered {
			return
		}
	}
}
