package daemon

import (
	"context"
	"encoding/json"
	"time"

	"boardly.dev/boardly/server/pkg/protocol"
)

func (d *Daemon) startTools(ctx context.Context) func() {
	if d.tools == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	d.tools.authorizeLogin = d.authorizeToolLogin
	stopSetup := d.tools.start(ctx)
	runtimeChanges, unsubscribe := d.runtimeSet.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		published := make(map[string]string)
		for {
			refresh := false
			select {
			case <-ctx.Done():
				return
			case <-runtimeChanges:
				// Registration replaces metadata, so an unchanged observation
				// must be published again to the newly registered projection.
				clear(published)
				d.cancelRemovedToolLogins(ctx)
				refresh = true
			case <-ticker.C:
				d.cancelRemovedToolLogins(ctx)
				refresh = true
			case <-d.tools.changed:
			}
			for _, id := range d.allRuntimeIDs() {
				runtime := d.findRuntime(id)
				if runtime == nil {
					continue
				}
				bindings, bindingErr := d.toolBindings(ctx, *runtime)
				if bindingErr != nil {
					continue
				}
				items := d.tools.observations(runtime.CompanyID, "")
				if refresh {
					for _, release := range reviewedToolReleases() {
						if release.HomeEnv == "" {
							_ = d.tools.schedule(
								toolJob{Key: release.Key, Action: "status"},
							)
						}
					}
				}
				for _, binding := range bindings {
					if binding.Runtime.ID != runtime.ID || !validToolAgentID(binding.AgentID) {
						continue
					}
					scoped := d.tools.observations(runtime.CompanyID, binding.AgentID)
					for _, item := range scoped {
						if item.AgentID == "" {
							continue
						}
						items = append(items, item)
						if refresh ||
							(item.State == "installed" && item.AuthState == "unknown") {
							_ = d.tools.schedule(
								toolJob{
									CompanyID: runtime.CompanyID,
									AgentID:   binding.AgentID,
									Key:       item.Key,
									Action:    "status",
								},
							)
						}
					}
				}
				encoded, err := json.Marshal(items)
				if err != nil || published[id] == string(encoded) {
					continue
				}
				publishCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				err = d.client.postJSON(publishCtx, "/api/v1/daemon/runtimes/"+id+"/tools",
					struct {
						Items []protocol.ToolObservation `json:"items"`
					}{Items: items}, nil)
				stop()
				if err == nil {
					published[id] = string(encoded)
				}
			}
		}
	}()
	return func() { cancel(); unsubscribe(); stopSetup(); <-done }
}

func (d *Daemon) authorizeToolLogin(ctx context.Context, scope toolAuthScope) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, id := range d.allRuntimeIDs() {
		runtime := d.findRuntime(id)
		if runtime != nil && runtime.CompanyID == scope.CompanyID && d.hasToolRuntimeAccess(ctx, *runtime) &&
			d.hasToolAgentAccess(ctx, *runtime, scope.AgentID) {
			return true
		}
	}
	return false
}

func (d *Daemon) cancelRemovedToolLogins(ctx context.Context) {
	d.tools.mu.Lock()
	var pending []toolJob
	for job := range d.tools.pending {
		if isToolLogin(job.Action) {
			pending = append(pending, job)
		}
	}
	d.tools.mu.Unlock()
	for _, job := range pending {
		if !d.authorizeToolLogin(ctx, job.scope()) {
			d.tools.cancelLogin(job.scope(), job.Key)
		}
	}
}
