package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestDaemonConfigReturnsOnlyEndpointAndRequiresBearer(t *testing.T) {
	t.Parallel()
	h := fullyWiredHandler()
	h.AuthService = authenticatedAuthStub(uuid.New())
	h.StudioAPIOrigin = "http://localhost:8000"
	router := New(h)
	for _, authorized := range []bool{false, true} {
		request := httptest.NewRequest("GET", "/api/v1/daemon/config", nil)
		if authorized {
			request.Header.Set("Authorization", "Bearer session")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if !authorized {
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
			continue
		}
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got["studioApiOrigin"] != h.StudioAPIOrigin {
			t.Fatalf("config = %v, want endpoint only", got)
		}
	}
}
