package daemon

import (
	"context"
	"sync"
	"testing"
)

func TestCapabilityRefreshQueueCoalescesForcedRefresh(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	var (
		mu    sync.Mutex
		types []capabilityRefreshType
	)
	queue := newCapabilityRefreshQueue(
		func(ctx context.Context, _ string, refreshType capabilityRefreshType) error {
			mu.Lock()
			types = append(types, refreshType)
			call := len(types)
			mu.Unlock()
			switch call {
			case 1:
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
				}
			case 2:
				close(completed)
			}
			return nil
		},
	)
	stop := queue.start(t.Context(), nil)
	defer stop()
	if err := queue.schedule("codex", capabilityRefreshTypeChanged); err != nil {
		t.Fatalf("schedule initial: %v", err)
	}
	<-started
	if err := queue.schedule("codex", capabilityRefreshTypeForced); err != nil {
		t.Fatalf("schedule forced: %v", err)
	}
	close(release)
	<-completed
	mu.Lock()
	defer mu.Unlock()
	if len(types) != 2 || types[0] != capabilityRefreshTypeChanged || types[1] != capabilityRefreshTypeForced {
		t.Fatalf("types = %v, want [changed forced]", types)
	}
}

// A coalesced refresh must do at least as much as the strongest request it
// absorbed: a keepalive re-observation arriving after a user's forced refresh
// must not downgrade it, and a runtime-set change must not downgrade either.
func TestCapabilityRefreshQueueMergesToTheStrongestType(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		scheduled []capabilityRefreshType
		want      capabilityRefreshType
	}{
		{
			name:      "reobserve outranks changed",
			scheduled: []capabilityRefreshType{capabilityRefreshTypeChanged, capabilityRefreshTypeReobserve},
			want:      capabilityRefreshTypeReobserve,
		},
		{
			name:      "forced survives a later reobserve",
			scheduled: []capabilityRefreshType{capabilityRefreshTypeForced, capabilityRefreshTypeReobserve},
			want:      capabilityRefreshTypeForced,
		},
		{
			name: "forced survives a later changed",
			scheduled: []capabilityRefreshType{
				capabilityRefreshTypeReobserve, capabilityRefreshTypeForced, capabilityRefreshTypeChanged,
			},
			want: capabilityRefreshTypeForced,
		},
		{
			name:      "changed alone stays changed",
			scheduled: []capabilityRefreshType{capabilityRefreshTypeChanged, capabilityRefreshTypeChanged},
			want:      capabilityRefreshTypeChanged,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			started := make(chan struct{})
			release := make(chan struct{})
			second := make(chan capabilityRefreshType, 1)
			var calls int
			queue := newCapabilityRefreshQueue(
				func(ctx context.Context, _ string, refreshType capabilityRefreshType) error {
					calls++
					switch calls {
					case 1:
						close(started)
						select {
						case <-release:
						case <-ctx.Done():
						}
					case 2:
						second <- refreshType
					}
					return nil
				},
			)
			stop := queue.start(t.Context(), nil)
			defer stop()
			if err := queue.schedule("codex", capabilityRefreshTypeChanged); err != nil {
				t.Fatalf("schedule initial: %v", err)
			}
			<-started
			for _, refreshType := range test.scheduled {
				if err := queue.schedule("codex", refreshType); err != nil {
					t.Fatalf("schedule %v: %v", refreshType, err)
				}
			}
			close(release)
			if got := <-second; got != test.want {
				t.Fatalf("coalesced type = %v, want %v", got, test.want)
			}
		})
	}
}
