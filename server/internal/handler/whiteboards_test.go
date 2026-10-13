package handler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/realtime"
	"boardly.dev/boardly/server/pkg/protocol"
)

type boardHTTPStub struct {
	canvas.BoardService
	calls   int
	board   uuid.UUID
	company uuid.UUID
	run     uuid.UUID
	agent   uuid.UUID
	items   []canvas.BoardItem
}

func TestWhiteboardUploadRefusesRuns(t *testing.T) {
	t.Parallel()
	f := newWikiAgentFixture("direct")
	h := f.handler()
	h.BoardService, h.BoardMediaService = &boardHTTPStub{}, &boardVisualHTTPStub{}
	h = New(*h)
	for _, scope := range []string{"/runs/" + f.runID.String()} {
		path := "/api/v1/companies/" + f.principal.CompanyID.String() + scope + "/agent-artifacts/" + uuid.NewString() + "/whiteboard/files"
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer access-token")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(runTokenHeader, wikiAgentRunToken)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s = %d, want 404", path, response.Code)
		}
	}
}

func TestWhiteboardPropertiesRejectReferenceURL(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"url":"https://example.test"}`))
	request.Header.Set("Content-Type", "application/json")
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = request
	if _, err := bindRequest[canvas.BoardProperties](ctx, 4096); err == nil {
		t.Fatal("reference URL accepted, want unknown-field error")
	}
}

func (*boardHTTPStub) Context(
	context.Context,
	auth.UserPrincipal,
	canvas.BoardScope,
	canvas.BoardContextInput,
) (canvas.BoardContext, error) {
	return canvas.BoardContext{Sequence: 17}, nil
}

type boardVisualHTTPStub struct {
	canvas.BoardMediaService
	input canvas.BoardContextInput
	scope canvas.BoardScope
	calls int
	err   error
	empty bool
}

func (s *boardVisualHTTPStub) Visuals(
	_ context.Context,
	_ auth.UserPrincipal,
	scope canvas.BoardScope,
	input canvas.BoardContextInput,
) (canvas.BoardVisuals, error) {
	s.calls++
	s.input, s.scope = input, scope
	return canvas.BoardVisuals{
		Sequence:     17,
		Regions:      []canvas.BoardVisualRegion{{Index: 4, MIMEType: "image/png", Data: []byte("PNG")}},
		TotalRegions: 5,
	}, s.err
}

func TestWhiteboardContextImagesUseRunProofAndCommittedSequence(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body, proof string
		err               error
		want              int
	}{
		{"images", `{"includeVisuals":true,"visualOffset":4,"expectedSequence":17}`, wikiAgentRunToken, nil, 200},
		{"text only", `{}`, wikiAgentRunToken, nil, 200},
		{"missing proof", `{"includeVisuals":true}`, "", nil, 401},
		{"invalid offset", `{"includeVisuals":true,"visualOffset":-1}`, wikiAgentRunToken, nil, 422},
		{"images unavailable keep text", `{"includeVisuals":true}`, wikiAgentRunToken, canvas.ErrBoardLimit, 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWikiAgentFixture("direct")
			h := f.handler()
			visuals := &boardVisualHTTPStub{err: tt.err}
			h.BoardService, h.BoardMediaService = &boardHTTPStub{}, visuals
			h = New(*h)
			board := uuid.New()
			path := "/api/v1/companies/" + f.principal.CompanyID.String() + "/runs/" + f.runID.String() + "/agent-artifacts/" + board.String() + "/whiteboard/context"
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer access-token")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(runTokenHeader, tt.proof)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.want, response.Body.String())
			}
			switch {
			case tt.name == "images":
				if visuals.calls != 1 || visuals.scope.BoardID != board ||
					visuals.scope.RunID != f.runID ||
					visuals.input.ExpectedSequence == nil ||
					*visuals.input.ExpectedSequence != 17 ||
					visuals.input.VisualOffset != 4 {
					t.Fatalf("visual request = %+v", visuals)
				}
				var got canvas.BoardContext
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Visuals == nil || len(got.Visuals.Regions) != 1 ||
					string(got.Visuals.Regions[0].Data) != "PNG" {
					t.Fatal("missing native image bytes")
				}
			case tt.err != nil:
				var got canvas.BoardContext
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Sequence != 17 || got.Visuals == nil || got.Visuals.Unavailable == "" ||
					len(got.Visuals.Regions) != 0 {
					t.Fatalf("context without images = %+v", got)
				}
			case visuals.calls != 0:
				t.Fatal("text-only or rejected call rendered images")
			}
		})
	}
}

func (s *boardHTTPStub) Viewer(
	_ context.Context,
	p auth.UserPrincipal,
	scope canvas.BoardScope,
) (canvas.BoardViewer, error) {
	viewer := canvas.BoardViewer{
		RecipientUserIDs: []uuid.UUID{p.UserID},
		Actor:            protocol.ActorRef{ID: p.UserID, Kind: protocol.ActorKindUser},
	}
	if scope.RunID != uuid.Nil {
		viewer.Actor.ID, viewer.Actor.Kind, viewer.RunID = s.agent, protocol.ActorKindAgent, &scope.RunID
	}
	return viewer, nil
}

// A working Agent appears on the Board like a person: reading or editing
// shows its cursor where it works and the objects it touched, beside the
// member's own session, and its old presence route is gone.
func TestWhiteboardRunCallsShowTheAgentLikeAPerson(t *testing.T) {
	t.Parallel()
	f := newWikiAgentFixture("direct")
	h := f.handler()
	note := uuid.New()
	h.BoardService = &boardHTTPStub{
		agent: f.origin.AgentID,
		items: []canvas.BoardItem{{
			ID:            note,
			BoardGeometry: canvas.BoardGeometry{X: 300, Y: 400, Width: 200, Height: 100},
		}},
	}
	h.CanvasPresence = realtime.NewCanvasPresence(nil)
	h = New(*h)
	board, memberSession := uuid.New(), uuid.New()
	base := "/api/v1/companies/" + f.principal.CompanyID.String()
	call := func(method, path, body string, run bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer access-token")
		req.Header.Set("Content-Type", "application/json")
		if run {
			req.Header.Set(runTokenHeader, wikiAgentRunToken)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		return response
	}
	heartbeat := func() []realtime.CanvasSession {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"sessionId": memberSession, "zoom": 1, "focused": true,
			"view": map[string]float64{"x": 100, "y": 200, "width": 1000, "height": 600},
		})
		if err != nil {
			t.Fatal(err)
		}
		response := call(
			http.MethodPut,
			"/artifacts/"+board.String()+"/whiteboard/presence",
			string(body),
			false,
		)
		if response.Code != http.StatusOK {
			t.Fatalf("presence = %d: %s", response.Code, response.Body.String())
		}
		var result struct {
			Sessions []realtime.CanvasSession `json:"sessions"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Sessions
	}
	agent := func(sessions []realtime.CanvasSession) realtime.CanvasSession {
		t.Helper()
		if len(sessions) != 2 {
			t.Fatalf("sessions = %+v; want the member and the Agent", sessions)
		}
		for _, session := range sessions {
			if session.RunID != nil {
				if session.SessionID != f.runID.String() || *session.RunID != f.runID ||
					session.Actor.Kind != protocol.ActorKindAgent || !session.Pointing {
					t.Fatalf("Agent session = %+v; want a pointing, Run-bound Agent", session)
				}
				return session
			}
		}
		t.Fatalf("no Agent session in %+v", sessions)
		return realtime.CanvasSession{}
	}
	if sessions := heartbeat(); len(sessions) != 1 || sessions[0].View == nil || !sessions[0].Focused {
		t.Fatalf("member session = %+v; want its view and focus kept", sessions)
	}
	runPath := "/runs/" + f.runID.String() + "/agent-artifacts/" + board.String() + "/whiteboard"
	if response := call(http.MethodPost, runPath+"/context", `{}`, true); response.Code != http.StatusOK {
		t.Fatalf("context = %d: %s", response.Code, response.Body.String())
	}
	if read := agent(heartbeat()); read.X != 600 || read.Y != 500 {
		t.Fatalf("Agent cursor after a read = %v,%v; want the center of the member's view", read.X, read.Y)
	}
	operations := `{"requestKey":"` + uuid.NewString() + `","commands":[{"type":"delete_item","id":"` +
		note.String() + `","expectedRevision":1}]}`
	if response := call(http.MethodPost, runPath+"/operations", operations, true); response.Code != http.StatusOK {
		t.Fatalf("operations = %d: %s", response.Code, response.Body.String())
	}
	edited := agent(heartbeat())
	if edited.X != 400 || edited.Y != 450 || len(edited.SelectionIDs) != 1 || edited.SelectionIDs[0] != note {
		t.Fatalf("Agent after an edit = %+v; want its cursor on the touched note and the note selected", edited)
	}
	if response := call(
		http.MethodPut,
		runPath+"/presence",
		`{"sessionId":"`+f.runID.String()+`","zoom":1}`,
		true,
	); response.Code != http.StatusNotFound {
		t.Fatalf("Run presence route = %d, want 404", response.Code)
	}
}

