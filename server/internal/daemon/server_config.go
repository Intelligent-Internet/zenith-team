package daemon

import (
	"context"
	"fmt"

	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/protocol"
)

// Resolve once before starting local handlers, tool jobs, and runs so an origin
// change cannot move an in-flight login or run to another credential home.
func (d *Daemon) loadServerConfig(ctx context.Context) error {
	var config protocol.DaemonConfig
	if err := d.client.getJSON(ctx, "/api/v1/daemon/config", &config); err != nil {
		return fmt.Errorf("load daemon server config: %w", err)
	}
	origin, err := util.CanonicalStudioOrigin(config.StudioAPIOrigin)
	if err != nil {
		return fmt.Errorf("daemon server config studioApiOrigin: %w", err)
	}
	d.cfg.StudioAPIOrigin = origin
	d.tools.studioOrigin = origin
	return nil
}
