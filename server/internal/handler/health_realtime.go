package handler

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/daemonws"
	"boardly.dev/boardly/server/internal/realtime"
)

type realtimeHealthResponse struct {
	realtime.MetricsSnapshot
	DaemonWS daemonws.MetricsSnapshot `json:"daemonws"`
}

// realtimeMetrics serves the live realtime + daemon-WS hub counters for
// operators. Access mirrors the reference architecture's gate: a configured
// bearer token admits anyone who presents it; without a token only direct
// loopback connections may read, and every other caller gets a
// non-disclosing 404.
func (h *Handler) realtimeMetrics(c *gin.Context) {
	if !h.realtimeMetricsAuthorized(c.Request) {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.JSON(http.StatusOK, realtimeHealthResponse{
		MetricsSnapshot: realtime.M.Snapshot(),
		DaemonWS:        daemonws.M.Snapshot(),
	})
}

func (h *Handler) realtimeMetricsAuthorized(r *http.Request) bool {
	if token := h.RealtimeMetricsToken; token != "" {
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		return subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
