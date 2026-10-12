package tools

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestPresentOutputStudioFreezesCanvasAlongsideMedia(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		filename string
		nodeType string
	}{
		{filename: "final.png", nodeType: "image"},
		{filename: "final.mp3", nodeType: "audio"},
	} {
		t.Run(test.nodeType, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, test.filename), []byte("media"), 0o600); err != nil {
				t.Fatal(err)
			}
			api := &presentAPIStub{}
			service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
			if err != nil {
				t.Fatal(err)
			}
			input := studioPresentation()
			input.Items[0].Target.WorkspaceRelativePath = test.filename
			input.Studio.Nodes[1].Type = test.nodeType
			if _, err := service.PresentOutput(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			if api.output.Type != "media" || len(api.output.Items) != 2 || api.output.Items[0].Role != "primary" ||
				api.output.Items[1].Role != "supporting" || api.upload.Filename != studioCanvasFilename {
				t.Fatalf("output = %+v, upload = %+v, want primary media and supporting canvas", api.output, api.upload)
			}
			var canvas StudioCanvas
			if err := json.Unmarshal(api.uploadedContents[1], &canvas); err != nil {
				t.Fatal(err)
			}
			if canvas.Version != 1 || canvas.ProjectID != "project-1" || len(canvas.Nodes) != 2 ||
				canvas.Nodes[1].Type != test.nodeType || canvas.Nodes[1].Position.X != 520 ||
				canvas.Nodes[1].OutputOrdinal == nil ||
				*canvas.Nodes[1].OutputOrdinal != 0 || len(canvas.Edges) != 1 {
				t.Fatalf("canvas = %+v, want frozen positions, dependencies and media ordinal", canvas)
			}
			key := api.upload.RequestKey
			if _, err := service.PresentOutput(t.Context(), input); err != nil || api.upload.RequestKey != key {
				t.Fatalf("replay key = %q, %v, want %q", api.upload.RequestKey, err, key)
			}
			input.Studio.Nodes[1].Prompt = "Changed prompt"
			if _, err := service.PresentOutput(t.Context(), input); err != nil || api.upload.RequestKey == key {
				t.Fatalf("changed canvas key = %q, %v, want distinct identity", api.upload.RequestKey, err)
			}
		})
	}
}

func TestPresentOutputStudioRejectsInvalidGraphBeforeUpload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*PresentOutputInput)
	}{
		{"unknown version", func(in *PresentOutputInput) { in.Studio.Version = 2 }},
		{"NUL project", func(in *PresentOutputInput) { in.Studio.ProjectID = "project\x00id" }},
		{"NUL prompt", func(in *PresentOutputInput) { in.Studio.Nodes[0].Prompt = "text\x00text" }},
		{"duplicate node", func(in *PresentOutputInput) { in.Studio.Nodes[1].ID = in.Studio.Nodes[0].ID }},
		{"missing edge endpoint", func(in *PresentOutputInput) { in.Studio.Edges[0].Source = "absent" }},
		{"self edge", func(in *PresentOutputInput) { in.Studio.Edges[0].Target = in.Studio.Edges[0].Source }},
		{"infinite position", func(in *PresentOutputInput) { in.Studio.Nodes[0].Position.X = math.Inf(1) }},
		{"invalid ordinal", func(in *PresentOutputInput) { in.Studio.Nodes[1].OutputOrdinal = new(1) }},
		{"empty nodes", func(in *PresentOutputInput) { in.Studio.Nodes = nil }},
		{"too many nodes", func(in *PresentOutputInput) { in.Studio.Nodes = make([]StudioCanvasNode, 201) }},
		{"app", func(in *PresentOutputInput) { in.Type = "app"; in.Items = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := studioPresentation()
			tt.change(&input)
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "final.png"), []byte("media"), 0o600); err != nil {
				t.Fatal(err)
			}
			api := &presentAPIStub{}
			service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.PresentOutput(t.Context(), input); err == nil {
				t.Fatal("PresentOutput() error = nil, want invalid canvas")
			}
			if len(api.uploadedContents) != 0 || api.postPath != "" {
				t.Fatal("invalid canvas reached output API")
			}
		})
	}
}

func studioPresentation() PresentOutputInput {
	return PresentOutputInput{
		Title: "Campaign", Summary: "Final image",
		Items: []PresentOutputItem{{
			Role: "primary", Label: "Final image",
			Target: PresentOutputTarget{Kind: "workspace_file", WorkspaceRelativePath: "final.png"},
		}},
		Studio: &StudioCanvas{
			Version: 1, ProjectID: "project-1",
			Nodes: []StudioCanvasNode{
				{
					ID:       "reference",
					Type:     "image",
					Label:    "Reference",
					Position: StudioCanvasPosition{X: -120, Y: 40},
				},
				{
					ID:            "final",
					Type:          "image",
					Label:         "Final image",
					Position:      StudioCanvasPosition{X: 520, Y: 40},
					Model:         "nano-banana-2",
					Prompt:        "Campaign photograph",
					OutputOrdinal: new(0),
				},
			},
			Edges: []StudioCanvasEdge{{Source: "reference", Target: "final"}},
		},
	}
}

func TestPresentOutputReadsCLIExportAndRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	input := studioPresentation()
	encoded, err := json.Marshal(input.Studio)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "final.png"), []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &presentAPIStub{}
	service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	input.Studio = nil
	input.StudioCanvasPath = "source.json"
	if _, err := service.PresentOutput(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if len(api.output.Items) != 2 {
		t.Fatal("CLI source not attached")
	}
	for _, path := range []string{"../source.json", "missing.json"} {
		input.StudioCanvasPath = path
		if _, err := service.PresentOutput(t.Context(), input); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}