// An Agent shows where it works, or where the person it works for looks or
// looked a moment ago; with no such place it stays hidden rather than
// appearing at the Board's origin.
func TestWhiteboardAgentAppearsOnlyWhereItWorks(t *testing.T) {
	t.Parallel()
	f := newWikiAgentFixture("direct")
	h := f.handler()
	erased := uuid.New()
	h.BoardService = &boardHTTPStub{
		agent: f.origin.AgentID,
		items: []canvas.BoardItem{{
			ID:            erased,
			BoardGeometry: canvas.BoardGeometry{X: 300, Y: 400, Width: 200, Height: 100},
			Deleted:       true,
		}},
	}
	presence := realtime.NewCanvasPresence(nil)
	h.CanvasPresence = presence
	h = New(*h)
	base := "/api/v1/companies/" + f.principal.CompanyID.String()
	call := func(method, path, body string, run bool) {
		t.Helper()
		req := httptest.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer access-token")
		req.Header.Set("Content-Type", "application/json")
		if run {
			req.Header.Set(runTokenHeader, wikiAgentRunToken)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", method, path, response.Code, response.Body.String())
		}
	}
	agent := func(board uuid.UUID) *realtime.CanvasSession {
		t.Helper()
		sessions, err := presence.Sessions(
			t.Context(),
			f.principal.CompanyID.String()+":whiteboard:"+board.String(),
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, session := range sessions {
			if session.RunID != nil {
				return &session
			}
		}
		return nil
	}
	runPath := func(board uuid.UUID) string {
		return "/runs/" + f.runID.String() + "/agent-artifacts/" + board.String() + "/whiteboard"
	}

	unseen := uuid.New()
	call(http.MethodPost, runPath(unseen)+"/context", `{}`, true)
	if session := agent(unseen); session != nil {
		t.Fatalf("Agent on a Board nobody looks at = %+v; want it hidden until it works somewhere", session)
	}
	operations := `{"requestKey":"` + uuid.NewString() + `","commands":[{"type":"delete_item","id":"` +
		erased.String() + `","expectedRevision":1}]}`
	call(http.MethodPost, runPath(unseen)+"/operations", operations, true)
	if session := agent(unseen); session == nil || session.X != 400 || session.Y != 450 ||
		len(session.SelectionIDs) != 0 {
		t.Fatalf("Agent after erasing = %+v; want its cursor where it erased, selecting nothing", session)
	}

	left := uuid.New()
	view := `{"sessionId":"` + uuid.NewString() + `","zoom":1,"view":{"x":0,"y":0,"width":800,"height":400}`
	call(http.MethodPut, "/artifacts/"+left.String()+"/whiteboard/presence", view+`}`, false)
	call(http.MethodPut, "/artifacts/"+left.String()+"/whiteboard/presence", view+`,"leave":true}`, false)
	call(http.MethodPost, runPath(left)+"/context", `{}`, true)
	if session := agent(left); session == nil || session.X != 400 || session.Y != 200 {
		t.Fatalf("Agent after its person left = %+v; want it where they last looked", session)
	}
}

func (s *boardHTTPStub) Mutate(
	_ context.Context,
	p auth.UserPrincipal,
	scope canvas.BoardScope,
	_ canvas.BoardBatch,
) (canvas.BoardMutation, error) {
	s.calls++
	s.board = scope.BoardID
	s.run = scope.RunID
	s.company = p.CompanyID
	return canvas.BoardMutation{
		Sequence: 3,
		IDs:      map[string]uuid.UUID{},
		Items:    append([]canvas.BoardItem{}, s.items...),
		Edges:    []canvas.BoardEdge{},
		Removed:  []uuid.UUID{},
	}, nil
}

func TestWhiteboardCollaborationRoutesSeparateMemberAndRunAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, route, token string
		kind               string
		want               int
	}{
		{"member", "artifacts", "", "direct", 200},
		{"Agent direct", "runs", wikiAgentRunToken, "direct", 200},
		{"Agent task", "runs", wikiAgentRunToken, "task", 200},
		{"Agent channel", "runs", wikiAgentRunToken, "channel", 200},
		{"missing proof", "runs", "", "direct", 401},
		{"invalid proof", "runs", "brt_invalid", "direct", 401},
		{"schedule proof", "runs", wikiAgentRunToken, "schedule", 401},
		{"proof on member route", "artifacts", wikiAgentRunToken, "direct", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newWikiAgentFixture(tc.kind)
			s := &boardHTTPStub{}
			h := fixture.handler()
			h.BoardService = s
			h = New(*h)
			path := "/api/v1/companies/" + fixture.principal.CompanyID.String() + "/artifacts/"
			if tc.route == "runs" {
				path = "/api/v1/companies/" + fixture.principal.CompanyID.String() + "/runs/" + fixture.runID.String() + "/agent-artifacts/"
			}
			board := uuid.New()
			req := httptest.NewRequest(
				http.MethodPost,
				path+board.String()+"/whiteboard/operations",
				strings.NewReader(
					`{"requestKey":"99987986-33e6-4ec7-b7d4-a19bc936ce48","commands":[{"type":"create_item","draftId":"note","itemType":"sticky"}]}`,
				),
			)
			req.Header.Set("Authorization", "Bearer access-token")
			req.Header.Set("Content-Type", "application/json")
			if tc.token != "" {
				req.Header.Set(runTokenHeader, tc.token)
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("status=%d want %d: %s", response.Code, tc.want, response.Body.String())
			}
			if tc.want != 200 {
				if s.calls != 0 {
					t.Fatal("rejected proof reached the service")
				}
				return
			}
			wantRun := uuid.Nil
			if tc.route == "runs" {
				wantRun = fixture.runID
			}
			if s.calls != 1 || s.board != board || s.run != wantRun ||
				s.company != fixture.principal.CompanyID {
				t.Fatalf("wrong scope: %+v", s)
			}
		})
	}
}

