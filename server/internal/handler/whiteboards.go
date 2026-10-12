package handler

import (
	"compress/gzip"
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/realtime"
	"boardly.dev/boardly/server/internal/util"
)

func (h *Handler) registerWhiteboardRoutes(api *gin.RouterGroup) {
	if h.BoardService == nil || h.AuthService == nil || h.CompanyService == nil {
		return
	}
	routes := api.Group(
		"/companies/:companyId/whiteboards",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		middleware.RequireHumanActor(),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionWhiteboardsRead),
	)
	edit := middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionWhiteboardsEdit)
	create := middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionWhiteboardsCreate)
	manage := middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionWhiteboardsManage)
	routes.GET("", h.listWhiteboards)
	routes.POST("", create, h.createWhiteboard)
	routes.GET("/:boardId", h.getWhiteboard)
	routes.PATCH("/:boardId", edit, h.updateWhiteboard)
	routes.PUT("/:boardId/favorite", h.favoriteWhiteboard)
	routes.POST("/:boardId/claim", manage, h.claimWhiteboard)
	routes.POST("/:boardId/operations", edit, h.mutateWhiteboard)
	routes.POST("/:boardId/hydrate", h.hydrateWhiteboard)
	routes.POST("/:boardId/text", edit, h.syncWhiteboardText)
	routes.POST("/:boardId/context", h.whiteboardContext)
	routes.PUT("/:boardId/presence", h.whiteboardPresence)
	routes.GET("/:boardId/access", h.whiteboardAccess)
	routes.PUT("/:boardId/access", edit, h.setWhiteboardAccess)
	routes.PUT("/:boardId/visibility", edit, h.setWhiteboardVisibility)
	if h.BoardChannelService != nil {
		routes.GET("/:boardId/channel", h.whiteboardChannel)
		routes.POST("/:boardId/channel", h.joinWhiteboardChannel)
	}
	public := api.Group("/public/whiteboards/:token")
	// Anonymous reads never wait on the pool longer than a page load would;
	// file streams are left unbounded so large downloads finish.
	bounded := middleware.RequestTimeout(10 * time.Second)
	public.GET("", bounded, h.publicWhiteboard)
	public.POST("/hydrate", bounded, h.hydratePublicWhiteboard)
	agent := api.Group(
		"/companies/:companyId/runs/:runId/whiteboards",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	// A Run reaches exactly the calls its channel offers: list, read (its
	// compact view, and context for whole-Board images), compose and edit.
	// Its presence follows those calls.
	agent.GET("", h.listWhiteboardsForRun)
	agent.POST("/:boardId/view", h.whiteboardAgentView)
	agent.POST("/:boardId/operations", edit, h.mutateWhiteboard)
	agent.POST("/:boardId/compose", edit, h.composeWhiteboard)
	agent.POST("/:boardId/context", h.whiteboardContext)
	if h.BoardMediaService != nil {
		public.GET("/files/:attachmentId", h.downloadPublicWhiteboardFile)
		routes.GET("/:boardId/files/:attachmentId", h.downloadWhiteboardFile)
		routes.POST("/:boardId/files", edit, h.attachWhiteboardFile)
	}
}

func whiteboardQuery(c *gin.Context, allowed ...string) (url.Values, bool) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeWhiteboardError(c, canvas.ErrBoardInvalid)
		return nil, false
	}
	for key, items := range values {
		if !slices.Contains(allowed, key) || len(items) != 1 || items[0] == "" {
			writeWhiteboardError(c, canvas.ErrBoardInvalid)
			return nil, false
		}
	}
	return values, true
}

func (h *Handler) whiteboardPath(c *gin.Context, allowed ...string) (auth.UserPrincipal, canvas.BoardScope, bool) {
	p, ok := taskPrincipal(c)
	if !ok {
		return p, canvas.BoardScope{}, false
	}
	id, err := util.ParseGoUUID(c.Param("boardId"))
	if err != nil {
		writeWhiteboardError(c, canvas.ErrBoardNotFound)
		return p, canvas.BoardScope{}, false
	}
	if _, ok = whiteboardQuery(c, allowed...); !ok {
		return p, canvas.BoardScope{}, false
	}
	c.Header("Cache-Control", "private, no-store")
	scope := canvas.BoardScope{BoardID: id}
	if c.Param("runId") != "" {
		_, run, valid := h.contentRunPrincipal(c)
		if !valid {
			return p, scope, false
		}
		scope.RunID = run
	}
	return p, scope, true
}

