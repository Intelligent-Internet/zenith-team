package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type Image struct {
	Content     []byte
	ContentType string
	URL         string
}

type PublishInput struct {
	TargetID, Text string
	Images         []Image
	Video          *Video
}
type PublishResult struct{ ExternalID, URL string }

func ValidatePost(platform string, input PublishInput) error {
	var maxText int
	maxImages := 1
	switch platform {
	case "x":
		maxText, maxImages = 280, 4
	case "facebook":
		maxText = 63206
	case "instagram":
		maxText = 2200
	case "linkedin":
		maxText = 3000
	default:
		return errors.New("unsupported social platform")
	}
	if input.Video != nil &&
		(platform != "x" || len(input.Images) != 0 || input.Video.ContentType != "video/mp4" || input.Video.SizeBytes <= 0 || input.Video.SizeBytes > MaxVideoBytes) {
		return errors.New("use one MP4 video up to 512 MB on X, without images")
	}
	if utf8.RuneCountInString(input.Text) > maxText || len(input.Images) > maxImages ||
		(strings.TrimSpace(input.Text) == "" && len(input.Images) == 0 && input.Video == nil) ||
		(platform == "instagram" && len(input.Images) != 1) {
		return errors.New("post exceeds this platform's text or image limits")
	}
	for _, image := range input.Images {
		if (image.ContentType != "image/jpeg" && image.ContentType != "image/png") ||
			(platform == "instagram" && image.ContentType != "image/jpeg") ||
			len(image.Content) > 5<<20 {
			return errors.New(
				"unsupported social image; use JPEG or PNG up to 5 MB, and JPEG for Instagram",
			)
		}
	}
	return nil
}

// PreparedPost contains media already uploaded and processed by the provider.
// Preparing never creates a public post, so callers can recheck authority before Publish.
type PreparedPost struct {
	client  *Client
	address string
	token   string
	form    url.Values
	payload any
}

func (c *Client) Prepare(ctx context.Context, token Token, input PublishInput) (PreparedPost, error) {
	if err := ValidatePost(c.platform, input); err != nil {
		return PreparedPost{}, err
	}
	switch c.platform {
	case "x":
		return c.prepareX(ctx, token, input)
	case "facebook":
		return c.prepareFacebook(token, input), nil
	case "instagram":
		return c.prepareInstagram(ctx, token, input)
	case "linkedin":
		return c.prepareLinkedIn(ctx, token, input)
	default:
		return PreparedPost{}, errors.New("unsupported social platform")
	}
}

func (c *Client) Publish(ctx context.Context, token Token, input PublishInput) (PublishResult, error) {
	post, err := c.Prepare(ctx, token, input)
	if err != nil {
		return PublishResult{}, err
	}
	return post.Publish(ctx)
}

func (p PreparedPost) Publish(ctx context.Context) (PublishResult, error) {
	if p.client == nil {
		return PublishResult{}, errors.New("social post has not been prepared")
	}
	c := p.client
	var data map[string]json.RawMessage
	var headers http.Header
	var err error
	if p.form != nil {
		data, err = c.request(ctx, http.MethodPost, p.address, p.form, p.token, false)
	} else {
		data, headers, err = c.jsonRequest(ctx, p.address, p.token, p.payload)
	}
	if err != nil {
		return PublishResult{}, err
	}
	id := textField(data, "id")
	switch c.platform {
	case "x":
		id = textField(object(data["data"]), "id")
		return receipt(id, "https://x.com/i/status/"+url.PathEscape(id))
	case "facebook":
		if postID := textField(data, "post_id"); postID != "" {
			id = postID
		}
		return receipt(id, "https://www.facebook.com/"+url.PathEscape(id))
	case "instagram":
		// A failed permalink lookup cannot turn a successful publish into a retry.
		info, _ := c.request(
			ctx,
			http.MethodGet,
			c.graphURL+"/"+c.credentials.GraphVersion+"/"+url.PathEscape(id)+"?fields=permalink",
			nil,
			p.token,
			false,
		)
		return receipt(id, textField(info, "permalink"))
	case "linkedin":
		if headerID := headers.Get("x-restli-id"); headerID != "" {
			id = headerID
		}
		return receipt(id, "https://www.linkedin.com/feed/update/"+url.PathEscape(id)+"/")
	default:
		return PublishResult{}, errors.New("unsupported social platform")
	}
}

func (c *Client) jsonRequest(
	ctx context.Context,
	address, token string,
	payload any,
) (map[string]json.RawMessage, http.Header, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(encoded))
	if err != nil {
		return nil, nil, errors.New("invalid social request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if c.platform == "linkedin" {
		req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	}
	return c.send(req)
}

func receipt(id, address string) (PublishResult, error) {
	if id == "" {
		return PublishResult{}, &ProviderError{Code: "invalid_response", Uncertain: true}
	}
	return PublishResult{ExternalID: id, URL: address}, nil
}

