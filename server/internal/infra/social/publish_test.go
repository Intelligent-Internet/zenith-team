package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestXVideoUploadCompletesBeforePublishing(t *testing.T) {
	t.Parallel()
	for _, processing := range []string{"succeeded", "failed"} {
		t.Run(processing, func(t *testing.T) {
			t.Parallel()
			video := bytes.Repeat([]byte("v"), (4<<20)+17)
			var uploaded bytes.Buffer
			segments, statuses, posts := 0, 0, 0
			client := NewClient(
				"x",
				Credentials{},
				&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Authorization") != "Bearer access" {
						t.Error("missing video authorization")
					}
					body := `{}`
					switch r.URL.Path {
					case "/2/media/upload/initialize":
						var payload map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Fatal(err)
						}
						if textField(payload, "media_category") != "tweet_video" ||
							textField(payload, "media_type") != "video/mp4" ||
							string(payload["total_bytes"]) != strconv.Itoa(len(video)) {
							t.Error("wrong video initialization")
						}
						body = `{"data":{"id":"video-id"}}`
					case "/2/media/upload/video-id/append":
						if err := r.ParseMultipartForm(5 << 20); err != nil {
							t.Fatal(err)
						}
						if r.FormValue("segment_index") != strconv.Itoa(segments) {
							t.Error("video segments out of order")
						}
						file, _, err := r.FormFile("media")
						if err != nil {
							t.Fatal(err)
						}
						defer file.Close()
						if _, err := io.Copy(&uploaded, file); err != nil {
							t.Fatal(err)
						}
						segments++
					case "/2/media/upload/video-id/finalize":
						if !bytes.Equal(uploaded.Bytes(), video) || segments != 2 {
							t.Fatal("finalized before uploading the complete video")
						}
						body = `{"data":{"id":"video-id","processing_info":{"state":"pending","check_after_secs":0}}}`
					case "/2/media/upload":
						if r.Method != http.MethodGet ||
							r.URL.Query().Get("command") != "STATUS" ||
							r.URL.Query().Get("media_id") != "video-id" {
							t.Error("wrong processing status request")
						}
						statuses++
						body = `{"data":{"processing_info":{"state":"` + processing + `"}}}`
					case "/2/tweets":
						if processing != "succeeded" || statuses != 1 {
							t.Error("published before video processing succeeded")
						}
						var payload struct {
							Media struct {
								IDs []string `json:"media_ids"`
							} `json:"media"`
						}
						if err := json.NewDecoder(r.Body).
							Decode(&payload); err != nil || len(payload.Media.IDs) != 1 ||
							payload.Media.IDs[0] != "video-id" {
							t.Error("missing uploaded video on the post")
						}
						posts++
						body = `{"data":{"id":"posted"}}`
					default:
						t.Fatalf("unexpected video request: %s", r.URL.Path)
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				})},
			)
			got, err := client.Publish(
				t.Context(),
				Token{AccessToken: "access"},
				PublishInput{
					Text: "Video",
					Video: &Video{
						Content:     bytes.NewReader(video),
						ContentType: "video/mp4",
						SizeBytes:   int64(len(video)),
					},
				},
			)
			if processing == "failed" {
				if err == nil || posts != 0 {
					t.Fatalf("rejected processing: err=%v posts=%d", err, posts)
				}
			} else if err != nil || posts != 1 || got.ExternalID != "posted" {
				t.Fatalf("video publish=%+v err=%v posts=%d", got, err, posts)
			}
		})
	}
}

func TestVideoValidationRejectsMixedOrUnsupportedMedia(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		platform string
		input    PublishInput
	}{
		{"x", PublishInput{Video: &Video{ContentType: "video/quicktime", SizeBytes: 12}}},
		{"x", PublishInput{Video: &Video{ContentType: "video/mp4", SizeBytes: (512 << 20) + 1}}},
		{"x", PublishInput{Video: &Video{ContentType: "video/mp4", SizeBytes: 12}, Images: []Image{{ContentType: "image/png"}}}},
		{"facebook", PublishInput{Video: &Video{ContentType: "video/mp4", SizeBytes: 12}}},
	} {
		if err := ValidatePost(tc.platform, tc.input); err == nil {
			t.Errorf("%s accepted unsupported video", tc.platform)
		}
	}
}

func TestXVideoProcessingStopsWhenCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client := NewClient(
		"x",
		Credentials{},
		&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			t.Error("processing continued after cancellation")
			return nil, errors.New("unexpected request")
		})},
	)
	data := map[string]json.RawMessage{
		"processing_info": json.RawMessage(`{"state":"in_progress","check_after_secs":30}`),
	}
	if err := client.waitForXVideo(ctx, "access", "video", data); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel processing = %v", err)
	}
}

