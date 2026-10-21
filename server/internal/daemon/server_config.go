package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// errStudioNotConfigured names the server setting an operator adds to turn
// Zenith Studio on.
var errStudioNotConfigured = errors.New(
	"Zenith Studio is not configured on this server; the operator sets ZENITH_STUDIO_API_URL to turn it on",
)

// Resolve once before starting local handlers, tool jobs, and runs so an origin
// change cannot move an in-flight login or run to another credential home.
func (d *Daemon) loadServerConfig(ctx context.Context) error {
	var config protocol.DaemonConfig
	if err := d.client.getJSON(ctx, "/api/v1/daemon/config", &config); err != nil {
		return fmt.Errorf("load daemon server config: %w", err)
	}
	// An empty origin means the server leaves Zenith Studio off.
	origin := ""
	if config.StudioAPIOrigin != "" {
		var err error
		origin, err = util.CanonicalStudioOrigin(config.StudioAPIOrigin)
		if err != nil {
			return fmt.Errorf("daemon server config studioApiOrigin: %w", err)
		}
	}
	d.cfg.StudioAPIOrigin = origin
	d.tools.studioOrigin = origin
	return nil
}
