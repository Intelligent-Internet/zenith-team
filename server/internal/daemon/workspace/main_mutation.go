package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	mainMutationLockFileName = "main-mutation.lock"
	mainMutationLockPoll     = 25 * time.Millisecond
)

type mainMutationGate struct {
	token chan struct{}
}

type advisoryFileLock struct {
	file *os.File
}

var mainMutationGates sync.Map

func sharedMainMutationGate(lockPath string) *mainMutationGate {
	gate := &mainMutationGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	actual, _ := mainMutationGates.LoadOrStore(filepath.Clean(lockPath), gate)
	// Only this function ever writes the map, so the stored value is always a
	// *mainMutationGate; the comma-ok form keeps a surprise from panicking the
	// daemon and falls back to the gate we just built.
	shared, ok := actual.(*mainMutationGate)
	if !ok {
		return gate
	}
	return shared
}

func (m *Manager) withMainMutation(
	ctx context.Context,
	mutate func(context.Context) error,
) (returnErr error) {
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-m.mainMutation.token:
	}
	defer func() {
		m.mainMutation.token <- struct{}{}
	}()

	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if err := m.ensureWritableRoots(); err != nil {
		return err
	}
	lock, err := acquireAdvisoryFileLock(ctx, m.mainMutationLockPath)
	if err != nil {
		return err
	}
	defer func() {
		releaseErr := lock.release()
		// Closing the descriptor releases the OS lock even when the explicit
		// unlock reports an error. Do not turn a completed Git mutation into an
		// unreportable failure after its authoritative callback already returned.
		if returnErr != nil {
			returnErr = errors.Join(returnErr, releaseErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	return mutate(ctx)
}

func acquireAdvisoryFileLock(ctx context.Context, path string) (*advisoryFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open main mutation lock: %w", err)
	}
	for {
		locked, lockErr := tryAdvisoryFileLock(file)
		if lockErr != nil {
			return nil, errors.Join(
				fmt.Errorf("acquire main mutation lock: %w", lockErr),
				file.Close(),
			)
		}
		if locked {
			return &advisoryFileLock{file: file}, nil
		}

		timer := time.NewTimer(mainMutationLockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, errors.Join(context.Cause(ctx), file.Close())
		case <-timer.C:
		}
	}
}

func (l *advisoryFileLock) release() error {
	return errors.Join(unlockAdvisoryFile(l.file), l.file.Close())
}