func whiteboardID(c *gin.Context, key string) (uuid.UUID, bool) {
	id, err := util.ParseGoUUID(c.Param(key))
	if err != nil {
		writeWhiteboardError(c, canvas.ErrBoardNotFound)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) listWhiteboards(c *gin.Context) {
	p, ok := taskPrincipal(c)
	if !ok {
		return
	}
	values, ok := whiteboardQuery(c, "search", "archived", "favorites", "unowned", "limit", "offset")
	if !ok {
		return
	}
	in := canvas.BoardListInput{Search: values.Get("search"), Limit: 40}
	for key, dst := range map[string]*bool{"archived": &in.Archived, "favorites": &in.Favorites, "unowned": &in.Unowned} {
		if raw := values.Get(key); raw != "" {
			if raw != "true" && raw != "false" {
				writeWhiteboardError(c, canvas.ErrBoardInvalid)
				return
			}
			*dst = raw == "true"
		}
	}
	for key, dst := range map[string]*int32{"limit": &in.Limit, "offset": &in.Offset} {
		if raw := values.Get(key); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || value < 0 {
				writeWhiteboardError(c, canvas.ErrBoardInvalid)
				return
			}
			*dst = int32(value)
		}
	}
	result, err := h.BoardService.List(c.Request.Context(), p, in)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) createWhiteboard(c *gin.Context) {
	p, ok := taskPrincipal(c)
	if !ok {
		return
	}
	if _, ok = whiteboardQuery(c); !ok {
		return
	}
	input, err := bindRequest[canvas.CreateBoardInput](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.BoardService.Create(c.Request.Context(), p, input)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getWhiteboard(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c, "after_seq")
	if !ok {
		return
	}
	var after int64
	if raw := c.Query("after_seq"); raw != "" {
		var err error
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			writeWhiteboardError(c, canvas.ErrBoardInvalid)
			return
		}
	}
	result, err := h.BoardService.Snapshot(c.Request.Context(), p, id, after)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	defer gzipWhiteboardResponse(c)()
	c.JSON(http.StatusOK, result)
}

// gzipWhiteboardWriter compresses a scene response: a whole Board's JSON is
// large and repetitive.
type gzipWhiteboardWriter struct {
	gin.ResponseWriter
	zw *gzip.Writer
}

func (w *gzipWhiteboardWriter) Write(data []byte) (int, error) { return w.zw.Write(data) }

func (w *gzipWhiteboardWriter) WriteString(data string) (int, error) { return w.zw.Write([]byte(data)) }

// gzipWhiteboardResponse compresses the rest of the response when the client
// accepts gzip; the returned function finishes the stream.
func gzipWhiteboardResponse(c *gin.Context) func() {
	c.Header("Vary", "Accept-Encoding")
	accepted := false
	for _, part := range strings.Split(c.GetHeader("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(coding), "gzip") && strings.ReplaceAll(params, " ", "") != "q=0" {
			accepted = true
		}
	}
	if !accepted {
		return func() {}
	}
	zw, _ := gzip.NewWriterLevel(c.Writer, gzip.BestSpeed)
	c.Header("Content-Encoding", "gzip")
	c.Writer = &gzipWhiteboardWriter{ResponseWriter: c.Writer, zw: zw}
	return func() { _ = zw.Close() }
}

