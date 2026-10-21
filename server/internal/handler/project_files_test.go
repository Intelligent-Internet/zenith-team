package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

// Files left the PATCH body (ADR 0017): they need project.files.manage, which
// no Company role grants, so they have their own route. The strict decoder
// refuses a PATCH that still names them.
func TestProjectFilesMoveFromPatchToTheirOwnRoute(t *testing.T) {
	t.Parallel()
	decode := func(body string) (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		util.SetRequestID(c, "project-files-request")
		c.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		return c, recorder
	}

	c, recorder := decode(`{"expectedRevision":1,"files":{"logoId":null,"documentIds":[]}}`)
	_, err := bindRequest[updateProjectRequest](c, 128<<10)
	if err == nil {
		t.Fatal("PATCH with files was accepted")
	}
	writeRequestError(c, err)
	assertErrorResponse(t, recorder, http.StatusBadRequest, "invalid_request")

	c, _ = decode(`{"logoId":null,"documentIds":[],"expectedRevision":3}`)
	request, err := bindRequest[replaceProjectFilesRequest](c, 16<<10)
	if err != nil || request.DocumentIDs == nil || request.LogoID != nil || request.ExpectedRevision != 3 {
		t.Fatalf("PUT files = %+v, %v", request, err)
	}
	for _, body := range []string{`{"logoId":null,"expectedRevision":3}`, `{"logoId":null,"documentIds":[]}`} {
		c, _ = decode(body)
		if _, err := bindRequest[replaceProjectFilesRequest](c, 16<<10); err == nil {
			t.Fatalf("PUT files %s was accepted", body)
		}
	}
}
