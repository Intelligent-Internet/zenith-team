package canvas

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestCommandRejectsArtifactApproval(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"accept", "reject", "request_changes", "keep_variant"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			command := Command{RequestKey: uuid.New(), Type: "review", Action: action}
			if err := normalizeCommand(&command); !errors.Is(err, ErrInvalid) {
				t.Fatalf("normalizeCommand() = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestCommandRejectsUnrelatedAttachment(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"frame", "move", "remove", "review", "create_document", "checkpoint"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			id := uuid.New()
			command := Command{RequestKey: uuid.New(), Type: operation, AttachmentID: &id}
			if err := normalizeCommand(&command); !errors.Is(err, ErrInvalid) {
				t.Fatalf("normalize = %v, want ErrInvalid", err)
			}
		})
	}
	for _, operation := range []string{"add_artifact", "add_version"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			id := uuid.New()
			command := Command{RequestKey: uuid.New(), Type: operation, AttachmentID: &id}
			if err := normalizeCommand(&command); err != nil {
				t.Fatal(err)
			}
			command.OutputID = &id
			if err := normalizeCommand(&command); !errors.Is(err, ErrInvalid) {
				t.Fatalf("mixed sources = %v, want ErrInvalid", err)
			}
		})
	}
}