func (h *Handler) favoriteWhiteboard(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[wikiFavoriteRequest](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err = h.BoardService.Favorite(c.Request.Context(), p, id.BoardID, *input.Favorite.ptr()); err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) updateWhiteboard(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.UpdateBoardInput](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.BoardService.Update(c.Request.Context(), p, id.BoardID, input)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) claimWhiteboard(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardClaimInput](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.BoardService.Claim(c.Request.Context(), p, id.BoardID, input.ExpectedRevision)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) mutateWhiteboard(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardBatch](c, 6<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.BoardService.Mutate(c.Request.Context(), p, id, input)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	// What the edit left on the Board is selected; erased objects are not.
	move := agentPresenceMove{selection: []uuid.UUID{}}
	for _, item := range result.Items {
		if !item.Deleted && len(move.selection) < 100 {
			move.selection = append(move.selection, item.ID)
		}
	}
	move.cursor = whiteboardItemsCenter(result.Items)
	h.moveAgentPresence(c, p, id, move)
	c.JSON(http.StatusOK, result)
}

func (h *Handler) hydrateWhiteboard(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	// A hundred notes, each with a state vector naming its editing clients.
	input, err := bindRequest[canvas.BoardHydrateInput](c, 256<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.BoardService.Hydrate(c.Request.Context(), p, id, input)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) syncWhiteboardText(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardTextInput](c, 512<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.BoardService.Text(c.Request.Context(), p, id, input)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) whiteboardContext(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardContextInput](c, 64<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.BoardService.Context(c.Request.Context(), p, id, input)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	move := agentPresenceMove{}
	if len(input.ItemIDs) > 0 {
		move.cursor = whiteboardItemsCenter(result.Items)
	}
	h.moveAgentPresence(c, p, id, move)
	if input.IncludeVisuals {
		if h.BoardMediaService == nil {
			writeWhiteboardError(c, canvas.ErrBoardInvalid)
			return
		}
		// Later pages may follow an earlier read's sequence; the first page
		// renders the sequence this context was read at.
		if input.ExpectedSequence == nil {
			input.ExpectedSequence = &result.Sequence
		}
		visuals, err := h.BoardMediaService.Visuals(c.Request.Context(), p, id, input)
		if err != nil {
			if c.Request.Context().Err() != nil {
				writeWhiteboardError(c, err)
				return
			}
			// Text context stays complete when images cannot be rendered.
			visuals = canvas.BoardVisuals{
				Sequence:    result.Sequence,
				Regions:     []canvas.BoardVisualRegion{},
				Unavailable: whiteboardVisualsUnavailable(err),
			}
		}
		result.Visuals = &visuals
	}
	c.JSON(http.StatusOK, result)
}

// whiteboardAgentView is an Agent's compact reading of a Board: by default
// the area the person it works for is looking at, so it works where they
// see, with an optional image of exactly that area.
func (h *Handler) whiteboardAgentView(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardAgentViewInput](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	ctx := c.Request.Context()
	result, err := h.BoardService.AgentView(ctx, p, id, input, h.whiteboardFocus(ctx, p, id))
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	move := agentPresenceMove{}
	if result.Area != nil {
		move.cursor = &whiteboardPoint{
			result.Area.X + result.Area.Width/2,
			result.Area.Y + result.Area.Height/2,
		}
	}
	h.moveAgentPresence(c, p, id, move)
	if input.Image && result.Area != nil && len(input.ItemRefs) == 0 {
		if h.BoardMediaService == nil {
			result.ImageUnavailable = whiteboardVisualsUnavailable(canvas.ErrBoardInvalid)
		} else if image, err := h.BoardMediaService.RectVisual(ctx, p, id, *result.Area, 1024); err != nil {
			if ctx.Err() != nil {
				writeWhiteboardError(c, err)
				return
			}
			result.ImageUnavailable = whiteboardVisualsUnavailable(err)
		} else {
			result.Image = &image
		}
	}
	c.JSON(http.StatusOK, result)
}

// composeWhiteboard adds an Agent's intended content where the person it
// works for is looking, then shows the Agent there like a person: its
// cursor on the new work, the work selected and its work area outlined.
func (h *Handler) composeWhiteboard(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardComposeInput](c, 1<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	ctx := c.Request.Context()
	result, err := h.BoardService.Compose(ctx, p, id, input, h.whiteboardFocus(ctx, p, id))
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	move := agentPresenceMove{selection: []uuid.UUID{}}
	created := map[uuid.UUID]bool{}
	for _, id := range result.Mutation.IDs {
		created[id] = true
	}
	for _, item := range result.Mutation.Items {
		if created[item.ID] && len(move.selection) < 100 {
			move.selection = append(move.selection, item.ID)
		}
	}
	if b := result.Bounds; b != nil {
		move.cursor = &whiteboardPoint{b.X + b.Width/2, b.Y + b.Height/2}
		move.workArea = &realtime.CanvasRect{X: b.X, Y: b.Y, Width: b.Width, Height: b.Height}
	}
	h.moveAgentPresence(c, p, id, move)
	if input.Preview && result.Bounds != nil && h.BoardMediaService != nil {
		b := *result.Bounds
		area := canvas.BoardVisualBounds{X: b.X - 40, Y: b.Y - 40, Width: b.Width + 80, Height: b.Height + 80}
		// The work is saved either way; a preview that cannot render is left out.
		if image, err := h.BoardMediaService.RectVisual(ctx, p, id, area, 768); err == nil {
			result.Preview = &image
		}
	}
	c.JSON(http.StatusOK, result)
}

// whiteboardFocus finds where the person a Run works for is looking: their
// focused browser window, else their latest one, else the view they left a
// few minutes ago. It steers reading and placement and grants nothing.
func (h *Handler) whiteboardFocus(
	ctx context.Context,
	p auth.UserPrincipal,
	scope canvas.BoardScope,
) canvas.BoardFocus {
	if h.CanvasPresence == nil {
		return canvas.BoardFocus{}
	}
	room := whiteboardRoom(p, scope)
	sessions, err := h.CanvasPresence.Sessions(ctx, room)
	if err != nil {
		slog.WarnContext(ctx, "Whiteboard presence read failed", "error", err)
	}
	if person := whiteboardRequesterSession(sessions, p.UserID); person != nil {
		return canvas.BoardFocus{
			Present:      true,
			Name:         person.Name,
			View:         canvas.BoardVisualBounds(*person.View),
			SelectionIDs: person.SelectionIDs,
		}
	}
	view, err := h.CanvasPresence.LastView(ctx, room, p.UserID.String())
	if err != nil {
		slog.WarnContext(ctx, "Whiteboard last view read failed", "error", err)
	}
	if view == nil {
		return canvas.BoardFocus{}
	}
	return canvas.BoardFocus{Recent: true, View: canvas.BoardVisualBounds(*view)}
}

func whiteboardVisualsUnavailable(err error) string {
	if errors.Is(err, canvas.ErrBoardLimit) {
		return "Board images exceed the supported size; read fewer selected items to see them."
	}
	return "Board images could not be rendered for this read; the text context is complete."
}

// whiteboardFileETag names the bytes a Board file request serves. An
// attachment ID never changes its bytes, so a browser may keep a copy, but it
// revalidates every use: the server resolves current Board access before it
// answers 304.
func whiteboardFileETag(id uuid.UUID) string {
	return `"` + id.String() + `"`
}

func whiteboardKnownFile(c *gin.Context) uuid.UUID {
	tag := strings.TrimPrefix(strings.TrimSpace(c.GetHeader("If-None-Match")), "W/")
	id, err := util.ParseGoUUID(strings.Trim(tag, `"`))
	if err != nil {
		return uuid.Nil
	}
	return id
}

func whiteboardFileVariant(c *gin.Context) (bool, bool) {
	switch c.Query("variant") {
	case "", "original":
		return false, true
	case "preview":
		return true, true
	}
	writeWhiteboardError(c, canvas.ErrBoardInvalid)
	return false, false
}

func (h *Handler) downloadWhiteboardFile(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c, "variant")
	if !ok {
		return
	}
	file, ok := whiteboardID(c, "attachmentId")
	if !ok {
		return
	}
	preview, ok := whiteboardFileVariant(c)
	if !ok {
		return
	}
	result, err := h.BoardMediaService.FileDownload(c.Request.Context(), p, id, file, preview, whiteboardKnownFile(c))
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.Header("ETag", whiteboardFileETag(result.Attachment.ID))
	if result.NotModified {
		c.Header("Cache-Control", "private, no-cache")
		c.Status(http.StatusNotModified)
		return
	}
	streamAttachmentDownloadWithPolicy(c, result, "private, no-cache")
}

type boardPresenceRequest struct {
	SessionID    uuid.UUID                   `json:"sessionId"    binding:"required"`
	X            float64                     `json:"x"            binding:"gte=-1000000,lte=1000000"`
	Y            float64                     `json:"y"            binding:"gte=-1000000,lte=1000000"`
	CameraX      float64                     `json:"cameraX"      binding:"gte=-1000000,lte=1000000"`
	CameraY      float64                     `json:"cameraY"      binding:"gte=-1000000,lte=1000000"`
	Zoom         float64                     `json:"zoom"         binding:"gte=0.01,lte=8"`
	Pointing     bool                        `json:"pointing"`
	Leave        bool                        `json:"leave"`
	SelectionIDs []uuid.UUID                 `json:"selectionIds" binding:"max=100,dive,required"`
	DragPreviews []realtime.BoardDragPreview `json:"dragPreviews" binding:"max=100,dive"`
	View         *boardPresenceView          `json:"view"`
	Focused      bool                        `json:"focused"`
}

// boardPresenceView is the area of the Board a browser shows, in Board units.
type boardPresenceView struct {
	X      float64 `json:"x"      binding:"gte=-1000000,lte=1000000"`
	Y      float64 `json:"y"      binding:"gte=-1000000,lte=1000000"`
	Width  float64 `json:"width"  binding:"gt=0,lte=2000000"`
	Height float64 `json:"height" binding:"gt=0,lte=2000000"`
}

func (h *Handler) whiteboardPresence(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[boardPresenceRequest](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	viewer, err := h.BoardService.Viewer(c.Request.Context(), p, id)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	session := realtime.CanvasSession{
		SessionID:    input.SessionID.String(),
		UserID:       p.UserID.String(),
		Name:         viewer.Actor.DisplayName,
		Actor:        viewer.Actor,
		X:            input.X,
		Y:            input.Y,
		CameraX:      input.CameraX,
		CameraY:      input.CameraY,
		Zoom:         input.Zoom,
		Pointing:     input.Pointing,
		SelectionIDs: input.SelectionIDs,
		DragPreviews: input.DragPreviews,
		UpdatedAt:    time.Now().UnixMilli(),
		Focused:      input.Focused,
	}
	if input.View != nil {
		session.View = &realtime.CanvasRect{
			X: input.View.X, Y: input.View.Y, Width: input.View.Width, Height: input.View.Height,
		}
	}
	sessions, err := h.CanvasPresence.Update(c.Request.Context(), whiteboardRoom(p, id), session, input.Leave)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	sessions, err = h.publishWhiteboardPresence(c, p, id, viewer, sessions, session, input.Leave)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Sessions []realtime.CanvasSession `json:"sessions"`
	}{sessions})
}

