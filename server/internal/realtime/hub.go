package realtime

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var (
	allowedWSOrigins atomic.Value // holds []string
	trustedProxies   atomic.Value // holds []netip.Prefix
)

// SetAllowedOrigins wires the WebSocket origin whitelist. internal/config
// owns the environment; cmd/server calls this at startup — this package
// deliberately performs no env reads of its own (no init() side effects, the
// same rule the handler package documents). Until wired, the nil whitelist
// fails closed against browser origins.
func SetAllowedOrigins(origins []string) {
	allowedWSOrigins.Store(origins)
}

// SetTrustedProxies wires the trusted proxy CIDR list from the shared config;
// cmd/server calls this at startup beside SetAllowedOrigins. Nil means
// "trust no proxy" and X-Forwarded-* is never honored.
func SetTrustedProxies(proxies []netip.Prefix) {
	trustedProxies.Store(proxies)
}

// isTrustedProxy reports whether the request's remote address falls within one
// of the configured trusted proxy CIDRs.
func isTrustedProxy(remoteAddr string) bool {
	proxies, _ := trustedProxies.Load().([]netip.Prefix)
	if len(proxies) == 0 {
		return false
	}
	addr, err := netip.ParseAddr(remoteHost(remoteAddr))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range proxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// remoteHost extracts the host/IP from an http.Request.RemoteAddr, which is
// normally "host:port". It handles bracketed IPv6 ("[::1]:443") via
// net.SplitHostPort and falls back to the raw value (sans brackets) when no
// port is present.
func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return strings.Trim(remoteAddr, "[]")
}

// firstForwardedHost returns the first host from a (possibly comma-separated)
// X-Forwarded-Host header. Proxy chains append values left-to-right, so the
// first entry is the original client-facing host we compare against Origin.
func firstForwardedHost(h string) string {
	if i := strings.IndexByte(h, ','); i >= 0 {
		h = h[:i]
	}
	return strings.TrimSpace(h)
}

func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	// Same-origin: native clients (mobile, CLI) have no real page host, so
	// their WebSocket library fills Origin with the connection target —
	// which equals the server's own Host. They authenticate via bearer
	// token, not auto-attached cookies, so CSRF (the attack the explicit
	// allowlist below defends against) does not apply. This matches the
	// gorilla/websocket default CheckOrigin behavior; the allowlist exists
	// in addition to support cross-origin browser clients (web/desktop).
	if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, r.Host) {
		return true
	}
	// Reverse-proxy support: when sitting behind a proxy the Host header
	// contains the internal address. X-Forwarded-Host carries the original
	// public host seen by the client, so we treat a matching origin as
	// same-origin in that case too. SECURITY: Only trust X-Forwarded-Host
	// if the request comes from a trusted proxy to prevent header spoofing.
	if fwdHost := firstForwardedHost(
		r.Header.Get("X-Forwarded-Host"),
	); fwdHost != "" &&
		isTrustedProxy(r.RemoteAddr) {
		if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, fwdHost) {
			return true
		}
	}
	origins, _ := allowedWSOrigins.Load().([]string)
	for _, allowed := range origins {
		if origin == allowed {
			return true
		}
	}
	slog.Warn("ws: rejected origin", "origin", origin, "remote_addr", r.RemoteAddr)
	return false
}

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10

	// inboundReadLimit caps unsolicited application messages on this
	// receive-only plane. Gorilla buffers a whole message before handing it
	// over, and a fragmented message keeps the read deadline alive through
	// interleaved protocol pongs. Without a limit one connection could grow
	// that buffer without bound and OOM the process.
	inboundReadLimit = 64 * 1024
)

var upgrader = websocket.Upgrader{
	CheckOrigin: checkOrigin,
}

// Upgrade turns an HTTP request into a WebSocket connection using the
// package's origin-checked upgrader. The handler layer authenticates the
// request, calls Upgrade, then hands the connection to Hub.ServeClient.
func Upgrade(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	return upgrader.Upgrade(w, r, nil)
}

// scopeKey identifies one server-owned company or user identity room.
type scopeKey struct {
	Type string
	ID   string
}

func sk(t, id string) scopeKey { return scopeKey{Type: t, ID: id} }

// Client represents a single authenticated WebSocket connection. Its rooms
// are fixed from the server-authorized company and user identities at connect
// time; browser frames cannot add or remove rooms.
type Client struct {
	hub        *Hub
	conn       *websocket.Conn
	send       chan []byte
	userID     string
	companyID  string
	registered chan struct{}

	// rooms is guarded by hub.mu and used only to clean up identity rooms on
	// disconnect.
	rooms map[scopeKey]bool

	// lastSeenEventIDs deduplicates messages that arrived first via the local
	// fast path and are then replayed from Redis. Bounded LRU semantics are
	// not required because event IDs are ULIDs and we only keep the last few.
	dedupMu  sync.Mutex
	seenIDs  map[string]struct{}
	seenList []string
}

