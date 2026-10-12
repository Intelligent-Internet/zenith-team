package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// lifecycleServer is the http.Server surface serveApplication drives. It is an
// interface so shutdown ordering can be tested without binding a port.
type lifecycleServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
	Close() error
}

// drainer flips readiness to failing so a load balancer stops sending new
// requests before the server refuses them. Satisfied by db.Readiness.
type drainer interface {
	StartDraining()
}

// serveApplication runs the HTTP server and the background workers as one
// unit, and returns every error they produced.
//
// Two orderings are load-bearing. A worker that stops on its own cancels
// serving, so a dead sweeper or relay takes the process down instead of
// leaving it serving traffic it cannot fulfill. And workers keep running until
// HTTP has fully drained, so an in-flight request never loses the database,
// Redis, or hub dependency underneath it mid-response.
func serveApplication(
	ctx context.Context,
	server lifecycleServer,
	ready drainer,
	shutdownTimeout time.Duration,
	workers ...backgroundWorker,
) error {
	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	// Workers survive the serve context so shutdown can drain HTTP first;
	// stopWorkers below is what actually ends them.
	workerCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	workerErrors := make(chan error, len(workers))
	var workersDone sync.WaitGroup

	for _, worker := range workers {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			if err := runWorker(workerCtx, worker); err != nil {
				workerErrors <- err
				stopServing()
			}
		}()
	}

	var serveErr error
	if ctx.Err() == nil {
		serveErr = serveHTTP(serveCtx, server, ready, shutdownTimeout)
	}

	stopWorkers()
	workersDone.Wait()
	close(workerErrors)
	collected := make([]error, 0, len(workers))
	for err := range workerErrors {
		collected = append(collected, err)
	}
	return errors.Join(serveErr, errors.Join(collected...))
}

// runWorker reports the failure that should take the process down, or nil
// when the worker stopped because shutdown asked it to.
func runWorker(ctx context.Context, worker backgroundWorker) error {
	if worker.run == nil {
		return fmt.Errorf("background worker %q has no run function", worker.name)
	}
	err := worker.run(ctx)
	if ctx.Err() != nil && (err == nil || errors.Is(err, ctx.Err())) {
		return nil
	}
	if err == nil {
		// A worker returning nil before shutdown is still a failure: nothing
		// is supervising the work it was meant to keep doing.
		err = errors.New("stopped unexpectedly")
	}
	return fmt.Errorf("background worker %q: %w", worker.name, err)
}

func serveHTTP(ctx context.Context, server lifecycleServer, ready drainer, shutdownTimeout time.Duration) error {
	if server == nil {
		return errors.New("HTTP server is required")
	}
	if ready == nil {
		return errors.New("readiness drainer is required")
	}
	if shutdownTimeout <= 0 {
		return errors.New("shutdown timeout must be positive")
	}
	if ctx.Err() != nil {
		return nil
	}

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErrors:
		return unexpectedServeError(err)
	case <-ctx.Done():
	}

	ready.StartDraining()
	// Shutdown gets its own context: the one that just fired is already done.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	var closeErr error
	if shutdownErr != nil {
		closeErr = server.Close()
	}
	return errors.Join(
		wrapLifecycleError("gracefully shut down HTTP server", shutdownErr),
		wrapLifecycleError("force close HTTP server", closeErr),
		unexpectedServeError(<-serveErrors),
	)
}

func unexpectedServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve HTTP: %w", err)
}

func wrapLifecycleError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
