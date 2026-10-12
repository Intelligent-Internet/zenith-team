package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer stands in for *http.Server: ListenAndServe blocks until Shutdown
// or Close releases it, so the test drives the real serve/shutdown ordering
// without binding a port.
type fakeServer struct {
	mu           sync.Mutex
	released     chan struct{}
	releaseOnce  sync.Once
	serving      chan struct{}
	servingOnce  sync.Once
	shutdownErr  error
	shutdownSeen bool
	closeSeen    bool
}

func newFakeServer() *fakeServer {
	return &fakeServer{released: make(chan struct{}), serving: make(chan struct{})}
}

func (s *fakeServer) ListenAndServe() error {
	s.servingOnce.Do(func() { close(s.serving) })
	<-s.released
	return http.ErrServerClosed
}

// waitUntilServing blocks until ListenAndServe has been entered. Cancelling
// before that point is a different path — serveApplication skips serving
// entirely — so a shutdown-ordering test must not race it.
func (s *fakeServer) waitUntilServing(t *testing.T) {
	t.Helper()
	select {
	case <-s.serving:
	case <-time.After(5 * time.Second):
		t.Fatal("server never started serving")
	}
}

func (s *fakeServer) Shutdown(context.Context) error {
	s.mu.Lock()
	s.shutdownSeen = true
	err := s.shutdownErr
	s.mu.Unlock()
	s.release()
	return err
}

func (s *fakeServer) Close() error {
	s.mu.Lock()
	s.closeSeen = true
	s.mu.Unlock()
	s.release()
	return nil
}

func (s *fakeServer) release() { s.releaseOnce.Do(func() { close(s.released) }) }

func (s *fakeServer) sawShutdown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdownSeen
}

func (s *fakeServer) sawClose() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeSeen
}

type fakeDrainer struct {
	mu      sync.Mutex
	drained bool
}

func (d *fakeDrainer) StartDraining() {
	d.mu.Lock()
	d.drained = true
	d.mu.Unlock()
}

func (d *fakeDrainer) hasDrained() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.drained
}

// Cancelling the process context must drain readiness, shut the server down,
// and stop every worker — with no error, because that is an ordinary exit.
func TestServeApplicationShutsDownCleanlyOnContextCancel(t *testing.T) {
	server, ready := newFakeServer(), &fakeDrainer{}
	ctx, cancel := context.WithCancel(context.Background())

	var stopped sync.WaitGroup
	stopped.Add(1)
	worker := backgroundWorker{name: "sweeper", run: func(ctx context.Context) error {
		defer stopped.Done()
		<-ctx.Done()
		return ctx.Err()
	}}

	done := make(chan error, 1)
	go func() { done <- serveApplication(ctx, server, ready, time.Second, worker) }()

	server.waitUntilServing(t)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveApplication() error = %v, want nil on an ordinary shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveApplication did not return after the context was cancelled")
	}
	stopped.Wait()

	if !ready.hasDrained() {
		t.Error("readiness was never drained; a load balancer would keep sending traffic into shutdown")
	}
	if !server.sawShutdown() {
		t.Error("server was never gracefully shut down")
	}
	if server.sawClose() {
		t.Error("server was force-closed even though graceful shutdown succeeded")
	}
}

// A worker that stops on its own must take the process down: continuing to
// serve traffic without the sweeper or the relay is worse than exiting.
func TestServeApplicationStopsServingWhenAWorkerFails(t *testing.T) {
	server, ready := newFakeServer(), &fakeDrainer{}
	failing := backgroundWorker{name: "relay", run: func(context.Context) error {
		return errors.New("relay lost its connection")
	}}

	err := serveApplication(context.Background(), server, ready, time.Second, failing)
	if err == nil {
		t.Fatal("serveApplication() error = nil, want the worker failure")
	}
	if !strings.Contains(err.Error(), "relay") || !strings.Contains(err.Error(), "lost its connection") {
		t.Fatalf("serveApplication() error = %v, want it to name the failing worker and cause", err)
	}
	if !server.sawShutdown() {
		t.Error("a failed worker did not shut the HTTP server down")
	}
}

// A worker returning nil before shutdown is still a failure — nothing is
// supervising the work it was supposed to keep doing.
func TestServeApplicationTreatsAnEarlyWorkerReturnAsFailure(t *testing.T) {
	server, ready := newFakeServer(), &fakeDrainer{}
	quitter := backgroundWorker{name: "test-worker", run: func(context.Context) error { return nil }}

	err := serveApplication(context.Background(), server, ready, time.Second, quitter)
	if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("serveApplication() error = %v, want an unexpected-stop failure", err)
	}
}

// Workers must outlive the HTTP drain: an in-flight request may still be
// using the database, Redis, or the hub while Shutdown waits for it.
func TestServeApplicationStopsWorkersOnlyAfterHTTPDrains(t *testing.T) {
	server, ready := newFakeServer(), &fakeDrainer{}
	ctx, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	var workerStoppedAfterShutdown bool
	worker := backgroundWorker{name: "attachment-cleanup", run: func(workerCtx context.Context) error {
		<-workerCtx.Done()
		mu.Lock()
		workerStoppedAfterShutdown = server.sawShutdown()
		mu.Unlock()
		return workerCtx.Err()
	}}

	done := make(chan error, 1)
	go func() { done <- serveApplication(ctx, server, ready, time.Second, worker) }()
	server.waitUntilServing(t)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveApplication() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !workerStoppedAfterShutdown {
		t.Error("worker context ended before HTTP finished draining")
	}
}

// A worker with no run function is a wiring bug in the composition root, and
// must surface by name rather than being silently skipped.
func TestServeApplicationRejectsAWorkerWithoutARunFunction(t *testing.T) {
	server, ready := newFakeServer(), &fakeDrainer{}

	err := serveApplication(context.Background(), server, ready, time.Second,
		backgroundWorker{name: "misconfigured"})
	if err == nil || !strings.Contains(err.Error(), "misconfigured") {
		t.Fatalf("serveApplication() error = %v, want the unwired worker named", err)
	}
}

// When graceful shutdown fails the server must still be force-closed, and
// both failures reported.
func TestServeHTTPForceClosesWhenGracefulShutdownFails(t *testing.T) {
	server, ready := newFakeServer(), &fakeDrainer{}
	server.shutdownErr = errors.New("shutdown deadline exceeded")

	serveCtx, stopServing := context.WithCancel(context.Background())
	go func() {
		server.waitUntilServing(t)
		stopServing()
	}()
	err := serveHTTP(serveCtx, server, ready, time.Second)

	if err == nil || !strings.Contains(err.Error(), "gracefully shut down HTTP server") {
		t.Fatalf("serveHTTP() error = %v, want the graceful-shutdown failure reported", err)
	}
	if !server.sawClose() {
		t.Error("server was not force-closed after graceful shutdown failed")
	}
}

func TestServeHTTPRejectsMissingDependencies(t *testing.T) {
	for name, test := range map[string]struct {
		server  lifecycleServer
		ready   drainer
		timeout time.Duration
	}{
		"no server":        {nil, &fakeDrainer{}, time.Second},
		"no drainer":       {newFakeServer(), nil, time.Second},
		"zero timeout":     {newFakeServer(), &fakeDrainer{}, 0},
		"negative timeout": {newFakeServer(), &fakeDrainer{}, -time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if err := serveHTTP(context.Background(), test.server, test.ready, test.timeout); err == nil {
				t.Fatal("serveHTTP() error = nil, want a dependency failure")
			}
		})
	}
}