func TestWhiteboardHTTPRejectsInvalidCommandsBeforeService(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"valid", "/boards/6b900286-ad8a-4da4-94be-fb272025ff73", `{"requestKey":"99987986-33e6-4ec7-b7d4-a19bc936ce48","commands":[{"type":"create_item","draftId":"note","itemType":"sticky"}]}`, 200},
		{"unknown field", "/boards/6b900286-ad8a-4da4-94be-fb272025ff73", `{"requestKey":"99987986-33e6-4ec7-b7d4-a19bc936ce48","commands":[{"type":"create_item"}],"plainText":"forged"}`, 400},
		{"missing key", "/boards/6b900286-ad8a-4da4-94be-fb272025ff73", `{"commands":[{"type":"create_item"}]}`, 422},
		{"malformed id", "/boards/invalid", `{}`, 404},
		{"unknown query", "/boards/6b900286-ad8a-4da4-94be-fb272025ff73?force=true", `{}`, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &boardHTTPStub{}
			h := Handler{BoardService: s}
			r := gin.New()
			r.Use(func(c *gin.Context) {
				p := auth.UserPrincipal{
					SessionID: uuid.New(),
					AuthKind:  auth.TokenKindSession,
					UserID:    uuid.MustParse("de04603c-f870-4b5f-ab9e-b177010fbd87"),
					CompanyID: uuid.MustParse("f3f30e8e-ceea-4702-8a1e-637e66777412"),
					Role:      auth.CompanyRoleMember,
				}
				c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), p))
			})
			r.POST("/boards/:artifactId", h.mutateWhiteboard)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == 200 && s.calls != 1 || tc.want != 200 && s.calls != 0 {
				t.Fatalf("service calls=%d", s.calls)
			}
		})
	}
}