func whiteboardRoom(p auth.UserPrincipal, scope canvas.BoardScope) string {
	return p.CompanyID.String() + ":whiteboard:" + scope.BoardID.String()
}

// publishWhiteboardPresence relays one session to the people who have the
// Board open and answers the room's sessions the viewer's audience may see.
// Only people with the Board open receive cursor frames; anyone who opens it
// later reads every session from their own first heartbeat.
func (h *Handler) publishWhiteboardPresence(
	c *gin.Context,
	p auth.UserPrincipal,
	scope canvas.BoardScope,
	viewer canvas.BoardViewer,
	sessions []realtime.CanvasSession,
	session realtime.CanvasSession,
	leave bool,
) ([]realtime.CanvasSession, error) {
	audience := make(map[string]bool, len(viewer.RecipientUserIDs))
	for _, id := range viewer.RecipientUserIDs {
		audience[id.String()] = true
	}
	sessions = slices.DeleteFunc(
		sessions,
		func(session realtime.CanvasSession) bool { return !audience[session.UserID] },
	)
	if h.EphemeralRelay == nil {
		return sessions, nil
	}
	company, actor := p.CompanyID.String(), viewer.Actor.ID.String()
	return sessions, h.EphemeralRelay.PublishToCompanyUsers(
		c.Request.Context(),
		company,
		whiteboardViewers(sessions),
		realtime.BroadcastEvent{
			Type:      "whiteboard.presence",
			CompanyID: &company,
			ActorID:   &actor,
			ActorType: string(viewer.Actor.Kind),
			CreatedAt: time.Now().UTC(),
			Payload: struct {
				BoardID uuid.UUID              `json:"boardId"`
				Session realtime.CanvasSession `json:"session"`
				Leave   bool                   `json:"leave"`
			}{scope.BoardID, session, leave},
		},
	)
}