func (c *Client) prepareX(ctx context.Context, token Token, input PublishInput) (PreparedPost, error) {
	var mediaIDs []string
	if input.Video != nil {
		id, err := c.uploadXVideo(ctx, token, *input.Video)
		if err != nil {
			return PreparedPost{}, err
		}
		mediaIDs = append(mediaIDs, id)
	}
	for _, image := range input.Images {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("media_category", "tweet_image")
		_ = writer.WriteField("media_type", image.ContentType)
		head := textproto.MIMEHeader{
			"Content-Disposition": {`form-data; name="media"; filename="image"`},
			"Content-Type":        {image.ContentType},
		}
		part, err := writer.CreatePart(head)
		if err != nil {
			return PreparedPost{}, err
		}
		if _, err = part.Write(image.Content); err != nil {
			return PreparedPost{}, err
		}
		if err = writer.Close(); err != nil {
			return PreparedPost{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.x.com/2/media/upload", &body)
		if err != nil {
			return PreparedPost{}, err
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		data, _, err := c.send(req)
		if err != nil {
			return PreparedPost{}, err
		}
		id := textField(object(data["data"]), "id")
		if id == "" {
			return PreparedPost{}, errors.New("social image upload failed")
		}
		mediaIDs = append(mediaIDs, id)
	}
	payload := map[string]any{"text": input.Text}
	if len(mediaIDs) > 0 {
		payload["media"] = map[string]any{"media_ids": mediaIDs}
	}
	return PreparedPost{
		client:  c,
		address: "https://api.x.com/2/tweets",
		token:   token.AccessToken,
		payload: payload,
	}, nil
}

func (c *Client) prepareFacebook(token Token, input PublishInput) PreparedPost {
	path := "/" + url.PathEscape(input.TargetID) + "/feed"
	form := url.Values{"message": {input.Text}}
	if len(input.Images) > 0 {
		path = "/" + url.PathEscape(input.TargetID) + "/photos"
		form.Set("url", input.Images[0].URL)
		form.Set("published", "true")
	}
	return PreparedPost{client: c, address: c.graphURL + path, token: token.AccessToken, form: form}
}

func (c *Client) prepareInstagram(ctx context.Context, token Token, input PublishInput) (PreparedPost, error) {
	base := c.graphURL + "/" + c.credentials.GraphVersion
	data, err := c.request(
		ctx,
		http.MethodPost,
		base+"/"+url.PathEscape(input.TargetID)+"/media",
		url.Values{"image_url": {input.Images[0].URL}, "caption": {input.Text}},
		token.AccessToken,
		false,
	)
	if err != nil {
		return PreparedPost{}, err
	}
	container := textField(data, "id")
	if container == "" {
		return PreparedPost{}, errors.New("social image container missing")
	}
	for attempt := range 30 {
		data, err = c.request(
			ctx,
			http.MethodGet,
			base+"/"+url.PathEscape(container)+"?fields=status_code",
			nil,
			token.AccessToken,
			false,
		)
		if err != nil {
			return PreparedPost{}, err
		}
		status := textField(data, "status_code")
		if status == "FINISHED" {
			break
		}
		if status != "IN_PROGRESS" || attempt == 29 {
			return PreparedPost{}, errors.New("Instagram image is not ready to publish")
		}
		select {
		case <-ctx.Done():
			return PreparedPost{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return PreparedPost{
		client:  c,
		address: base + "/" + url.PathEscape(input.TargetID) + "/media_publish",
		token:   token.AccessToken,
		form:    url.Values{"creation_id": {container}},
	}, nil
}

func (c *Client) prepareLinkedIn(ctx context.Context, token Token, input PublishInput) (PreparedPost, error) {
	owner := "urn:li:person:" + input.TargetID
	content := map[string]any{
		"shareCommentary":    map[string]string{"text": input.Text},
		"shareMediaCategory": "NONE",
	}
	if len(input.Images) > 0 {
		asset, err := c.uploadLinkedInImage(ctx, token, owner, input.Images[0])
		if err != nil {
			return PreparedPost{}, err
		}
		content["shareMediaCategory"] = "IMAGE"
		content["media"] = []map[string]string{{"status": "READY", "media": asset}}
	}
	payload := map[string]any{
		"author":          owner,
		"lifecycleState":  "PUBLISHED",
		"specificContent": map[string]any{"com.linkedin.ugc.ShareContent": content},
		"visibility":      map[string]string{"com.linkedin.ugc.MemberNetworkVisibility": "PUBLIC"},
	}
	return PreparedPost{
		client:  c,
		address: "https://api.linkedin.com/v2/ugcPosts",
		token:   token.AccessToken,
		payload: payload,
	}, nil
}

func (c *Client) uploadLinkedInImage(ctx context.Context, token Token, owner string, image Image) (string, error) {
	payload := map[string]any{
		"registerUploadRequest": map[string]any{
			"owner":   owner,
			"recipes": []string{"urn:li:digitalmediaRecipe:feedshare-image"},
			"serviceRelationships": []map[string]string{
				{"relationshipType": "OWNER", "identifier": "urn:li:userGeneratedContent"},
			},
			"supportedUploadMechanism": []string{"SYNCHRONOUS_UPLOAD"},
		},
	}
	data, _, err := c.jsonRequest(
		ctx,
		"https://api.linkedin.com/v2/assets?action=registerUpload",
		token.AccessToken,
		payload,
	)
	if err != nil {
		return "", err
	}
	value := object(data["value"])
	upload := object(object(value["uploadMechanism"])["com.linkedin.digitalmedia.uploading.MediaUploadHttpRequest"])
	address, err := url.Parse(textField(upload, "uploadUrl"))
	if err != nil || address.Scheme != "https" || address.User != nil || address.Port() != "" ||
		(address.Hostname() != "www.linkedin.com" && address.Hostname() != "api.linkedin.com") {
		return "", errors.New("invalid LinkedIn upload destination")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, address.String(), bytes.NewReader(image.Content))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	_, _, err = c.send(req)
	if err != nil {
		return "", err
	}
	asset := textField(value, "asset")
	if asset == "" {
		return "", errors.New("LinkedIn upload did not return an asset")
	}
	return asset, nil
}