func (s *boardVisualHTTPStub) AttachFile(
	_ context.Context,
	_ auth.UserPrincipal,
	_ uuid.UUID,
	input canvas.BoardFileInput,
) (canvas.BoardFile, error) {
	s.calls++
	return canvas.BoardFile{AttachmentID: input.AttachmentID, ContentType: "image/png"}, s.err
}

func TestWhiteboardFileAttachBoundary(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body, proof string
		want              int
		calls             int
	}{
		{"image", `{"attachmentId":"12345678-1234-4234-8234-123456789abc"}`, "", 200, 1},
		{"missing attachment", `{}`, "", 422, 0},
		{"unknown field", `{"attachmentId":"12345678-1234-4234-8234-123456789abc","url":"https://example.test"}`, "", 400, 0},
		{"run proof", `{"attachmentId":"12345678-1234-4234-8234-123456789abc"}`, wikiAgentRunToken, 403, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newWikiAgentFixture("direct")
			h := f.handler()
			media := &boardVisualHTTPStub{}
			h.BoardService, h.BoardMediaService = &boardHTTPStub{}, media
			h = New(*h)
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/companies/"+f.principal.CompanyID.String()+"/artifacts/"+uuid.NewString()+"/whiteboard/files",
				strings.NewReader(tt.body),
			)
			request.Header.Set("Authorization", "Bearer access-token")
			request.Header.Set("Content-Type", "application/json")
			if tt.proof != "" {
				request.Header.Set(runTokenHeader, tt.proof)
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != tt.want || media.calls != tt.calls {
				t.Fatalf(
					"status/calls = %d/%d, want %d/%d: %s",
					response.Code,
					media.calls,
					tt.want,
					tt.calls,
					response.Body.String(),
				)
			}
		})
	}
}

