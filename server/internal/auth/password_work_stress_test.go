package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Cancelling a caller cannot interrupt Argon2. Its slot must remain occupied
// until the password manager returns, even as mixed public routes contend for
// that same budget. Repeated rounds also prove that slots recover after work
// finishes rather than gradually leaking under contention.
func TestPasswordWorkStressCancellationOverloadAndRecovery(t *testing.T) {
	passwords := &passwordStressManager{}
	service := storelessService(t, ServiceOptions{Passwords: passwords})
	service.passwordReset = &passwordResetManager{}
	calls := passwordWorkCalls(service)
	names := []string{"login", "signup", "reset"}

	for round := range 12 {
		if !t.Run(names[round%len(names)], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			passwords.entered = make(chan struct{}, 64)
			passwords.release = make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(passwords.release) }) }
			defer release()
			completed := make(chan error, 4)
			for worker := range 4 {
				name := names[(round+worker)%len(names)]
				go func() { completed <- calls[name](ctx) }()
			}
			for range 4 {
				select {
				case <-passwords.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("admitted password work did not start")
				}
			}

			cancel()
			// Half the calls are already cancelled; all remaining calls must
			// be refused immediately while the original four are still busy.
			results := make(chan error, 48)
			var burst sync.WaitGroup
			for worker := range 48 {
				burst.Go(func() {
					requestCtx := t.Context()
					if worker%2 == 0 {
						requestCtx = ctx
					}
					err := calls[names[worker%len(names)]](requestCtx)
					want := ErrPasswordWorkBusy
					if worker%2 == 0 {
						want = context.Canceled
					}
					if !errors.Is(err, want) {
						results <- err
					}
				})
			}
			done := make(chan struct{})
			go func() { burst.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("overloaded requests queued behind cancelled running work")
			}
			close(results)
			for err := range results {
				t.Errorf("mixed burst returned unexpected error: %v", err)
			}
			if got := passwords.active.Load(); got != 4 {
				t.Errorf("running work after caller cancellation = %d, want 4", got)
			}
			if got := passwords.peak.Load(); got != 4 {
				t.Errorf("peak password work = %d, want 4", got)
			}

			release()
			for range 4 {
				select {
				case err := <-completed:
					if !errors.Is(err, ErrInvalidCredentials) &&
						!errors.Is(err, errPasswordWorkTestHash) {
						t.Errorf("admitted work returned unexpected error: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("admitted work did not finish after release")
				}
			}
			if got := passwords.active.Load(); got != 0 {
				t.Errorf("running work after completion = %d, want zero", got)
			}
		}) {
			return
		}
	}
	if got := passwords.workCalls.Load(); got != 48 {
		t.Fatalf("password operations started = %d, want 48 admitted operations", got)
	}
}

type passwordStressManager struct {
	initialized atomic.Bool
	active      atomic.Int32
	peak        atomic.Int32
	workCalls   atomic.Int32
	entered     chan struct{}
	release     chan struct{}
}

func (p *passwordStressManager) Hash(string) (string, error) {
	if !p.initialized.Swap(true) {
		return "startup dummy hash", nil
	}
	p.work()
	return "", errPasswordWorkTestHash
}

func (p *passwordStressManager) Verify(string, string) bool {
	p.work()
	return false
}

func (p *passwordStressManager) work() {
	p.workCalls.Add(1)
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for peak := p.peak.Load(); active > peak; peak = p.peak.Load() {
		if p.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	p.entered <- struct{}{}
	<-p.release
}
