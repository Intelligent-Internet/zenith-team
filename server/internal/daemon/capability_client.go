package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// publishConnectorDiscovery PUTs one runtime's sanitized Connector snapshot
// to the cloud. The whole manifest replaces the stored projection, so a
// partial send is never published.
func (c *Client) publishConnectorDiscovery(
	ctx context.Context,
	companyID, runtimeID string,
	manifest connectorDiscoveryManifest,
) (discoveryPublishResult, error) {
	var result discoveryPublishResult
	path := fmt.Sprintf(
		"/api/v1/daemon/companies/%s/runtimes/%s/connectors/discovery",
		companyID,
		runtimeID,
	)
	if err := c.putJSON(ctx, path, manifest, &result); err != nil {
		return discoveryPublishResult{}, err
	}
	return result, nil
}

// reaffirmConnectorDiscovery tells the cloud that a fresh observation found
// the Runtime's current Connector generation unchanged, so only its freshness
// is renewed. A 409 (the generation or digest moved on) or a 404 (a server
// without the route) is the caller's cue to publish the whole snapshot.
func (c *Client) reaffirmConnectorDiscovery(
	ctx context.Context,
	companyID, runtimeID string,
	reaffirmation connectorDiscoveryReaffirmation,
) (discoveryPublishResult, error) {
	var result discoveryPublishResult
	path := fmt.Sprintf(
		"/api/v1/daemon/companies/%s/runtimes/%s/connectors/discovery/reaffirm",
		companyID,
		runtimeID,
	)
	if err := c.postJSON(ctx, path, reaffirmation, &result); err != nil {
		return discoveryPublishResult{}, err
	}
	return result, nil
}

// connectorReaffirmationRejected reports a reaffirmation the server refused
// on its merits — no matching current generation, or no route on an older
// server — as opposed to a transient failure worth retrying as-is.
func connectorReaffirmationRejected(err error) bool {
	var reqErr *requestError
	return errors.As(err, &reqErr) &&
		(reqErr.StatusCode == http.StatusNotFound || reqErr.StatusCode == http.StatusConflict)
}

// publishPluginDiscovery PUTs one runtime's sanitized Plugin snapshot to the
// cloud. Like the Connector snapshot it replaces the stored projection whole,
// never as a delta.
func (c *Client) publishPluginDiscovery(
	ctx context.Context,
	companyID, runtimeID string,
	manifest pluginDiscoveryManifest,
) (discoveryPublishResult, error) {
	var result discoveryPublishResult
	path := fmt.Sprintf(
		"/api/v1/daemon/companies/%s/runtimes/%s/plugins/discovery",
		companyID,
		runtimeID,
	)
	if err := c.putJSON(ctx, path, manifest, &result); err != nil {
		return discoveryPublishResult{}, err
	}
	return result, nil
}