func (s *boardVisualHTTPStub) FileDownload(
	_ context.Context,
	_ auth.UserPrincipal,
	_ canvas.BoardScope,
	file uuid.UUID,
	_ bool,
	known uuid.UUID,
) (attachment.DownloadResult, error) {
	s.calls++
	result := attachment.DownloadResult{Attachment: attachment.Response{
		ID: file, Filename: "photo.png", ContentType: "image/png", SizeBytes: 3,
	}}
	if known == file {
		result.NotModified = true
		return result, s.err
	}
	result.Body = io.NopCloser(strings.NewReader("PNG"))
	return result, s.err
}

// A member's browser may keep a Board file, but reuses it only after the
// server resolves current Board access again.
func TestWhiteboardFileDownloadsRevalidate(t *testing.T) {
	t.Parallel()
	f := newWikiAgentFixture("direct")
	h := f.handler()
	media := &boardVisualHTTPStub{}
	h.BoardService, h.BoardMediaService = &boardHTTPStub{}, media
	h = New(*h)
	file := uuid.New()
	path := "/api/v1/companies/" + f.principal.CompanyID.String() + "/artifacts/" + uuid.NewString() +
		"/whiteboard/files/" + file.String()
	download := func(etag string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer access-token")
		if etag != "" {
			request.Header.Set("If-None-Match", etag)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}
	first := download("")
	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || first.Body.String() != "PNG" || etag != `"`+file.String()+`"` ||
		first.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("download = %d %q, ETag %q, Cache-Control %q", first.Code, first.Body.String(), etag,
			first.Header().Get("Cache-Control"))
	}
	again := download(etag)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 || again.Header().Get("ETag") != etag ||
		media.calls != 2 {
		t.Fatalf("revalidation = %d %q, ETag %q, service calls %d", again.Code, again.Body.String(),
			again.Header().Get("ETag"), media.calls)
	}
}

func (s *boardVisualHTTPStub) Preview(
	_ context.Context,
	_ auth.UserPrincipal,
	scope canvas.BoardScope,
	known int64,
) (canvas.BoardPreview, error) {
	s.calls++
	s.scope = scope
	result := canvas.BoardPreview{Sequence: 7, NotModified: known == 7}
	if s.empty {
		result.Sequence = 0
	} else if !result.NotModified {
		result.Image = &canvas.BoardVisualRegion{MIMEType: "image/png", Data: []byte("PNG")}
	}
	return result, s.err
}

