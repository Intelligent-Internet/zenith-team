//go:build integration

package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/config"
	"boardly.dev/boardly/server/internal/db"
	adapter "boardly.dev/boardly/server/internal/infra/social"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

type socialTransport func(*http.Request) (*http.Response, error)

func (f socialTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type socialAttachmentDownload struct {
	attachment.AttachmentService
	download func(context.Context, attachment.ResolvedDownloadInput) (attachment.DownloadResult, error)
}

func (s socialAttachmentDownload) DownloadResolved(
	ctx context.Context,
	input attachment.ResolvedDownloadInput,
) (attachment.DownloadResult, error) {
	return s.download(ctx, input)
}

func TestPublishRunAuthorityAndDuplicateDelivery(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSocialService(
		store,
		config.SocialConfig{
			Platforms: map[string]config.SocialPlatformConfig{"x": {Enabled: true, ClientID: "id"}},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	s.vault, _ = newVault(bytes.Repeat([]byte{3}, 32))
	var sessionID, runID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO auth_sessions(user_id,token_prefix,token_hash,expires_at) VALUES($1,'social',decode('1234','hex'),now()+interval '1 hour') RETURNING id`, f.UserA).
		Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,revision) VALUES($1,$2,$3,$4,'running','direct_chat',1) RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA).
		Scan(&runID); err != nil {
		t.Fatal(err)
	}
	token := adapter.Token{
		AccessToken:  "access",
		RefreshToken: "refresh",
		Scopes:       "tweet.write",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := s.saveAccount(
		ctx,
		f.UserA,
		sessionID,
		"x",
		adapter.Account{
			ExternalID:  "account",
			DisplayName: "Mine",
			Token:       token,
			Targets: []adapter.Target{
				{ExternalID: "account", DisplayName: "Mine", Type: "account", Token: token},
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	p := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA}
	connections, err := s.List(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	input := PostInput{
		ConnectionID: connections[0].ID,
		TargetID:     connections[0].Targets[0].ID,
		Text:         "Hello from my agent",
	}
	run := RunContext{
		RunID:          runID,
		ConversationID: f.DirectConversationA,
		AgentID:        f.AgentA,
	}
	var messageID, videoID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO conversation_items(company_id,conversation_id,run_id,seq,type,author_kind,author_id,content) VALUES($1,$2,$3,1,'user_message','user',$4,'Post my video') RETURNING id`, f.CompanyA, f.DirectConversationA, runID, f.UserA).
		Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO attachments(company_id,uploaded_by_user_id,request_key,filename,content_type,size_bytes,status) VALUES($1,$2,gen_random_uuid(),'clip.mp4','video/mp4',11,'ready') RETURNING id`, f.CompanyA, f.UserA).
		Scan(&videoID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO message_attachments(company_id,message_id,attachment_id,position) VALUES($1,$2,$3,0)`,
		f.CompanyA,
		messageID,
		videoID,
	); err != nil {
		t.Fatal(err)
	}
	resources, err := s.ReadForRun(ctx, p, run)
	if err != nil || len(resources.Images) != 0 || len(resources.Videos) != 1 ||
		resources.Videos[0].ID != videoID.String() {
		t.Fatalf("conversation video resources = %+v, err = %v", resources, err)
	}
	input.AttachmentIDs = []string{videoID.String()}
	s.attachments = socialAttachmentDownload{
		download: func(_ context.Context, input attachment.ResolvedDownloadInput) (attachment.DownloadResult, error) {
			if input.ID != videoID || input.CompanyID != f.CompanyA {
				t.Error("download escaped the authorized conversation media")
			}
			return attachment.DownloadResult{Body: io.NopCloser(strings.NewReader("video-bytes"))}, nil
		},
	}
	var publishes atomic.Int32
	var uncertain atomic.Bool
	var unauthorized atomic.Bool
	var cancelDuringUpload atomic.Bool
	s.providerHTTP = &http.Client{Transport: socialTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"id":"account"}}`
		if r.URL.Path == "/2/media/upload/initialize" {
			body = `{"data":{"id":"video"}}`
		}
		if r.URL.Path == "/2/media/upload/video/finalize" && cancelDuringUpload.Load() {
			if _, err := pool.Exec(
				ctx,
				`UPDATE runs SET status='cancelled' WHERE id=$1`,
				runID,
			); err != nil {
				t.Error(err)
			}
		}
		if r.URL.Path == "/2/tweets" {
			publishes.Add(1)
			if uncertain.Load() {
				return nil, errors.New("connection lost after send")
			}
			if unauthorized.Load() {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Body:       io.NopCloser(strings.NewReader(`{}`)),
					Header:     make(http.Header),
				}, nil
			}
			body = `{"data":{"id":"posted"}}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})}
	preview, err := s.PreviewForRun(ctx, p, run, input)
	if err != nil || preview.Text != input.Text || preview.TargetName != "Mine" ||
		len(preview.Media) != 1 || preview.Media[0].ID != videoID.String() || publishes.Load() != 0 {
		t.Fatalf("preview = %+v, err = %v, sends = %d", preview, err, publishes.Load())
	}
	newReview := func(t *testing.T, preview *approval.QuestionSocialPost, status, choice string) uuid.UUID {
		t.Helper()
		prompts, err := json.Marshal([]approval.QuestionPrompt{
			{
				ID:            "post",
				Prompt:        "Publish this post?",
				Required:      true,
				SelectionMode: approval.QuestionSelectionSingle,
				SocialPost:    preview,
				Options: []approval.QuestionOption{
					{ID: "publish", Label: "Publish now"},
					{ID: "cancel", Label: "Do not publish"},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		answers, err := json.Marshal([]approval.QuestionAnswer{{PromptID: "post", OptionIDs: []string{choice}}})
		if err != nil {
			t.Fatal(err)
		}
		var id uuid.UUID
		err = pool.QueryRow(ctx, `INSERT INTO questions
			(company_id, title, asked_by_agent_id, origin_run_id, request_key, conversation_id, origin_user_id,
			prompts, request_sha256, expires_at, status, answers, resolved_by_user_id, continuation_status)
			VALUES ($1,'Review post',$2,$3,gen_random_uuid(),$4,$5,$6,decode('1234','hex'),now()+interval '1 day',
			$7,$8,CASE WHEN $7='answered' THEN $5::uuid ELSE NULL END,
			CASE WHEN $7='pending' THEN NULL ELSE 'pending' END) RETURNING id`,
			f.CompanyA, f.AgentA, runID, f.DirectConversationA, f.UserA, prompts, status, answers).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, tc := range []struct {
		name, status, choice string
		preview              *approval.QuestionSocialPost
	}{
		{"pending", "pending", "publish", &preview},
		{"declined", "answered", "cancel", &preview},
		{"withdrawn", "withdrawn", "publish", &preview},
		{"ordinary question", "answered", "publish", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := newReview(t, tc.preview, tc.status, tc.choice)
			if _, err := s.PublishForRun(ctx, p, run, id); !errors.Is(err, ErrApprovalRequired) {
				t.Fatalf("publish = %v, want approval required", err)
			}
		})
	}
	for _, tc := range []struct{ name, query string }{
		{"expired", `UPDATE questions SET expires_at=now()-interval '1 second' WHERE id=$1`},
		{"wrong resolver", `UPDATE questions SET resolved_by_user_id=gen_random_uuid() WHERE id=$1`},
		{"wrong owner", `UPDATE questions SET origin_user_id=gen_random_uuid() WHERE id=$1`},
		{"wrong agent", `UPDATE questions SET asked_by_agent_id=gen_random_uuid() WHERE id=$1`},
		{"custom revision", `UPDATE questions SET answers='[{"promptId":"post","optionIds":[],"text":"Change the caption"}]' WHERE id=$1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := newReview(t, &preview, "answered", "publish")
			if _, err := pool.Exec(ctx, tc.query, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PublishForRun(ctx, p, run, id); !errors.Is(err, ErrApprovalRequired) {
				t.Fatalf("publish = %v, want approval required", err)
			}
		})
	}
	if publishes.Load() != 0 {
		t.Fatal("unapproved post reached provider")
	}
	staleID := newReview(t, &preview, "answered", "publish")
	if _, err := pool.Exec(
		ctx,
		`UPDATE social_connections SET version=version+1 WHERE id=$1`,
		input.ConnectionID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishForRun(ctx, p, run, staleID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("account reconnected since preview = %v, want approval required", err)
	}
	preview, err = s.PreviewForRun(ctx, p, run, input)
	if err != nil {
		t.Fatal(err)
	}
	questionID := newReview(t, &preview, "answered", "publish")
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, err := s.PublishForRun(ctx, p, run, questionID)
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := s.PublishForRun(ctx, p, run, questionID)
	if err != nil || got.Status != "published" || publishes.Load() != 1 {
		t.Fatalf("receipt = %+v, err = %v, sends = %d", got, err, publishes.Load())
	}
	// Resuming in a different Run still consumes the same review only once.
	if _, err := pool.Exec(ctx, `UPDATE runs SET status='completed' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,revision) VALUES($1,$2,$3,$4,'running','direct_chat',1) RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA).
		Scan(&runID); err != nil {
		t.Fatal(err)
	}
	run.RunID = runID
	got, err = s.PublishForRun(ctx, p, run, questionID)
	if err != nil || got.Text != input.Text || publishes.Load() != 1 {
		t.Fatalf("approved replay = %+v, err = %v, sends = %d", got, err, publishes.Load())
	}
	if _, err := s.ReadForRun(
		ctx,
		auth.UserPrincipal{UserID: f.UserB, CompanyID: f.CompanyA},
		run,
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("other owner = %v", err)
	}
	uncertain.Store(true)
	questionID = newReview(t, &preview, "answered", "publish")
	for range 2 {
		result, err := s.PublishForRun(ctx, p, run, questionID)
		if err != nil || result.Status != "unknown" {
			t.Fatalf("ambiguous result: %+v %v", result, err)
		}
	}
	if publishes.Load() != 2 {
		t.Fatalf("uncertain publication was sent again: %d", publishes.Load())
	}
	uncertain.Store(false)
	cancelDuringUpload.Store(true)
	questionID = newReview(t, &preview, "answered", "publish")
	cancelled, err := s.PublishForRun(ctx, p, run, questionID)
	if err != nil || cancelled.Status != "failed" || cancelled.ErrorCode != "authorization_changed" ||
		publishes.Load() != 2 {
		t.Fatalf("run cancelled during upload: receipt=%+v err=%v sends=%d", cancelled, err, publishes.Load())
	}
	cancelDuringUpload.Store(false)
	if _, err := pool.Exec(ctx, `UPDATE runs SET status='running' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	unauthorized.Store(true)
	questionID = newReview(t, &preview, "answered", "publish")
	result, err := s.PublishForRun(ctx, p, run, questionID)
	if err != nil || result.ErrorCode != "needs_reconnect" {
		t.Fatalf("revoked publish grant: %+v %v", result, err)
	}
	connections, err = s.List(ctx, p)
	if err != nil || connections[0].CanPublish || connections[0].Status != "needs_reconnect" {
		t.Fatalf("revoked grant visibility: %+v %v", connections, err)
	}
	if err := s.Disconnect(ctx, p, connections[0].ID, connections[0].Revision); err != nil {
		t.Fatal(err)
	}
	history, err := s.ReadForRun(ctx, p, run)
	if err != nil || len(history.Connections) != 0 || len(history.Posts) != 4 {
		t.Fatalf("history after disconnect = %+v, err = %v", history, err)
	}
	questionID = newReview(t, &preview, "answered", "publish")
	if _, err := s.PublishForRun(ctx, p, run, questionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("publish after disconnect = %v, want not found", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE runs SET status='completed' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishForRun(ctx, p, run, questionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("completed run = %v", err)
	}
}
