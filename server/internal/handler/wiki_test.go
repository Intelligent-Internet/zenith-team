package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

type wikiServiceStub struct {
	wiki.WikiService
	publishPage      func(uuid.UUID, wiki.PublishPageInput) (wiki.PageResponse, error)
	createPage       func(wiki.CreatePageInput) (wiki.PageResponse, error)
	updateMetadata   func(uuid.UUID, wiki.DraftMetadataInput) (wiki.Document, error)
	setFavorite      func(uuid.UUID, bool) error
	deleteCollection func(uuid.UUID, int64) error
}

func (s *wikiServiceStub) DeleteCollection(
	_ context.Context,
	_ auth.UserPrincipal,
	id uuid.UUID,
	revision int64,
) error {
	return s.deleteCollection(id, revision)
}

func (s *wikiServiceStub) PublishPage(
	_ context.Context,
	_ auth.UserPrincipal,
	id uuid.UUID,
	input wiki.PublishPageInput,
) (wiki.PageResponse, error) {
	return s.publishPage(id, input)
}

func (s *wikiServiceStub) CreatePage(
	_ context.Context,
	_ auth.UserPrincipal,
	input wiki.CreatePageInput,
) (wiki.PageResponse, error) {
	return s.createPage(input)
}

func (s *wikiServiceStub) UpdateDraftMetadata(
	_ context.Context,
	_ auth.UserPrincipal,
	id uuid.UUID,
	input wiki.DraftMetadataInput,
) (wiki.Document, error) {
	return s.updateMetadata(id, input)
}

func (s *wikiServiceStub) SetFavorite(_ context.Context, _ auth.UserPrincipal, id uuid.UUID, favorite bool) error {
	return s.setFavorite(id, favorite)
}

// wikiTestHandler mounts the Wiki routes for one active member whose
// wiki.publish check answers permissionErr.
func wikiTestHandler(principal auth.UserPrincipal, permissionErr error, service wiki.WikiService) *Handler {
	return New(Handler{
		AuthService: authenticatedAuthStub(principal.UserID),
		CompanyService: &companyServiceStub{
			authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
			requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				if permissionErr != nil {
					return auth.UserPrincipal{}, permissionErr
				}
				return principal, nil
			},
		},
		WikiService:          service,
		WikiAgentReadService: &wikiAgentReadServiceStub{},
	})
}

func TestDeleteWikiCollectionRequiresRevisionAndMapsConflict(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	id := uuid.New()
	calls := 0
	h := wikiTestHandler(
		principal,
		nil,
		&wikiServiceStub{deleteCollection: func(gotID uuid.UUID, revision int64) error {
			calls++
			if gotID != id {
				t.Fatalf("id = %v, want %v", gotID, id)
			}
			if revision == 2 {
				return wiki.ErrCollectionConflict
			}
			return nil
		}},
	)
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/wiki/collections/" + id.String()
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{}`, http.StatusUnprocessableEntity},
		{`{"expectedRevision":9007199254740992}`, http.StatusUnprocessableEntity},
		{`{"expectedRevision":1,"deletePages":true}`, http.StatusBadRequest},
		{`{"expectedRevision":2}`, http.StatusConflict},
		{`{"expectedRevision":1}`, http.StatusNoContent},
	} {
		result := serveBearerJSON(h, http.MethodDelete, path, []byte(tc.body))
		if result.Code != tc.status {
			t.Fatalf("body %s = %d %s, want %d", tc.body, result.Code, result.Body.String(), tc.status)
		}
	}
	if calls != 2 {
		t.Fatalf("service calls = %d, want 2", calls)
	}
}

func TestWikiPublishRequiresExactSavedDraft(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	var received []wiki.PublishPageInput
	h := wikiTestHandler(
		principal,
		nil,
		&wikiServiceStub{
			publishPage: func(_ uuid.UUID, input wiki.PublishPageInput) (wiki.PageResponse, error) {
				received = append(received, input)
				if input.ExpectedSequence == 9 {
					return wiki.PageResponse{}, wiki.ErrPageConflict
				}
				return wiki.PageResponse{}, nil
			},
		},
	)
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/wiki/pages/" + uuid.NewString() + "/publish"
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"expectedRevision":1}`, 422},
		{`{"expectedSequence":0}`, 422},
		{`{"expectedRevision":1,"expectedSequence":-1}`, 422},
		{`{"expectedRevision":1,"expectedSequence":0,"body":"unreviewed"}`, 400},
		{`{"expectedRevision":1,"expectedSequence":9}`, 409},
		{`{"expectedRevision":1,"expectedSequence":0}`, 200},
		{`{"expectedRevision":0,"expectedSequence":0}`, 200},
		{`{"expectedRevision":null,"expectedSequence":0}`, 400},
		{`{"expectedRevision":-1,"expectedSequence":0}`, 422},
		{`{"expectedRevision":9007199254740992,"expectedSequence":0}`, 422},
	} {
		result := serveBearerJSON(h, http.MethodPost, path, []byte(tc.body))
		if result.Code != tc.status {
			t.Fatalf("publish %s = %d %s, want %d", tc.body, result.Code, result.Body.String(), tc.status)
		}
	}
	if len(received) != 3 || received[1].ExpectedSequence != 0 || received[2].ExpectedRevision != 0 {
		t.Fatalf("publish inputs = %+v", received)
	}
	oldPath := strings.TrimSuffix(path, "/publish")
	if result := serveBearerJSON(h, http.MethodPatch, oldPath, []byte(`{}`)); result.Code != http.StatusNotFound {
		t.Fatalf("retired markdown update = %d", result.Code)
	}
}