// A Whiteboard card's preview is drawn again only when the Board changed: the
// ETag names the sequence it shows. An empty Board has no preview.
func TestWhiteboardPreviewRevalidatesBySequence(t *testing.T) {
	t.Parallel()
	f := newWikiAgentFixture("direct")
	h := f.handler()
	media := &boardVisualHTTPStub{}
	h.BoardService, h.BoardMediaService = &boardHTTPStub{}, media
	h = New(*h)
	path := "/api/v1/companies/" + f.principal.CompanyID.String() + "/artifacts/" + uuid.NewString() +
		"/whiteboard/preview"
	preview := func(etag string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer access-token")
		if etag != "" {
			request.Header.Set("If-None-Match", etag)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}
	first := preview(`"preview-0-7"`)
	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || first.Body.String() != "PNG" || etag != `"preview-1-7"` ||
		first.Header().Get("Content-Type") != "image/png" ||
		first.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf(
			"preview = %d %q, ETag %q, Content-Type %q, Cache-Control %q",
			first.Code,
			first.Body.String(),
			etag,
			first.Header().Get("Content-Type"),
			first.Header().Get("Cache-Control"),
		)
	}
	if again := preview("W/" + etag); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Fatalf("revalidation = %d %q", again.Code, again.Body.String())
	}
	media.empty = true
	if empty := preview(""); empty.Code != http.StatusNoContent || empty.Body.Len() != 0 {
		t.Fatalf("empty Board preview = %d %q", empty.Code, empty.Body.String())
	}
}

func (s *boardHTTPStub) Snapshot(
	_ context.Context,
	_ auth.UserPrincipal,
	scope canvas.BoardScope,
	_ int64,
) (canvas.BoardSnapshot, error) {
	s.calls++
	items := make([]canvas.BoardItem, 200)
	for i := range items {
		items[i] = canvas.BoardItem{ID: uuid.New(), Type: "sticky", Text: strings.Repeat("idea ", 20)}
	}
	return canvas.BoardSnapshot{
		Board: canvas.Board{ID: scope.BoardID}, Full: true, Items: items,
		Edges: []canvas.BoardEdge{}, Removed: []uuid.UUID{},
	}, nil
}

// A whole-Board snapshot is large and repetitive; a client that accepts gzip
// receives it compressed.
func TestWhiteboardSnapshotsAreCompressed(t *testing.T) {
	t.Parallel()
	f := newWikiAgentFixture("direct")
	h := f.handler()
	h.BoardService = &boardHTTPStub{}
	h = New(*h)
	path := "/api/v1/companies/" + f.principal.CompanyID.String() + "/artifacts/" + uuid.NewString() + "/whiteboard"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Accept-Encoding", "gzip, br")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Encoding") != "gzip" ||
		!strings.Contains(response.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("snapshot = %d, encoding %q, vary %q", response.Code,
			response.Header().Get("Content-Encoding"), response.Header().Get("Vary"))
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot canvas.BoardSnapshot
	if err = json.NewDecoder(reader).Decode(&snapshot); err != nil || len(snapshot.Items) != 200 {
		t.Fatalf("decoded snapshot = %d items, %v", len(snapshot.Items), err)
	}
	plain := httptest.NewRequest(http.MethodGet, path, nil)
	plain.Header.Set("Authorization", "Bearer access-token")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, plain)
	if response.Header().Get("Content-Encoding") != "" || !json.Valid(response.Body.Bytes()) {
		t.Fatalf("uncompressed snapshot = encoding %q", response.Header().Get("Content-Encoding"))
	}
}

type boardViewHTTPStub struct {
	boardHTTPStub
	focus canvas.BoardFocus
	input canvas.BoardAgentViewInput
}

func (s *boardViewHTTPStub) AgentView(
	_ context.Context,
	_ auth.UserPrincipal,
	_ canvas.BoardScope,
	input canvas.BoardAgentViewInput,
	focus canvas.BoardFocus,
) (canvas.BoardAgentView, error) {
	s.focus, s.input = focus, input
	area := canvas.BoardVisualBounds{X: 0, Y: 100, Width: 1250, Height: 750}
	return canvas.BoardAgentView{Sequence: 9, Text: "Board", Refs: []canvas.BoardAgentRef{}, Area: &area}, nil
}

