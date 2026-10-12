package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// MaxVideoBytes bounds the files exposed to Agents and accepted for publication.
const MaxVideoBytes int64 = 512 << 20

type Video struct {
	Content     io.Reader
	ContentType string
	SizeBytes   int64
}

func (c *Client) uploadXVideo(ctx context.Context, token Token, video Video) (string, error) {
	if video.Content == nil {
		return "", errors.New("missing video content")
	}
	data, _, err := c.jsonRequest(
		ctx,
		"https://api.x.com/2/media/upload/initialize",
		token.AccessToken,
		map[string]any{
			"media_type":     video.ContentType,
			"total_bytes":    video.SizeBytes,
			"media_category": "tweet_video",
		},
	)
	if err != nil {
		return "", err
	}
	id := textField(object(data["data"]), "id")
	if id == "" {
		return "", errors.New("video upload did not return a media ID")
	}
	base := "https://api.x.com/2/media/upload/" + url.PathEscape(id)
	chunk := make([]byte, 4<<20)
	remaining := video.SizeBytes
	for segment := 0; remaining > 0; segment++ {
		size := min(int64(len(chunk)), remaining)
		if _, err := io.ReadFull(video.Content, chunk[:size]); err != nil {
			return "", errors.New("could not read complete video content")
		}
		if err := c.appendXVideo(ctx, token.AccessToken, base, segment, chunk[:size]); err != nil {
			return "", err
		}
		remaining -= size
	}
	if count, err := video.Content.Read(chunk[:1]); count != 0 || err != io.EOF {
		return "", errors.New("video size does not match its attachment")
	}
	data, err = c.request(ctx, http.MethodPost, base+"/finalize", nil, token.AccessToken, false)
	if err != nil {
		return "", err
	}
	if err := c.waitForXVideo(ctx, token.AccessToken, id, object(data["data"])); err != nil {
		return "", err
	}
	return id, nil
}

func (c *Client) appendXVideo(ctx context.Context, token, base string, segment int, content []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("segment_index", strconv.Itoa(segment)); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("media", "video.mp4")
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/append", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	_, _, err = c.send(req)
	return err
}

func (c *Client) waitForXVideo(ctx context.Context, token, id string, data map[string]json.RawMessage) error {
	for attempt := 0; ; attempt++ {
		info := object(data["processing_info"])
		switch textField(info, "state") {
		case "succeeded":
			return nil
		case "":
			if len(info) == 0 && attempt == 0 {
				return nil
			}
			return errors.New("missing video processing status")
		case "pending", "in_progress":
		default:
			return errors.New("X could not process the video")
		}
		if attempt == 60 {
			return errors.New("video processing did not finish")
		}
		var seconds int
		if err := json.Unmarshal(info["check_after_secs"], &seconds); err != nil || seconds < 1 {
			seconds = 1
		}
		if seconds > 120 {
			return errors.New("video processing exceeds the publishing deadline")
		}
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		result, err := c.request(
			ctx,
			http.MethodGet,
			"https://api.x.com/2/media/upload?"+url.Values{
				"command":  {"STATUS"},
				"media_id": {id},
			}.Encode(),
			nil,
			token,
			false,
		)
		if err != nil {
			return err
		}
		data = object(result["data"])
	}
}