// agentPresenceMove is where a working Agent's cursor goes after a call and
// what it selects; nil keeps what its session already shows.
type agentPresenceMove struct {
	cursor    *whiteboardPoint
	selection []uuid.UUID
	workArea  *realtime.CanvasRect
}

// moveAgentPresence shows a working Agent on the Board like a person: its
// session joins with its first call, then its cursor moves to where it reads
// or edits and its selection names what it touched. A new session starts at
// the center of the view of the person it works for. Presence grants
// nothing, so a failure is logged, never answered.
func (h *Handler) moveAgentPresence(
	c *gin.Context,
	p auth.UserPrincipal,
	scope canvas.BoardScope,
	move agentPresenceMove,
) {
	if scope.RunID == uuid.Nil || h.CanvasPresence == nil {
		return
	}
	ctx := c.Request.Context()
	if err := h.updateAgentPresence(c, p, scope, move); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "Agent presence update failed", "error", err)
	}
}

func (h *Handler) updateAgentPresence(
	c *gin.Context,
	p auth.UserPrincipal,
	scope canvas.BoardScope,
	move agentPresenceMove,
) error {
	ctx := c.Request.Context()
	viewer, err := h.BoardService.Viewer(ctx, p, scope)
	if err != nil {
		return err
	}
	room := whiteboardRoom(p, scope)
	sessions, err := h.CanvasPresence.Sessions(ctx, room)
	if err != nil {
		return err
	}
	session := realtime.CanvasSession{
		SessionID:    scope.RunID.String(),
		UserID:       p.UserID.String(),
		Name:         viewer.Actor.DisplayName,
		Actor:        viewer.Actor,
		RunID:        &scope.RunID,
		Zoom:         1,
		Pointing:     true,
		SelectionIDs: move.selection,
		UpdatedAt:    time.Now().UnixMilli(),
		WorkArea:     move.workArea,
	}
	if move.cursor != nil {
		session.X, session.Y = move.cursor.x, move.cursor.y
	}
	index := slices.IndexFunc(sessions, func(s realtime.CanvasSession) bool {
		return s.RunID != nil && *s.RunID == scope.RunID
	})
	switch {
	case index >= 0:
		previous := sessions[index]
		if move.cursor == nil {
			session.X, session.Y = previous.X, previous.Y
		}
		if move.selection == nil {
			session.SelectionIDs = previous.SelectionIDs
		}
		if move.workArea == nil {
			session.WorkArea = previous.WorkArea
		}
	case move.cursor == nil:
		// A new session starts where the person it works for looks, or
		// looked a moment ago; with neither, it waits for a place to show.
		view, err := h.whiteboardRequesterView(ctx, room, sessions, p.UserID)
		if err != nil || view == nil {
			return err
		}
		session.X, session.Y = view.X+view.Width/2, view.Y+view.Height/2
	}
	sessions, err = h.CanvasPresence.Update(ctx, room, session, false)
	if errors.Is(err, realtime.ErrCanvasPresenceFull) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = h.publishWhiteboardPresence(c, p, scope, viewer, sessions, session, false)
	return err
}

