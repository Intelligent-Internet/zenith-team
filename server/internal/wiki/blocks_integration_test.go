//go:build integration

package wiki

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

// This contract needs the installed client dependencies but no database.
// Keep it in the integration lane so Go-only unit environments stay standalone.
func TestReactiveBlocksReadByYjs(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/intelligent-yjs.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Original, Expected     BlockNode
		Base, UpdateA, UpdateB []byte
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeBlocks(fixture.Original)
	if err != nil {
		t.Fatal(err)
	}
	merged, _, _, err := MergeBlocks(fixture.Base, fixture.UpdateA)
	if err != nil {
		t.Fatal(err)
	}
	merged, _, _, err = MergeBlocks(merged, fixture.UpdateB)
	if err != nil {
		t.Fatal(err)
	}
	_, vector, err := BlockStateDelta(fixture.Base, nil)
	if err != nil {
		t.Fatal(err)
	}
	delta, _, err := BlockStateDelta(merged, vector)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		updates  [][]byte
		expected BlockNode
	}{
		{"Go encoded state", [][]byte{encoded}, fixture.Original},
		{"Go merged state", [][]byte{merged}, fixture.Expected},
		{"Go delta and retry", [][]byte{fixture.Base, delta, delta}, fixture.Expected},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input, err := json.Marshal(struct {
				Updates  [][]byte  `json:"updates"`
				Expected BlockNode `json:"expected"`
			}{tt.updates, tt.expected})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "node", "testdata/verify-intelligent-yjs.mjs")
			command.Stdin = bytes.NewReader(input)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf(
					"client adapter rejected Go state (install frontend dependencies first): %v\n%s",
					err,
					output,
				)
			}
		})
	}
}

func TestReactiveBlocksRegisterInClientEditorSchema(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-intelligent-editor-schema.mjs")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf(
			"client editor schema rejected reactive nodes (install frontend dependencies first): %v\n%s",
			err,
			output,
		)
	}
}

func TestInlineRuntimeContract(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/verify-inline-runtime.mjs")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inline interpreter contract failed: %v\n%s", err, output)
	}
}
