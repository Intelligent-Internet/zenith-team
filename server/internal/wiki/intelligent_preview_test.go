package wiki

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPreviewPatchReplayAndRecovery(t *testing.T) {
	t.Parallel()
	first := SourcePreviewPatch{
		RequestKey:      uuid.New(),
		ProtocolVersion: 1,
		LanguageVersion: 1,
		Text:            "# Ready\n\n<text title=",
	}
	state, err := ApplySourcePreviewPatch(SourcePreviewState{}, first)
	if err != nil || state.Revision != 1 || state.Preview.Complete || state.Preview.Content == nil {
		t.Fatalf("first preview = %+v, %v", state, err)
	}
	replay, err := ApplySourcePreviewPatch(state, first)
	if err != nil || replay.Revision != state.Revision || replay.Source != state.Source {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	changed := first
	changed.Text = "Different"
	if _, err := ApplySourcePreviewPatch(state, changed); err == nil {
		t.Fatal("reused key accepted different bytes")
	}
	// Reconnect restores the retained snapshot and resumes at its byte boundary.
	encoded, _ := json.Marshal(state)
	var restored SourcePreviewState
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	appendPatch := SourcePreviewPatch{
		RequestKey:       uuid.New(),
		ProtocolVersion:  1,
		LanguageVersion:  1,
		ExpectedRevision: 1,
		Start:            len(state.Source),
		Text:             `"Total">{2 + 3}</text>`,
	}
	completed, err := ApplySourcePreviewPatch(restored, appendPatch)
	if err != nil || !completed.Preview.Complete || completed.Revision != 2 {
		t.Fatalf("completed = %+v, %v", completed, err)
	}
	if state.Source != first.Text {
		t.Fatal("patch mutated prior state")
	}
	discard := SourcePreviewPatch{
		RequestKey:       uuid.New(),
		ProtocolVersion:  1,
		LanguageVersion:  1,
		ExpectedRevision: 2,
		Discard:          true,
	}
	cleared, err := ApplySourcePreviewPatch(completed, discard)
	if err != nil || !cleared.Discarded || cleared.Source != "" || cleared.Preview.Content != nil {
		t.Fatalf("discard = %+v, %v", cleared, err)
	}
	if _, err := ApplySourcePreviewPatch(cleared, discard); err != nil {
		t.Fatalf("discard replay: %v", err)
	}
	appendPatch.RequestKey = uuid.New()
	appendPatch.ExpectedRevision = 3
	if _, err := ApplySourcePreviewPatch(cleared, appendPatch); err == nil {
		t.Fatal("discarded stream accepted more content")
	}
}

func TestPreviewPatchBounds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		current SourcePreviewState
		patch   SourcePreviewPatch
		code    string
	}{
		{"UTF-8 interior", SourcePreviewState{Source: "é"}, SourcePreviewPatch{Start: 1}, "preview.offset"},
		{"UTF-8 delete", SourcePreviewState{Source: "é"}, SourcePreviewPatch{DeleteBytes: 1}, "preview.offset"},
		{
			"overflow offset",
			SourcePreviewState{},
			SourcePreviewPatch{Start: int(^uint(0) >> 1)},
			"preview.offset",
		},
		{
			"patch bytes",
			SourcePreviewState{},
			SourcePreviewPatch{Text: strings.Repeat("a", 65537)},
			"preview.patch",
		},
		{
			"document bytes",
			SourcePreviewState{Source: strings.Repeat("a", 262144)},
			SourcePreviewPatch{Text: "b"},
			"preview.size",
		},
		{"stale", SourcePreviewState{Revision: 1}, SourcePreviewPatch{}, "preview.conflict"},
		{
			"revision bound",
			SourcePreviewState{Revision: 4096},
			SourcePreviewPatch{ExpectedRevision: 4096},
			"preview.limit",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.patch.RequestKey = uuid.New()
			tt.patch.ProtocolVersion = 1
			tt.patch.LanguageVersion = 1
			_, err := ApplySourcePreviewPatch(tt.current, tt.patch)
			var diagnostic *UIEvaluationError
			if !errors.As(err, &diagnostic) || diagnostic.Code != tt.code {
				t.Fatalf("error = %v, want %s", err, tt.code)
			}
		})
	}
}