func (s *boardVisualHTTPStub) RectVisual(
	_ context.Context,
	_ auth.UserPrincipal,
	scope canvas.BoardScope,
	area canvas.BoardVisualBounds,
	maxPixels int,
) (canvas.BoardVisualRegion, error) {
	s.calls++
	s.scope = scope
	if area.Width <= 0 || maxPixels != 1024 && maxPixels != 768 {
		return canvas.BoardVisualRegion{}, canvas.ErrBoardInvalid
	}
	return canvas.BoardVisualRegion{Bounds: area, MIMEType: "image/png", Data: []byte("PNG")}, s.err
}

// The Agent reads where the person it works for is looking: the view and
// selection their browser reports, and an image of exactly that area.
func TestWhiteboardAgentViewReadsWhereTheRequesterLooks(t *testing.T) {
	t.Parallel()
	f := newWikiAgentFixture("direct")
	h := f.handler()
	views := &boardViewHTTPStub{boardHTTPStub: boardHTTPStub{agent: f.origin.AgentID}}
	visuals := &boardVisualHTTPStub{}
	h.BoardService, h.BoardMediaService = views, visuals
	h.CanvasPresence = realtime.NewCanvasPresence(nil)
	h = New(*h)
	board, note := uuid.New(), uuid.New()
	base := "/api/v1/companies/" + f.principal.CompanyID.String()
	call := func(method, path, body, proof string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer access-token")
		req.Header.Set("Content-Type", "application/json")
		if proof != "" {
			req.Header.Set(runTokenHeader, proof)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		return response
	}
	heartbeat := `{"sessionId":"` + uuid.NewString() + `","zoom":1,"focused":true,` +
		`"view":{"x":100,"y":200,"width":1000,"height":600},"selectionIds":["` + note.String() + `"]}`
	if response := call(
		http.MethodPut,
		"/artifacts/"+board.String()+"/whiteboard/presence",
		heartbeat,
		"",
	); response.Code != 200 {
		t.Fatalf("presence = %d: %s", response.Code, response.Body.String())
	}
	path := "/runs/" + f.runID.String() + "/agent-artifacts/" + board.String() + "/whiteboard/view"
	if response := call(http.MethodPost, path, `{}`, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("view without a Run proof = %d, want 401", response.Code)
	}
	response := call(http.MethodPost, path, `{}`, wikiAgentRunToken)
	if response.Code != http.StatusOK {
		t.Fatalf("view = %d: %s", response.Code, response.Body.String())
	}
	looked := canvas.BoardVisualBounds{X: 100, Y: 200, Width: 1000, Height: 600}
	if !views.focus.Present || views.focus.View != looked || visuals.calls != 0 {
		t.Fatalf("focus = %+v, images = %d; want the member's view and no image", views.focus, visuals.calls)
	}
	if len(views.focus.SelectionIDs) != 1 || views.focus.SelectionIDs[0] != note {
		t.Fatalf("focus selection = %v; want the member's selected note", views.focus.SelectionIDs)
	}
	response = call(http.MethodPost, path, `{"image":true,"area":"view"}`, wikiAgentRunToken)
	var got canvas.BoardAgentView
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || visuals.calls != 1 || visuals.scope.RunID != f.runID ||
		got.Image == nil || string(got.Image.Data) != "PNG" {
		t.Fatalf("view with an image = %d %+v", response.Code, got)
	}
	if response := call(http.MethodPost, path, `{"area":"nearby"}`, wikiAgentRunToken); response.Code != 422 {
		t.Fatalf("unknown area = %d, want 422", response.Code)
	}
}

type boardComposeHTTPStub struct {
	boardHTTPStub
	focus canvas.BoardFocus
	input canvas.BoardComposeInput
	note  uuid.UUID
}

func (s *boardComposeHTTPStub) Compose(
	_ context.Context,
	_ auth.UserPrincipal,
	scope canvas.BoardScope,
	input canvas.BoardComposeInput,
	focus canvas.BoardFocus,
) (canvas.BoardComposeResult, error) {
	s.focus, s.input, s.run = focus, input, scope.RunID
	bounds := canvas.BoardVisualBounds{X: 200, Y: 300, Width: 600, Height: 400}
	return canvas.BoardComposeResult{
		Mutation: canvas.BoardMutation{
			Sequence: 4,
			IDs:      map[string]uuid.UUID{"n1": s.note},
			Items: []canvas.BoardItem{
				{
					ID:            s.note,
					Type:          "sticky",
					BoardGeometry: canvas.BoardGeometry{X: 200, Y: 300, Width: 240, Height: 190},
				},
			},
			Edges:   []canvas.BoardEdge{},
			Removed: []uuid.UUID{},
		},
		Bounds:    &bounds,
		Placement: canvas.BoardPlacementTypeView,
	}, nil
}

// Composing reads where the requester looks, then shows the Agent there
// like a person: its cursor on the new work, the work selected and its
// work area outlined, with an image of the result when asked.
func TestWhiteboardComposeLandsWhereTheRequesterLooks(t *testing.T) {
	t.Parallel()
	f := newWikiAgentFixture("direct")
	h := f.handler()
	composer := &boardComposeHTTPStub{boardHTTPStub: boardHTTPStub{agent: f.origin.AgentID}, note: uuid.New()}
	visuals := &boardVisualHTTPStub{}
	h.BoardService, h.BoardMediaService = composer, visuals
	h.CanvasPresence = realtime.NewCanvasPresence(nil)
	h = New(*h)
	board := uuid.New()
	base := "/api/v1/companies/" + f.principal.CompanyID.String()
	call := func(method, path, body, proof string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, base+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer access-token")
		req.Header.Set("Content-Type", "application/json")
		if proof != "" {
			req.Header.Set(runTokenHeader, proof)
		}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		return response
	}
	heartbeat := `{"sessionId":"` + uuid.NewString() + `","zoom":1,"view":{"x":0,"y":0,"width":1200,"height":800}}`
	if response := call(
		http.MethodPut,
		"/artifacts/"+board.String()+"/whiteboard/presence",
		heartbeat,
		"",
	); response.Code != 200 {
		t.Fatalf("presence = %d: %s", response.Code, response.Body.String())
	}
	path := "/runs/" + f.runID.String() + "/agent-artifacts/" + board.String() + "/whiteboard/compose"
	body := `{"requestKey":"` + uuid.NewString() + `","preview":true,` +
		`"ops":[{"type":"notes","items":[{"key":"n1","text":"Idea"}]}]}`
	response := call(http.MethodPost, path, body, wikiAgentRunToken)
	if response.Code != http.StatusOK {
		t.Fatalf("compose = %d: %s", response.Code, response.Body.String())
	}
	if !composer.focus.Present || composer.focus.View.Width != 1200 || composer.run != f.runID ||
		len(composer.input.Ops) != 1 || composer.input.Ops[0].Items[0].Key != "n1" {
		t.Fatalf("compose got focus %+v, input %+v", composer.focus, composer.input)
	}
	var got canvas.BoardComposeResult
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Preview == nil || visuals.calls != 1 {
		t.Fatalf("preview = %+v after %d renders; want an image of the placed work", got.Preview, visuals.calls)
	}
	response = call(http.MethodPut, "/artifacts/"+board.String()+"/whiteboard/presence", heartbeat, "")
	var presence struct {
		Sessions []realtime.CanvasSession `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &presence); err != nil {
		t.Fatal(err)
	}
	var agent *realtime.CanvasSession
	for i := range presence.Sessions {
		if presence.Sessions[i].RunID != nil {
			agent = &presence.Sessions[i]
		}
	}
	if agent == nil || agent.X != 500 || agent.Y != 500 || agent.WorkArea == nil || agent.WorkArea.Width != 600 ||
		len(agent.SelectionIDs) != 1 || agent.SelectionIDs[0] != composer.note {
		t.Fatalf(
			"Agent after composing = %+v; want its cursor on the work, the work selected and outlined",
			agent,
		)
	}
	if response := call(
		http.MethodPost,
		path,
		`{"requestKey":"`+uuid.NewString()+`","ops":[]}`,
		wikiAgentRunToken,
	); response.Code != 422 {
		t.Fatalf("compose without ops = %d, want 422", response.Code)
	}
}
