package daemon

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/repocache"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/workspace"
)

// MigrateWorkspaceRunIdentities is the offline maintenance entry point. The CLI
// holds the daemon's health port for its duration to prevent execution starting.
func MigrateWorkspaceRunIdentities(
	ctx context.Context,
	root, daemonID string,
	mappings map[string]uuid.UUID,
	apply bool,
) ([]workspace.RunIdentityChange, error) {
	manager, err := workspace.NewManager(
		root,
		root+"-repos",
		workspaceMetadataKey(daemonID),
		repocache.OSRunner{},
		workspace.Retention{Completed: 24 * time.Hour, Orphaned: 72 * time.Hour},
		nil,
	)
	if err != nil {
		return nil, err
	}
	return manager.MigrateRunIdentities(ctx, mappings, apply)
}
