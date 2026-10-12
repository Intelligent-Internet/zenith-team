package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCodexMCPImagesStayOutOfDurableTranscript(t *testing.T) {
	t.Parallel()
	image := map[string]any{"type": "image", "mimeType": "image/png", "data": "large-base64-preview"}
	result := map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": "region 0: stroke-a"}, image},
		"structuredContent": map[string]any{"sequence": 7},
	}
	got := codexMCPResult(result)
	if strings.Contains(got, "large-base64-preview") || !strings.Contains(got, "stroke-a") ||
		!strings.Contains(got, "dataOmitted") ||
		!strings.Contains(got, "sequence") {
		t.Fatalf("durable output = %s", got)
	}
	if image["data"] != "large-base64-preview" {
		t.Fatal("native image delivered to model was modified")
	}
}

func TestCodexImageGenerationCompletedItem(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			c, _, _ := newTestCodexClient(t)
			c.notificationProtocol = "raw"
			c.turnStarted = true
			var events []Event
			sequence := 0
			c.onMessage = func(msg Message) {
				if event, ok := codexExecutionEvent(msg, &sequence); ok {
					events = append(events, event)
				}
			}

			// Fields follow ImageGenerationItem from codex-cli 0.157.1.
			item := map[string]any{
				"type": "imageGeneration", "id": "image-1", "status": status,
				"revisedPrompt": "A green sports car", "transparentBackground": false,
				"result": "private-image-bytes", "savedPath": "/private/run/car.png",
				"failure": nil,
			}
			wantOutput := `{"status":"completed"}`
			if status == "failed" {
				item["failure"] = map[string]any{
					"type": "usageLimitExceeded", "limitId": "image_generation", "resetsAt": 12345,
					"privateMetadata": "must-not-leak",
				}
				wantOutput = `{"failure":{"limitId":"image_generation","resetsAt":12345,"type":"usageLimitExceeded"},"status":"failed"}`
			}
			for _, method := range []string{"item/started", "item/completed"} {
				frame, err := json.Marshal(map[string]any{
					"jsonrpc": "2.0", "method": method,
					"params": map[string]any{"threadId": "thr-1", "turnId": "turn-1", "item": item},
				})
				if err != nil {
					t.Fatal(err)
				}
				c.handleLine(string(frame))
			}
			if got := c.getTurnError(); got != "" {
				t.Fatalf("turn error = %q, want none for a supported tool item", got)
			}
			wantParts := []Part{
				ToolCallPart{CallID: "image-1", Name: "image_generation", Input: map[string]any{
					"revisedPrompt": "A green sports car", "transparentBackground": false,
				}},
				ToolResultPart{
					CallID:   "image-1",
					Name:     "image_generation",
					Status:   ToolResultStatus(status),
					Output:   wantOutput,
					FilePath: "/private/run/car.png",
				},
			}
			want := []Event{
				CompletedItem{Key: "codex-tool-call:image-1", Role: RoleAgent, Parts: wantParts[:1]},
				CompletedItem{Key: "image-1", Role: RoleTool, Parts: wantParts},
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v, want %#v", events, want)
			}
		})
	}
}
