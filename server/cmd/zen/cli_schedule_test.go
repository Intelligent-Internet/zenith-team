package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSchedulePreviewAndReviewedResume(t *testing.T) {
	scheduleID := "0198aa00-0000-7000-8000-00000000e201"
	collection := "/api/v1/companies/" + testCompanyID + "/schedules"
	digest := strings.Repeat("a", 64)
	definition := map[string]any{
		"id": scheduleID, "agentId": "0198aa00-0000-7000-8000-0000000000a1",
		"name": "Morning digest", "instructions": "Read the selected source.",
		"presetKey": nil, "revision": 3, "status": "paused",
		"trigger": map[string]any{"type": "cron", "expression": "0 9 * * *", "timeZone": "UTC"},
		"sources": []map[string]any{{"id": "0198aa00-0000-7000-8000-00000000e202"}},
	}
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == collection+"/"+scheduleID:
			json.NewEncoder(w).Encode(definition)
		case r.Method == http.MethodPost && r.URL.Path == collection+"/preview":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			want := map[string]any{
				"schemaVersion": float64(1), "agentId": definition["agentId"],
				"name": definition["name"], "instructions": definition["instructions"],
				"presetKey": nil, "trigger": definition["trigger"],
				"sourceIds": []any{"0198aa00-0000-7000-8000-00000000e202"},
			}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("preview body = %#v, want %#v", body, want)
			}
			json.NewEncoder(w).Encode(map[string]any{"reviewDigest": digest, "provider": "codex"})
		case r.Method == http.MethodPost && r.URL.Path == collection+"/"+scheduleID+"/resume":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			want := map[string]any{
				"schemaVersion":    float64(1),
				"expectedRevision": float64(3),
				"reviewDigest":     digest,
			}
			if !reflect.DeepEqual(body, want) {
				t.Errorf("resume body = %#v, want %#v", body, want)
			}
			json.NewEncoder(w).Encode(map[string]any{"status": "active"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	seedSession(t, srv.URL)

	out, err := executeCLI(t, newRootCmd, "schedule", "preview", scheduleID, "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{digest, "Morning digest", "Read the selected source.", "codex"} {
		if !strings.Contains(out, want) {
			t.Errorf("review output = %q, want %q", out, want)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("preview requests = %v, want only read and preview", requests)
	}
	if _, err := executeCLI(
		t,
		newRootCmd,
		"schedule",
		"resume",
		scheduleID,
		"--revision",
		"3",
		"--review-digest",
		digest,
	); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || requests[2] != "POST "+collection+"/"+scheduleID+"/resume" {
		t.Fatalf("requests = %v, want reviewed resume after preview", requests)
	}
}

func TestScheduleResumeRequiresExplicitReview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unreviewed resume sent %s %s", r.Method, r.URL.Path)
		http.Error(w, "unreviewed", http.StatusBadRequest)
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	_, err := executeCLI(t, newRootCmd, "schedule", "resume", "0198aa00-0000-7000-8000-00000000e201")
	if err == nil || !strings.Contains(err.Error(), "review-digest") {
		t.Fatalf("error = %v, want explicit review requirement", err)
	}
}
