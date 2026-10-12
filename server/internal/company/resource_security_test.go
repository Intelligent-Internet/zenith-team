package company

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLocalDirectoryResourcePathTypesRedactFmtGoStringAndSlog(t *testing.T) {
	t.Parallel()
	const path = "/Users/owner/private/boardly-secret"
	resourceID, companyID, ownerID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	now := time.Date(2026, time.July, 12, 1, 2, 3, 0, time.UTC)
	input := CreateLocalDirectoryResourceInput{Path: path, Label: "Workspace", Position: 1}
	record := resourceRecord{Response: ResourceResponse{
		ID: resourceID, CompanyID: companyID,
		CreatedBy: ActorRef{ID: ownerID, Kind: ActorTypeUser, DisplayName: "Owner"},
		Label:     "Workspace", CreatedAt: now, UpdatedAt: now, Revision: 1,
	}, LocalPath: path}
	for _, value := range []any{input, record} {
		goStringer, ok := value.(fmt.GoStringer)
		if !ok {
			t.Errorf("value %T does not implement fmt.GoStringer", value)
			continue
		}
		assertResourcePathRedacted(t, value, goStringer.GoString(), path, resourceID, companyID, ownerID)
		for _, format := range []string{"%v", "%+v", "%#v", "%q"} {
			assertResourcePathRedacted(
				t,
				value,
				fmt.Sprintf(format, value),
				path,
				resourceID,
				companyID,
				ownerID,
			)
		}
		var output bytes.Buffer
		slog.New(slog.NewJSONHandler(&output, nil)).Info("Company resource", slog.Any("value", value))
		assertResourcePathRedacted(t, value, output.String(), path, resourceID, companyID, ownerID)
	}
}

func assertResourcePathRedacted(t *testing.T, value any, rendered, path string, ids ...uuid.UUID) {
	t.Helper()
	if !strings.Contains(rendered, "[REDACTED]") || strings.Contains(rendered, path) {
		t.Fatalf("rendered %T leaks path: %s", value, rendered)
	}
	for _, id := range ids {
		if strings.Contains(rendered, id.String()) {
			t.Fatalf("rendered %T leaks UUID %s: %s", value, id, rendered)
		}
	}
}