func TestWikiDraftMetadataRequiresCompleteSavedMetadata(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	var received []wiki.DraftMetadataInput
	h := wikiTestHandler(
		principal,
		nil,
		&wikiServiceStub{updateMetadata: func(_ uuid.UUID, in wiki.DraftMetadataInput) (wiki.Document, error) {
			received = append(received, in)
			return wiki.Document{}, nil
		}},
	)
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/wiki/pages/" + uuid.NewString() + "/document/metadata"
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"title":"Draft","summary":"","collectionId":null}`, 422},
		{`{"expectedSequence":0,"title":"Draft","collectionId":null}`, 422},
		{`{"expectedSequence":0,"title":"Draft","summary":""}`, 422},
		{`{"expectedSequence":0,"title":"Draft","summary":"","collectionId":"invalid"}`, 422},
		{`{"expectedSequence":0,"title":"Draft","summary":"","collectionId":null,"body":"replace"}`, 400},
		{`{"expectedSequence":0,"title":"Draft","summary":"","collectionId":null}`, 200},
	} {
		result := serveBearerJSON(h, http.MethodPatch, path, []byte(tc.body))
		if result.Code != tc.status {
			t.Fatalf("metadata %s = %d %s, want %d", tc.body, result.Code, result.Body.String(), tc.status)
		}
	}
	if len(received) != 1 || received[0].CollectionID != nil || received[0].ExpectedSequence != 0 {
		t.Fatalf("metadata inputs = %+v", received)
	}
}

func TestWikiFavoriteRequiresTheFavoriteKey(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	var received []bool
	h := wikiTestHandler(principal, nil, &wikiServiceStub{
		setFavorite: func(_ uuid.UUID, favorite bool) error {
			received = append(received, favorite)
			return nil
		},
	})
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/wiki/pages/" + uuid.NewString() + "/favorite"
	if result := serveBearerJSON(
		h,
		http.MethodPut,
		path,
		[]byte(`{}`),
	); result.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(result.Body.String(), "favorite") {
		t.Fatalf("empty favorite = %d %s", result.Code, result.Body.String())
	}
	if result := serveBearerJSON(
		h,
		http.MethodPut,
		path,
		[]byte(`{"favorite":false}`),
	); result.Code != http.StatusNoContent {
		t.Fatalf("unfavorite = %d %s", result.Code, result.Body.String())
	}
	if len(received) != 1 || received[0] {
		t.Fatalf("service favorites = %v", received)
	}
}

// The documented limits count characters, so the request cap must admit the
// largest valid page however the client escapes it: here every character is
// an astral rune sent as a \u surrogate pair, 12 bytes each.
func TestWikiPageImportAdmitsTheLargestEscapedPage(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	var received []wiki.CreatePageInput
	h := wikiTestHandler(principal, nil, &wikiServiceStub{
		createPage: func(input wiki.CreatePageInput) (wiki.PageResponse, error) {
			received = append(received, input)
			return wiki.PageResponse{}, nil
		},
	})
	const astral = `\ud83d\ude00`
	body := `{"title":"` + strings.Repeat(astral, 240) +
		`","body":"` + strings.Repeat(astral, 262144) +
		`","summary":"` + strings.Repeat(astral, 2048) + `","collectionId":null}`
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/wiki/pages"
	result := serveBearerJSON(h, http.MethodPost, path, []byte(body))
	if result.Code != http.StatusCreated {
		t.Fatalf("largest page (%d bytes) = %d %s", len(body), result.Code, result.Body.String())
	}
	if len(received) != 1 || utf8.RuneCountInString(received[0].Body) != 262144 {
		t.Fatalf("service did not receive the full body")
	}
}

