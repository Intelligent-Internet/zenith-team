//go:build integration

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/artifact"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestArtifactGrantRoutesControlLivePageAccess(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	q := store.Queries()
	page, err := q.CreateWikiPage(
		ctx,
		generated.CreateWikiPageParams{
			CompanyID:       f.CompanyA,
			Type:            "page",
			OwnerUserID:     uuid.NullUUID{UUID: f.UserA, Valid: true},
			Title:           "Shared Page",
			CreatedByUserID: f.UserA,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	root := wiki.BlocksFromMarkdown("Shared content")
	if err := wiki.AllocateBlockTreeInTx(ctx, q, f.CompanyA, page, &root); err != nil {
		t.Fatal(err)
	}
	state, err := wiki.EncodeBlocks(root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateCanvasDraft(
		ctx,
		generated.CreateCanvasDraftParams{
			CompanyID:     f.CompanyA,
			PageID:        page,
			State:         state,
			Content:       content,
			Markdown:      "Shared content",
			SchemaVersion: 1,
		},
	); err != nil {
		t.Fatal(err)
	}
	h := Handler{
		ArtifactService: artifact.NewArtifactService(store, bus),
		PageService:     canvas.NewPageService(store, bus),
	}
	router := gin.New()
	router.GET("/artifacts/:artifactId/access", h.getArtifactContentAccess)
	router.PATCH("/artifacts/:artifactId/access", h.changeArtifactContentAccess)
	router.GET("/pages", h.listPages)
	router.GET("/pages/:pageId/document", h.readPageDocument)
	router.POST("/pages/:pageId/document/ids", h.reservePageBlockIDs)
	owner := auth.UserPrincipal{
		CompanyID: f.CompanyA,
		UserID:    f.UserA,
		Role:      auth.CompanyRoleOwner,
		AuthKind:  auth.TokenKindSession,
		SessionID: uuid.New(),
	}
	reader := owner
	reader.UserID, reader.Role = f.UserB, auth.CompanyRoleMember
	accessPath := "/artifacts/" + page.String() + "/access"
	documentPath := "/pages/" + page.String() + "/document"
	request := func(p auth.UserPrincipal, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(auth.WithUserPrincipal(req.Context(), p))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, response.Code, response.Body, want)
		}
		return response
	}
	request(reader, http.MethodGet, documentPath, "", http.StatusNotFound)
	var access artifact.Access
	response := request(owner, http.MethodGet, accessPath, "", http.StatusOK)
	if err := json.Unmarshal(response.Body.Bytes(), &access); err != nil {
		t.Fatal(err)
	}
	patch := func(permission string) {
		t.Helper()
		body := fmt.Sprintf(
			`{"expectedRevision":%d,"grant":{"userId":%q,"permission":%s}}`,
			access.Revision,
			f.UserB.String(),
			permission,
		)
		response := request(owner, http.MethodPatch, accessPath, body, http.StatusOK)
		if err := json.Unmarshal(response.Body.Bytes(), &access); err != nil {
			t.Fatal(err)
		}
	}
	patch(`"view"`)
	response = request(reader, http.MethodGet, documentPath, "", http.StatusOK)
	var document canvas.Document
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.CanEdit || document.PageID != page {
		t.Fatalf("view-only document = %+v", document)
	}
	request(reader, http.MethodPost, documentPath+"/ids", `{"count":1}`, http.StatusForbidden)
	response = request(reader, http.MethodGet, "/pages", "", http.StatusOK)
	if !strings.Contains(response.Body.String(), page.String()) {
		t.Fatalf("discovery omits shared Page: %s", response.Body)
	}
	// Revocation must be explicit: an omitted permission is not a null grant.
	request(
		owner,
		http.MethodPatch,
		accessPath,
		fmt.Sprintf(`{"expectedRevision":%d,"grant":{"userId":%q}}`, access.Revision, f.UserB.String()),
		http.StatusUnprocessableEntity,
	)
	for _, invalid := range []string{`7`, `true`, `"owner"`, `[]`} {
		request(
			owner,
			http.MethodPatch,
			accessPath,
			fmt.Sprintf(
				`{"expectedRevision":%d,"grant":{"userId":%q,"permission":%s}}`,
				access.Revision,
				f.UserB.String(),
				invalid,
			),
			http.StatusUnprocessableEntity,
		)
	}
	patch(`"edit"`)
	request(reader, http.MethodPost, documentPath+"/ids", `{"count":1}`, http.StatusOK)
	request(
		reader,
		http.MethodPatch,
		accessPath,
		fmt.Sprintf(`{"expectedRevision":%d,"visibility":"company"}`, access.Revision),
		http.StatusForbidden,
	)
	request(
		owner,
		http.MethodPatch,
		accessPath,
		`{"expectedRevision":1,"visibility":"company"}`,
		http.StatusConflict,
	)
	request(
		owner,
		http.MethodPatch,
		accessPath,
		`{"expectedRevision":0,"visibility":"company"}`,
		http.StatusUnprocessableEntity,
	)
	request(
		owner,
		http.MethodPatch,
		accessPath,
		fmt.Sprintf(`{"expectedRevision":%d,"visibility":"public"}`, access.Revision),
		http.StatusUnprocessableEntity,
	)
	patch(`null`)
	request(reader, http.MethodGet, documentPath, "", http.StatusNotFound)
	request(reader, http.MethodGet, accessPath, "", http.StatusNotFound)
	response = request(reader, http.MethodGet, "/pages", "", http.StatusOK)
	if strings.Contains(response.Body.String(), page.String()) {
		t.Fatalf("discovery retains revoked Page: %s", response.Body)
	}
}
