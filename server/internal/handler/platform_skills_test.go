package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/tools"
)

// The app lists the platform skills, compiled and pinned — the same catalog
// the MCP server searches — so an Agent's capability page names what every
// Run can load. Any member may read it; it carries no Company data.
func TestPlatformSkillsListTheCatalogForMembers(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	member := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	companies := &companyServiceStub{
		authorize: func(_ context.Context, _ auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
			if gotCompanyID != companyID {
				t.Fatalf("Authorize() Company = %s, want %s", gotCompanyID, companyID)
			}
			return member, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: companies})
	path := "/api/v1/companies/" + companyID.String() + "/platform-skills"

	response := serveBearerJSON(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	var got struct {
		Items []tools.PlatformSkillSummary `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 3 || got.Items[0].Key != tools.ZenithPlatformSkillKey ||
		got.Items[1].Key != "zenith-intelligent-ui" ||
		got.Items[2].Key != tools.ZenithWorkflowsSkillKey ||
		len(got.Items[0].References) == 0 || got.Items[0].Description == "" {
		t.Fatalf("items = %+v", got.Items)
	}

	// The read takes no query parameters.
	response = serveBearerJSON(handler, http.MethodGet, path+"?limit=1", nil)
	assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
}