// Writes need wiki.publish (owner/admin); a member is refused by the route
// before its body is read, and the service never hears about it.
func TestWikiWritesRefuseMembersWithoutPublish(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	h := wikiTestHandler(principal, company.ErrForbidden, &wikiServiceStub{})
	root := "/api/v1/companies/" + principal.CompanyID.String() + "/wiki"
	page := root + "/pages/" + uuid.NewString()
	for _, request := range []struct{ method, path string }{
		{http.MethodPost, root + "/collections"},
		{http.MethodPatch, root + "/collections/" + uuid.NewString()},
		{http.MethodDelete, root + "/collections/" + uuid.NewString()},
		{http.MethodPost, root + "/pages"},
		{http.MethodGet, page + "/document"},
		{http.MethodPost, page + "/document"},
		{http.MethodPost, page + "/document/ids"},
		{http.MethodPatch, page + "/document/metadata"},
		{http.MethodPost, page + "/publish"},
		{http.MethodPost, page + "/archive"},
		{http.MethodPost, page + "/restore"},
		{http.MethodPost, page + "/restore-revision"},
	} {
		result := serveBearerJSON(h, request.method, request.path, []byte(`{}`))
		if result.Code != http.StatusForbidden ||
			!strings.Contains(result.Body.String(), `"code":"company_permission_required"`) {
			t.Fatalf(
				"member %s %s = %d %s",
				request.method,
				request.path,
				result.Code,
				result.Body.String(),
			)
		}
	}
}

// Every Wiki route is human-only in V1: a request carrying a Run token is
// refused whatever the route, so an Agent gains no Wiki authority.
func TestWikiRoutesRefuseRunTokenRequests(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	h := wikiTestHandler(principal, nil, &wikiServiceStub{})
	walked := 0
	for _, route := range h.router.Routes() {
		if !strings.Contains(route.Path, "/wiki/") {
			continue
		}
		walked++
		path := strings.NewReplacer(
			":companyId", principal.CompanyID.String(),
			":pageId", uuid.NewString(),
			":collectionId", uuid.NewString(),
		).Replace(route.Path)
		request := httptest.NewRequest(route.Method, path, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer access-token")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(middleware.HeaderRunToken, "run-token")
		result := httptest.NewRecorder()
		h.ServeHTTP(result, request)
		if result.Code != http.StatusForbidden ||
			!strings.Contains(result.Body.String(), `"code":"human_actor_required"`) {
			t.Fatalf("run token %s %s = %d %s", route.Method, route.Path, result.Code, result.Body.String())
		}
	}
	if walked != 20 {
		t.Fatalf("walked %d Wiki routes, want 20", walked)
	}
}

// Wiki pages begin at revision zero; collections still begin at one.
func TestWikiPageRevisionBounds(t *testing.T) {
	t.Parallel()
	for _, operation := range []struct {
		name  string
		bind  func(*gin.Context) error
		extra string
	}{
		{"publish", func(c *gin.Context) error { _, err := bindRequest[wikiPublishRequest](c, 4096); return err }, `,"expectedSequence":0`},
		{"archive and restore", func(c *gin.Context) error { _, err := bindRequest[wikiPageRevisionRequest](c, 4096); return err }, ""},
		{"restore revision", func(c *gin.Context) error { _, err := bindRequest[wikiRestoreRevisionRequest](c, 4096); return err }, `,"expectedSequence":0,"revisionId":"01980000-0000-7000-8000-000000000001"`},
	} {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				revision string
				want     int
			}{
				{"", 422},
				{"null", 400},
				{"-1", 422},
				{"0", 200},
				{"1", 200},
				{"9007199254740991", 200},
				{"9007199254740992", 422},
			} {
				body := "{"
				if tc.revision != "" {
					body += `"expectedRevision":` + tc.revision
				}
				if operation.extra != "" {
					if tc.revision == "" {
						body += strings.TrimPrefix(operation.extra, ",")
					} else {
						body += operation.extra
					}
				}
				body += "}"
				router := gin.New()
				router.POST("/", func(c *gin.Context) {
					if err := operation.bind(c); err != nil {
						writeRequestError(c, err)
						return
					}
					c.Status(http.StatusOK)
				})
				request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != tc.want {
					t.Errorf(
						"body %s = %d %s, want %d",
						body,
						response.Code,
						response.Body.String(),
						tc.want,
					)
				}
			}
		})
	}
}