func TestPublishUsesSelectedTargetAndReturnsReceipt(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"x", "facebook", "linkedin", "instagram"} {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			posts := 0
			client := NewClient(
				platform,
				Credentials{},
				&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Authorization") != "Bearer access" {
						t.Error("missing provider authorization")
					}
					body := `{"id":"post-id"}`
					switch r.URL.Path {
					case "/2/tweets":
						var payload struct {
							Text string `json:"text"`
						}
						if err := json.NewDecoder(r.Body).
							Decode(&payload); err != nil ||
							payload.Text != "Hello" {
							t.Error("wrong X post payload")
						}
						body = `{"data":{"id":"post-id"}}`
						posts++
					case "/v23.0/selected-account/feed":
						if err := r.ParseForm(); err != nil ||
							r.Form.Get("message") != "Hello" {
							t.Error("wrong Page payload")
						}
						posts++
					case "/v2/ugcPosts":
						var payload map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Fatal(err)
						}
						if textField(payload, "author") != "urn:li:person:selected-account" ||
							r.Header.Get("X-Restli-Protocol-Version") != "2.0.0" {
							t.Error("wrong LinkedIn author")
						}
						posts++
					case "/v23.0/selected-account/media":
						if err := r.ParseForm(); err != nil ||
							r.Form.Get("caption") != "Hello" ||
							r.Form.Get("image_url") != "https://objects.example/image.jpg" {
							t.Error("wrong Instagram container payload")
						}
						body = `{"id":"container-id"}`
					case "/v23.0/container-id":
						body = `{"status_code":"FINISHED"}`
					case "/v23.0/selected-account/media_publish":
						if err := r.ParseForm(); err != nil ||
							r.Form.Get("creation_id") != "container-id" {
							t.Error("wrong Instagram container")
						}
						posts++
					case "/v23.0/post-id":
						body = `{"permalink":"https://www.instagram.com/p/test/"}`
					default:
						t.Errorf("unexpected provider path %s", r.URL.Path)
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				})},
			)
			input := PublishInput{TargetID: "selected-account", Text: "Hello"}
			if platform == "instagram" {
				input.Images = []Image{
					{ContentType: "image/jpeg", URL: "https://objects.example/image.jpg"},
				}
			}
			got, err := client.Publish(context.Background(), Token{AccessToken: "access"}, input)
			if err != nil || got.ExternalID != "post-id" || posts != 1 {
				t.Fatalf("publish=%+v err=%v posts=%d", got, err, posts)
			}
		})
	}
}

func TestPublishImagesUploadBeforePublicPost(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"x", "linkedin", "facebook"} {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			uploaded, published := false, false
			client := NewClient(
				platform,
				Credentials{},
				&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					body := `{}`
					switch r.URL.Path {
					case "/2/media/upload":
						if err := r.ParseMultipartForm(1024); err != nil {
							t.Fatal(err)
						}
						file, _, err := r.FormFile("media")
						if err != nil {
							t.Fatal(err)
						}
						defer file.Close()
						content, _ := io.ReadAll(file)
						if string(content) != "image-bytes" ||
							r.FormValue("media_category") != "tweet_image" {
							t.Error("wrong media upload")
						}
						body = `{"data":{"id":"uploaded"}}`
						uploaded = true
					case "/2/tweets":
						var payload map[string]json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Fatal(err)
						}
						if !uploaded ||
							!strings.Contains(string(payload["media"]), "uploaded") {
							t.Error("post before selected media upload")
						}
						body = `{"data":{"id":"posted"}}`
						published = true
					case "/v2/assets":
						body = `{"value":{"asset":"urn:li:digitalmediaAsset:uploaded","uploadMechanism":{"com.linkedin.digitalmedia.uploading.MediaUploadHttpRequest":{"uploadUrl":"https://api.linkedin.com/mediaUpload/image"}}}}`
					case "/mediaUpload/image":
						content, _ := io.ReadAll(r.Body)
						if r.Method != http.MethodPut || string(content) != "image-bytes" {
							t.Error("wrong LinkedIn upload")
						}
						uploaded = true
						body = ""
					case "/v2/ugcPosts":
						content, _ := io.ReadAll(r.Body)
						if !uploaded ||
							!strings.Contains(
								string(content),
								"urn:li:digitalmediaAsset:uploaded",
							) {
							t.Error("wrong LinkedIn attachment")
						}
						published = true
						body = `{"id":"posted"}`
					case "/v23.0/page/photos":
						if err := r.ParseForm(); err != nil ||
							r.Form.Get("url") != "https://objects.example/image.jpg" ||
							r.Form.Get("published") != "true" {
							t.Error("wrong Page photo")
						}
						published = true
						body = `{"post_id":"posted"}`
					default:
						t.Errorf("unexpected image path %s", r.URL.Path)
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				})},
			)
			got, err := client.Publish(
				t.Context(),
				Token{AccessToken: "access"},
				PublishInput{
					TargetID: "page",
					Text:     "Caption",
					Images: []Image{
						{
							Content:     []byte("image-bytes"),
							ContentType: "image/jpeg",
							URL:         "https://objects.example/image.jpg",
						},
					},
				},
			)
			if err != nil || got.ExternalID != "posted" || !published {
				t.Fatalf("image publish=%+v %v", got, err)
			}
		})
	}
}

func TestPublishValidationRejectsUnsupportedMediaBeforeRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		platform string
		input    PublishInput
	}{
		{"instagram", PublishInput{Text: "no image"}},
		{"facebook", PublishInput{Images: make([]Image, 2)}},
		{"x", PublishInput{Text: strings.Repeat("x", 281)}},
	} {
		if err := ValidatePost(tc.platform, tc.input); err == nil {
			t.Errorf("%s accepted invalid post", tc.platform)
		}
	}
}