// whiteboardRequesterView is the area a person looks at on the Board, or
// looked at a moment ago; nil when neither is known.
func (h *Handler) whiteboardRequesterView(
	ctx context.Context,
	room string,
	sessions []realtime.CanvasSession,
	user uuid.UUID,
) (*realtime.CanvasRect, error) {
	if person := whiteboardRequesterSession(sessions, user); person != nil {
		return person.View, nil
	}
	return h.CanvasPresence.LastView(ctx, room, user.String())
}

// whiteboardRequesterSession is the browser session showing where a person
// looks: the focused window when there is one, else the latest heartbeat.
func whiteboardRequesterSession(sessions []realtime.CanvasSession, user uuid.UUID) *realtime.CanvasSession {
	var best *realtime.CanvasSession
	for i := range sessions {
		session := &sessions[i]
		if session.RunID != nil || session.View == nil || session.UserID != user.String() {
			continue
		}
		if best == nil || session.Focused && !best.Focused ||
			session.Focused == best.Focused && session.UpdatedAt > best.UpdatedAt {
			best = session
		}
	}
	return best
}

// whiteboardPoint is a position in Board coordinates.
type whiteboardPoint struct{ x, y float64 }

// whiteboardItemsCenter is the center of the live items' bounding box, else
// of the items just deleted, where the Agent erased; nil for no items.
func whiteboardItemsCenter(items []canvas.BoardItem) *whiteboardPoint {
	live := slices.ContainsFunc(items, func(item canvas.BoardItem) bool { return !item.Deleted })
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, item := range items {
		if !item.Deleted || !live {
			minX, minY = min(minX, item.X), min(minY, item.Y)
			maxX, maxY = max(maxX, item.X+item.Width), max(maxY, item.Y+item.Height)
		}
	}
	if minX > maxX {
		return nil
	}
	return &whiteboardPoint{(minX + maxX) / 2, (minY + maxY) / 2}
}

