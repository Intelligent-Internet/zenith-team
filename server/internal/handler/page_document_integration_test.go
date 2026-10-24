//go:build integration

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

func TestPageDocumentRoutesUseArtifactAuthority(t *testing.T) {
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
	pages := canvas.NewPageService(store, bus)
	owner := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	page, err := pages.CreatePage(ctx, owner, f.ProjectA, canvas.PageCreateInput{
		RequestKey: uuid.New(), Title: "Direct Page", Content: wiki.BlocksFromMarkdown("Shared text"),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := Handler{PageService: pages}
	router := gin.New()
	router.GET("/pages/:pageId/document", h.readPageDocument)
	router.POST("/pages/:pageId/document/ids", h.reservePageBlockIDs)
	router.GET("/pages/:pageId/document/history", h.documentHistory)
	request := func(p auth.UserPrincipal, method, suffix, body string) *httptest.ResponseRecorder {
		t.Helper()
		p.SessionID = uuid.New()
		p.AuthKind = auth.TokenKindSession
		req := httptest.NewRequest(
			method,
			"/pages/"+page.PageID.String()+"/document"+suffix,
			strings.NewReader(body),
		)
		req = req.WithContext(auth.WithUserPrincipal(req.Context(), p))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	response := request(owner, http.MethodGet, "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("read = %d, %s", response.Code, response.Body)
	}
	var document canvas.Document
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.PageID != page.PageID || !document.CanEdit || !strings.Contains(document.Markdown, "Shared text") {
		t.Fatalf("direct document = %+v", document)
	}
	for _, operation := range []struct{ method, suffix, body string }{
		{http.MethodPost, "/ids", `{"count":2}`},
		{http.MethodGet, "/history", ""},
	} {
		if response := request(
			owner,
			operation.method,
			operation.suffix,
			operation.body,
		); response.Code != http.StatusOK {
			t.Fatalf("%s = %d, %s", operation.suffix, response.Code, response.Body)
		}
	}
	// A valid Company membership alone cannot open Project work by its Page ID.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	reader := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserB, Role: auth.CompanyRoleMember}
	if response := request(reader, http.MethodGet, "", ""); response.Code != http.StatusNotFound {
		t.Fatalf("ungranted member read = %d, %s", response.Code, response.Body)
	}
	foreign := owner
	foreign.CompanyID = f.CompanyB
	if response := request(foreign, http.MethodGet, "", ""); response.Code != http.StatusNotFound {
		t.Fatalf("foreign Company read = %d, %s", response.Code, response.Body)
	}
}
