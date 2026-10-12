package tools

import (
	"context"
	"strings"
	"testing"
)

type appPreviewRegistrarStub struct {
	outputID string
	commands []PresentOutputCommand
	err      error
}

func (s *appPreviewRegistrarStub) RegisterAppPreview(
	_ context.Context,
	outputID string,
	commands []PresentOutputCommand,
) error {
	s.outputID = outputID
	s.commands = append([]PresentOutputCommand(nil), commands...)
	return s.err
}

func TestPresentAppRegistersCommandsAndPostsPrimaryLocalURL(t *testing.T) {
	t.Parallel()
	api := &presentAPIStub{}
	registrar := &appPreviewRegistrarStub{}
	service, err := NewPresentOutputService(
		api,
		testPresentRunContext(),
		t.TempDir(),
		nil,
		registrar,
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.PresentOutput(t.Context(), PresentOutputInput{
		Type: "app", Title: "Site", Summary: "Ready to preview.",
		App: &PresentOutputApp{Commands: []PresentOutputCommand{
			{Command: "go", Args: []string{"run", "./cmd/api"}, WorkingDirectory: "api", Port: 8080},
			{Command: "npm", Args: []string{"run", "dev"}, WorkingDirectory: "web", Port: 5173},
		}},
	})
	if err != nil {
		t.Fatalf("PresentOutput(app) error = %v", err)
	}
	if result.OutputID != presentOutputTestID || api.uploadCalls != 0 ||
		api.output.Type != "app" || api.output.App == nil || api.output.App.Commands[0].Port != 8080 ||
		len(api.output.Items) != 0 || registrar.outputID != presentOutputTestID ||
		len(registrar.commands) != 2 ||
		registrar.commands[1].Port != 5173 {
		t.Fatalf("result = %+v, uploads = %d, output = %+v", result, api.uploadCalls, api.output)
	}
}

func TestPresentAppRejectsInvalidCommandsBeforeRegistration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		commands []PresentOutputCommand
	}{
		{name: "missing", commands: nil},
		{name: "missing executable", commands: []PresentOutputCommand{{Port: 5173}}},
		{name: "shell command", commands: []PresentOutputCommand{{Command: "npm run dev", Port: 5173}}},
		{
			name:     "invalid working directory",
			commands: []PresentOutputCommand{{Command: "npm", WorkingDirectory: "../app", Port: 5173}},
		},
		{name: "privileged port", commands: []PresentOutputCommand{{Command: "npm", Port: 80}}},
		{
			name:     "duplicate port",
			commands: []PresentOutputCommand{{Command: "api", Port: 8080}, {Command: "web", Port: 8080}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			api := &presentAPIStub{}
			registrar := &appPreviewRegistrarStub{}
			service, err := NewPresentOutputService(
				api,
				testPresentRunContext(),
				t.TempDir(),
				nil,
				registrar,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.PresentOutput(t.Context(), PresentOutputInput{
				Type:    "app",
				App:     &PresentOutputApp{Commands: test.commands},
				Title:   "App",
				Summary: "Ready.",
			})
			if err == nil || !strings.Contains(err.Error(), "command") {
				t.Fatalf("PresentOutput(%s) error = %v, want command rejection", test.name, err)
			}
			if api.uploadCalls != 0 || api.postPath != "" || len(registrar.commands) != 0 {
				t.Fatal("invalid app command reached registration or the API")
			}
		})
	}
}

func TestPresentAppRejectsFiles(t *testing.T) {
	t.Parallel()
	api := &presentAPIStub{}
	service, err := NewPresentOutputService(
		api,
		testPresentRunContext(),
		t.TempDir(),
		nil,
		&appPreviewRegistrarStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PresentOutput(t.Context(), PresentOutputInput{
		Type: "app", App: &PresentOutputApp{Commands: []PresentOutputCommand{{Command: "npm", Port: 4173}}},
		Title: "App", Summary: "Ready.",
		Items: []PresentOutputItem{{
			Role: "primary", Label: "App",
			Target: PresentOutputTarget{Kind: "workspace_file", WorkspaceRelativePath: "index.html"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "not items") {
		t.Fatalf("PresentOutput(app with items) error = %v", err)
	}
	if api.uploadCalls != 0 || api.postPath != "" {
		t.Fatal("invalid app presentation reached the API")
	}
}

func TestPresentAppReportsRegistrationFailureAfterOutputWasSaved(t *testing.T) {
	t.Parallel()
	api := &presentAPIStub{}
	registrar := &appPreviewRegistrarStub{err: context.Canceled}
	service, err := NewPresentOutputService(api, testPresentRunContext(), t.TempDir(), nil, registrar)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PresentOutput(t.Context(), PresentOutputInput{
		Type: "app", App: &PresentOutputApp{Commands: []PresentOutputCommand{{Command: "npm", Port: 5173}}},
		Title: "App", Summary: "Ready.",
	})
	if err == nil || !strings.Contains(err.Error(), "register local app preview") {
		t.Fatalf("PresentOutput() error = %v", err)
	}
	if api.postPath == "" || registrar.outputID != presentOutputTestID {
		t.Fatal("failed local registration did not retain the saved Output identity for retry")
	}
}
