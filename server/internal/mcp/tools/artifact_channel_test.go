package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestArtifactChannelBindsRunAndCapability(t *testing.T) {
	t.Parallel()
	runID, artifactID := uuid.NewString(), uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "ZenithRun secret" {
			t.Error("missing Run capability")
		}
		var request struct {
			Path string               `json:"path"`
			Body ArtifactInspectInput `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Path != "/runs/"+runID+"/artifact-inspect" || request.Body.ArtifactID != artifactID {
			t.Errorf("unexpected request: %+v", request)
		}
		_ = json.NewEncoder(w).
			Encode(map[string]any{"schemaVersion": 1, "status": 200, "body": map[string]any{"id": artifactID, "items": []any{}}})
	}))
	defer server.Close()
	service, err := NewArtifactChannelService(server.URL, "secret", runID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.InspectArtifact(t.Context(), ArtifactInspectInput{ArtifactID: artifactID})
	if err != nil || result.ID != artifactID {
		t.Fatalf("inspect = %+v, %v", result, err)
	}
}