const dedupCapacity = 128

// markSeen records eventID as already delivered to this client. Returns true
// if it was the first time we saw this id (caller should deliver), false if
// it's a duplicate (caller should drop).
func (c *Client) markSeen(eventID string) bool {
	if eventID == "" {
		return true
	}
	c.dedupMu.Lock()
	defer c.dedupMu.Unlock()
	if c.seenIDs == nil {
		c.seenIDs = make(map[string]struct{}, dedupCapacity)
	}
	if _, ok := c.seenIDs[eventID]; ok {
		return false
	}
	c.seenIDs[eventID] = struct{}{}
	c.seenList = append(c.seenList, eventID)
	if len(c.seenList) > dedupCapacity {
		drop := c.seenList[0]
		c.seenList = c.seenList[1:]
		delete(c.seenIDs, drop)
	}
	return true
}

// Hub manages WebSocket connections in server-owned company and user rooms.
type Hub struct {
	rooms      map[scopeKey]map[*Client]bool
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

// NewHub creates a new Hub instance.
func NewHub() *Hub {
	return &Hub{
		rooms:      make(map[scopeKey]map[*Client]bool),
		clients:    make(map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub event loop.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.joinIdentityRoomLocked(client, ScopeCompany, client.companyID)
			h.joinIdentityRoomLocked(client, ScopeUser, client.userID)
			total := len(h.clients)
			h.mu.Unlock()
			if client.registered != nil {
				close(client.registered)
			}
			M.ConnectsTotal.Add(1)
			M.ActiveConnections.Add(1)
			slog.Info(
				"ws client connected",
				"company_id",
				client.companyID,
				"user_id",
				client.userID,
				"total_clients",
				total,
			)

		case client := <-h.unregister:
			h.removeClient(client)
		}
	}
}

// removeClient drops a client from all rooms and the global set.
func (h *Hub) removeClient(client *Client) {
	h.mu.Lock()
	if !h.clients[client] {
		h.mu.Unlock()
		return
	}
	delete(h.clients, client)
	rooms := client.rooms
	client.rooms = nil
	emptied := make([]scopeKey, 0, len(rooms))
	for key := range rooms {
		if room, ok := h.rooms[key]; ok {
			delete(room, client)
			if len(room) == 0 {
				delete(h.rooms, key)
				emptied = append(emptied, key)
			}
		}
	}
	close(client.send)
	total := len(h.clients)
	h.mu.Unlock()

	M.DisconnectsTotal.Add(1)
	M.ActiveConnections.Add(-1)
	for _, key := range emptied {
		M.DecRoom(key.Type)
	}
	slog.Info(
		"ws client disconnected",
		"company_id",
		client.companyID,
		"user_id",
		client.userID,
		"total_clients",
		total,
	)
}

// joinIdentityRoomLocked adds a client to one server-owned identity room.
// h.mu must be held by the caller.
func (h *Hub) joinIdentityRoomLocked(client *Client, scopeType, scopeID string) {
	if scopeID == "" || (scopeType != ScopeCompany && scopeType != ScopeUser) {
		return
	}
	key := sk(scopeType, scopeID)
	if client.rooms == nil {
		client.rooms = make(map[scopeKey]bool, 2)
	}
	if client.rooms[key] {
		return
	}
	client.rooms[key] = true
	room, exists := h.rooms[key]
	if !exists {
		room = make(map[*Client]bool)
		h.rooms[key] = room
		M.IncRoom(scopeType)
	}
	room[client] = true
}

// Deliver fans one canonical frame out to exactly the authenticated
// connections selected by audience. Browser-controlled resource rooms do not
// exist, so routing cannot broaden after connect. eventID is required and is
// also the local/Redis loopback deduplication key.
func (h *Hub) Deliver(to audience, message []byte, eventID string) {
	if len(message) == 0 || eventID == "" {
		return
	}

	h.mu.RLock()
	clients := h.audienceClientsLocked(to)
	var slow []*Client
	var sent int64
	for client := range clients {
		if !client.markSeen(eventID) {
			continue
		}
		select {
		case client.send <- message:
			sent++
		default:
			slow = append(slow, client)
		}
	}
	h.mu.RUnlock()
	if sent > 0 {
		M.MessagesSentTotal.Add(sent)
	}
	if len(slow) > 0 {
		h.evictSlow(slow)
	}
}

// audienceClientsLocked returns a set so CompanyOrUser naturally de-duplicates
// a connection that matches both halves. h.mu must be read- or write-locked.
func (h *Hub) audienceClientsLocked(to audience) map[*Client]struct{} {
	clients := make(map[*Client]struct{})
	addRoom := func(key scopeKey, match func(*Client) bool) {
		for client := range h.rooms[key] {
			if match == nil || match(client) {
				clients[client] = struct{}{}
			}
		}
	}
	switch to.typ {
	case audienceCompany:
		addRoom(sk(ScopeCompany, to.companyID), nil)
	case audienceUser:
		addRoom(sk(ScopeUser, to.userID), nil)
	case audienceCompanyUser:
		addRoom(sk(ScopeCompany, to.companyID), func(client *Client) bool {
			return client.userID == to.userID
		})
	case audienceCompanyOrUser:
		addRoom(sk(ScopeCompany, to.companyID), nil)
		addRoom(sk(ScopeUser, to.userID), nil)
	}
	return clients
}

// evictSlow removes clients whose send channel was full: closes the send
// channel and decrements the room and connection counters.
func (h *Hub) evictSlow(slow []*Client) {
	M.MessagesDroppedTotal.Add(int64(len(slow)))
	M.SlowEvictionsTotal.Add(int64(len(slow)))

	h.mu.Lock()
	evicted := 0
	type emptied struct {
		Type, ID string
	}
	var drainedRooms []emptied
	for _, c := range slow {
		if !h.clients[c] {
			continue
		}
		delete(h.clients, c)
		for key := range c.rooms {
			if room, ok := h.rooms[key]; ok {
				delete(room, c)
				if len(room) == 0 {
					delete(h.rooms, key)
					drainedRooms = append(drainedRooms, emptied(key))
				}
			}
		}
		c.rooms = nil
		close(c.send)
		evicted++
	}
	h.mu.Unlock()

	if evicted > 0 {
		M.ActiveConnections.Add(int64(-evicted))
		M.DisconnectsTotal.Add(int64(evicted))
	}
	for _, r := range drainedRooms {
		M.DecRoom(r.Type)
	}
}

// Snapshot returns a JSON-friendly summary of the hub state.
func (h *Hub) Snapshot() map[string]any {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rooms := map[string]int{}
	for key := range h.rooms {
		rooms[key.Type]++
	}
	return map[string]any{
		"connections": len(h.clients),
		"rooms":       rooms,
	}
}

// ServeClient attaches an authenticated WebSocket connection to the hub and
// starts its read/write pumps. The handler layer owns authentication and the
// membership check; by the time it calls ServeClient it must have verified
// that userID is a member of companyID. Registration joins the two fixed
// identity rooms; application frames from the browser cannot change them.
func (h *Hub) ServeClient(conn *websocket.Conn, userID, companyID string, validate ...func() bool) {
	// Bound inbound messages before the first read so no read path is ever
	// unbounded.
	conn.SetReadLimit(inboundReadLimit)

	client := &Client{
		hub:        h,
		conn:       conn,
		send:       make(chan []byte, 256),
		registered: make(chan struct{}),
		userID:     userID,
		companyID:  companyID,
	}
	h.register <- client
	<-client.registered
	// Registration precedes the second access check, closing the gap where
	// revocation could commit after upgrade but before this socket joined.
	if len(validate) > 0 && !validate[0]() {
		h.removeClient(client)
		_ = conn.Close()
		return
	}

	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	// A read deadline can only fail on a connection that is already dead, and
	// the ReadMessage below then fails and tears the client down, so there is
	// nothing to act on here.
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		// The browser plane is receive-only. ReadMessage still has to drain
		// application frames so gorilla can process control pong frames and
		// enforce inboundReadLimit, but application data is discarded.
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			switch {
			case errors.Is(err, websocket.ErrReadLimit):
				// Counted separately so an over-limit close stays visible
				// instead of blending into ordinary connection churn.
				M.InboundTooLargeTotal.Add(1)
				slog.Warn("ws: inbound frame exceeded read limit",
					"limit_bytes", inboundReadLimit,
					"user_id", c.userID,
					"company_id", c.companyID,
				)
			case websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure):
				slog.Debug(
					"websocket read error",
					"error",
					err,
					"user_id",
					c.userID,
					"company_id",
					c.companyID,
				)
			}
			break
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			// A write deadline only fails on a dead connection; the write
			// below then fails and this pump returns.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Best-effort courtesy close frame: the send channel is
				// already closed and this client is going away regardless.
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				slog.Warn(
					"websocket write error",
					"error",
					err,
					"user_id",
					c.userID,
					"company_id",
					c.companyID,
				)
				return
			}
		case <-ticker.C:
			// Same as above: a failed deadline surfaces as a failed ping.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// disconnectCompanyUser queues the final receipt before closing each send
// channel. The writer drains that receipt; no subsequent fanout finds the
// removed connections. Other companies for the same user remain connected.
func (h *Hub) disconnectCompanyUser(companyID, userID string, frame []byte, eventID string) {
	to := audience{typ: audienceCompanyUser, companyID: companyID, userID: userID}
	h.mu.RLock()
	clients := h.audienceClientsLocked(to)
	h.mu.RUnlock()
	h.Deliver(to, frame, eventID)
	for client := range clients {
		h.removeClient(client)
	}
}