func whiteboardViewers(sessions []realtime.CanvasSession) []uuid.UUID {
	viewers := make([]uuid.UUID, 0, len(sessions))
	for _, session := range sessions {
		if id, err := uuid.Parse(session.UserID); err == nil && !slices.Contains(viewers, id) {
			viewers = append(viewers, id)
		}
	}
	return viewers
}

var whiteboardErrorMappings = []errorMapping{
	{canvas.ErrBoardNotFound, 404, "Whiteboard or file is unavailable", "whiteboard_not_found"},
	{canvas.ErrBoardItemNotFound, 404, "An object in this Whiteboard change no longer exists", "whiteboard_item_not_found"},
	{canvas.ErrBoardForbidden, 403, "You do not have permission for this Whiteboard action", "whiteboard_forbidden"},
	{canvas.ErrBoardInvalid, 422, "Invalid Whiteboard operation", "whiteboard_invalid"},
	{
		canvas.ErrBoardRequestKeyReused,
		422,
		"This request key was already used for a different Whiteboard change",
		"whiteboard_request_key_reused",
	},
	{
		canvas.ErrBoardConflict,
		409,
		"The Whiteboard item changed. Review the saved version before retrying.",
		"whiteboard_conflict",
	},
	{canvas.ErrBoardLimit, 422, "This Whiteboard operation exceeds the supported size", "whiteboard_limit"},
	{canvas.ErrBoardArchived, 409, "This Whiteboard is archived", "whiteboard_archived"},
	{
		canvas.ErrBoardLocked,
		409,
		"This Whiteboard object is locked. Unlock it before moving it.",
		"whiteboard_locked",
	},
	{realtime.ErrCanvasPresenceFull, 409, "Too many people are present on this Whiteboard", "whiteboard_presence_full"},
	{
		attachment.ErrStorageUnavailable,
		http.StatusServiceUnavailable,
		"Attachment storage is unavailable",
		"attachment_storage_unavailable",
	},
	{
		attachment.ErrUnavailable,
		http.StatusServiceUnavailable,
		"Attachment service is unavailable",
		"attachment_unavailable",
	},
}

func writeWhiteboardError(c *gin.Context, err error) {
	// An invalid operation that knows why says so, so its caller can fix it.
	var invalid canvas.BoardInvalidError
	if errors.As(err, &invalid) {
		writeBoundaryErrorDetail(
			c,
			boundaryErrorWhiteboardInvalid,
			"Invalid Whiteboard operation: "+invalid.Reason,
		)
		return
	}
	writeMappedError(c, err, whiteboardErrorMappings, internalErrorFallback)
}

