package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type socialAPIStub struct{ paths, keys []string }

func (s *socialAPIStub) FetchRunJSON(_ context.Context, path string, destination any) error {
	s.paths = append(s.paths, path)
	return json.Unmarshal(
		[]byte(
			`{"connections":[],"posts":[{"id":"receipt","status":"published"}],"images":[],"videos":[{"id":"video","filename":"clip.mp4","contentType":"video/mp4","sizeBytes":12}]}`,
		),
		destination,
	)
}

func (s *socialAPIStub) PostRunJSON(_ context.Context, path, key string, _, destination any) error {
	s.paths = append(s.paths, path)
	s.keys = append(s.keys, key)
	return json.Unmarshal(
		[]byte(`{"id":"receipt","status":"published","url":"https://x.com/i/status/posted"}`),
		destination,
	)
}

func TestSocialToolsBindRunAndKeepPublishRequestKey(t *testing.T) {
	t.Parallel()
	api := new(socialAPIStub)
	service, err := NewSocialToolService(
		api,
		RunContext{CompanyID: "company", ConversationID: "conversation", RunID: "run", AgentID: "agent"},
	)
	if err != nil {
		t.Fatal(err)
	}
	input := PublishSocialInput{QuestionID: "review"}
	for range 2 {
		got, err := service.PublishSocial(t.Context(), input)
		if err != nil || got.Status != "published" || got.ID != "receipt" {
			t.Fatalf("publish receipt=%+v, %v", got, err)
		}
	}
	if api.keys[0] != api.keys[1] || api.paths[0] != "/companies/company/conversations/conversation/social/posts" {
		t.Fatalf("paths=%v, keys=%v", api.paths, api.keys)
	}
	input.QuestionID = "another-review"
	if _, err := service.PublishSocial(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if api.keys[2] == api.keys[0] {
		t.Fatal("different review reused a receipt key")
	}
	result, err := service.ReadSocial(t.Context(), ReadSocialInput{})
	if err != nil || len(result.Posts) != 1 || len(result.Videos) != 1 || result.Videos[0].ID != "video" ||
		api.paths[3] != "/companies/company/conversations/conversation/social" {
		t.Fatalf("social read=%+v, %v", result, err)
	}
}
