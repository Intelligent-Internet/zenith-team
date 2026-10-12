package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// clientWebSocketHTTPPath is the client realtime socket. CORS bypasses it:
// browsers cannot set custom headers on WebSocket dials, and the upgrade
// handshake carries its own origin check (realtime.Upgrade). The route
// registration below derives from this constant, so the CORS bypass and the
// mounted path cannot drift apart.
const clientWebSocketHTTPPath = "/api/v1/ws"

// registerClientWebSocketRoute mounts the composed client WS handler. The
// composition root builds it from the realtime hub (auth resolution, origin
// check via realtime.Upgrade, then hub.ServeClient); the route stays a thin
// delegation so gin never leaks into the realtime plane.
func (h *Handler) registerClientWebSocketRoute(api *gin.RouterGroup) {
	if h.ClientWebSocket == nil {
		return
	}
	api.GET(strings.TrimPrefix(clientWebSocketHTTPPath, api.BasePath()), gin.WrapH(h.ClientWebSocket))
}