func (h *Handler) whiteboardAccess(c *gin.Context) {
	p, scope, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	result, err := h.BoardService.Access(c.Request.Context(), p, scope.BoardID)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	if h.OAuthWebAppOrigin != "" {
		result.URL = strings.TrimRight(
			h.OAuthWebAppOrigin,
			"/",
		) + "/whiteboards/" + scope.BoardID.String() + "?companyId=" + p.CompanyID.String()
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) setWhiteboardAccess(c *gin.Context) {
	p, scope, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardAccessInput](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err = h.BoardService.SetAccess(c.Request.Context(), p, scope.BoardID, input); err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) listWhiteboardsForRun(c *gin.Context) {
	p, run, ok := h.contentRunPrincipal(c)
	if !ok {
		return
	}
	values, ok := whiteboardQuery(c, "offset")
	if !ok {
		return
	}
	offset := int64(0)
	if raw := values.Get("offset"); raw != "" {
		var err error
		offset, err = strconv.ParseInt(raw, 10, 32)
		if err != nil || offset < 0 {
			writeWhiteboardError(c, canvas.ErrBoardInvalid)
			return
		}
	}
	result, err := h.BoardService.ListForRun(c.Request.Context(), p, run, int32(offset))
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, result)
}

func (h *Handler) setWhiteboardVisibility(c *gin.Context) {
	p, scope, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardVisibilityInput](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err = h.BoardService.SetVisibility(c.Request.Context(), p, scope.BoardID, input); err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func publicWhiteboardToken(c *gin.Context, allowed ...string) (uuid.UUID, bool) {
	token, ok := whiteboardID(c, "token")
	if !ok {
		return token, false
	}
	if _, ok = whiteboardQuery(c, allowed...); !ok {
		return token, false
	}
	c.Header("Cache-Control", "private, no-store")
	return token, true
}

func (h *Handler) publicWhiteboard(c *gin.Context) {
	token, ok := publicWhiteboardToken(c)
	if !ok {
		return
	}
	result, err := h.BoardService.PublicSnapshot(c.Request.Context(), token)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	defer gzipWhiteboardResponse(c)()
	c.JSON(http.StatusOK, result)
}

func (h *Handler) hydratePublicWhiteboard(c *gin.Context) {
	token, ok := publicWhiteboardToken(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardHydrateInput](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.BoardService.PublicHydrate(c.Request.Context(), token, input)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) downloadPublicWhiteboardFile(c *gin.Context) {
	token, ok := publicWhiteboardToken(c, "variant")
	if !ok {
		return
	}
	file, ok := whiteboardID(c, "attachmentId")
	if !ok {
		return
	}
	preview, ok := whiteboardFileVariant(c)
	if !ok {
		return
	}
	result, err := h.BoardMediaService.PublicFileDownload(c.Request.Context(), token, file, preview)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	// Public links are revocable; never let a shared cache keep their files.
	streamAttachmentDownloadWithPolicy(c, result, "private, no-store")
}

func (h *Handler) attachWhiteboardFile(c *gin.Context) {
	p, scope, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.BoardFileInput](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.BoardMediaService.AttachFile(c.Request.Context(), p, scope.BoardID, input)
	if err != nil {
		writeWhiteboardError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// whiteboardChannel reads the Board's chat and the people who can read the
// Board, without joining it.
func (h *Handler) whiteboardChannel(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	result, err := h.BoardChannelService.Channel(c.Request.Context(), p, id.BoardID)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// joinWhiteboardChannel opens the Board's chat for its reader, starting it the
// first time.
func (h *Handler) joinWhiteboardChannel(c *gin.Context) {
	p, id, ok := h.whiteboardPath(c)
	if !ok {
		return
	}
	result, err := h.BoardChannelService.Join(c.Request.Context(), p, id.BoardID)
	if err != nil {
		writeDiscussionError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
