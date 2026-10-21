package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	localPreviewPath             = "/local-preview"
	localPreviewChannelText      = "text"
	localPreviewChannelReasoning = "reasoning"
	localPreviewOperationAppend  = "append"
	localPreviewOperationReplace = "replace"
	localPreviewMaxValueBytes    = 512
	localPreviewMaxContentBytes  = 64 << 10
)

type localRunPreviewFrame struct {
	Type      string `json:"type"`
	CompanyID string `json:"companyId"`
	// RunID is the Run UUID as text, the same id the cloud planes carry. This
	// loopback frame is disposable scratch the desktop correlates by string
	// key and validates as bounded text.
	RunID          string `json:"runId"`
	ConversationID string `json:"conversationId"`
	ItemKey        string `json:"itemKey"`
	Channel        string `json:"channel"`
	Operation      string `json:"operation"`
	Content        string `json:"content"`
}

type localPreviewResetFrame struct {
	Type string `json:"type"`
}

type localPreviewConfig struct {
	Version    int    `json:"version"`
	Endpoint   string `json:"endpoint"`
	Capability string `json:"capability"`
}

// localPreviewHub is the daemon-local, disposable preview fanout. Publish
// holds no socket and never waits for a reader; each subscriber has an
// independently bounded queue whose oldest preview is discarded under load.
type localPreviewHub struct {
	mu            sync.Mutex
	maxQueueBytes int
	subscribers   map[*localPreviewQueue]struct{}
}

func newLocalPreviewHub(maxQueueBytes int) *localPreviewHub {
	return &localPreviewHub{
		maxQueueBytes: maxQueueBytes,
		subscribers:   make(map[*localPreviewQueue]struct{}),
	}
}

func (h *localPreviewHub) subscribe() (*localPreviewQueue, func(), bool) {
	h.mu.Lock()
	if len(h.subscribers) >= 4 {
		h.mu.Unlock()
		return nil, func() {}, false
	}
	queue := newLocalPreviewQueue(h.maxQueueBytes)
	h.subscribers[queue] = struct{}{}
	h.mu.Unlock()
	return queue, func() {
		h.mu.Lock()
		delete(h.subscribers, queue)
		h.mu.Unlock()
	}, true
}

func (h *localPreviewHub) publish(frame localRunPreviewFrame) {
	frame, ok := normalizeLocalRunPreviewFrame(frame)
	if !ok {
		return
	}
	h.mu.Lock()
	subscribers := make([]*localPreviewQueue, 0, len(h.subscribers))
	for subscriber := range h.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	h.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber.enqueue(frame)
	}
}

type localPreviewQueue struct {
	mu       sync.Mutex
	frames   []localRunPreviewFrame
	bytes    int
	maxBytes int
	notify   chan struct{}
}

func newLocalPreviewQueue(maxBytes int) *localPreviewQueue {
	return &localPreviewQueue{
		maxBytes: maxBytes,
		// The buffer stores one wakeup, not preview data. Multiple publishes
		// coalesce while next drains the bounded frame slice.
		notify: make(chan struct{}, 1),
	}
}

func (q *localPreviewQueue) enqueue(frame localRunPreviewFrame) {
	if frame.Content == "" || q.maxBytes <= 0 || len(frame.Content) > q.maxBytes {
		return
	}

	q.mu.Lock()
	lastIndex := len(q.frames) - 1
	coalescable := lastIndex >= 0 && sameLocalPreviewStream(q.frames[lastIndex], frame)
	switch {
	case coalescable &&
		q.frames[lastIndex].Operation == localPreviewOperationAppend &&
		frame.Operation == localPreviewOperationAppend &&
		len(q.frames[lastIndex].Content)+len(frame.Content) <= localPreviewMaxContentBytes:
		q.frames[lastIndex].Content += frame.Content
		q.bytes += len(frame.Content)
	case coalescable &&
		q.frames[lastIndex].Operation == localPreviewOperationReplace &&
		frame.Operation == localPreviewOperationReplace:
		q.bytes -= len(q.frames[lastIndex].Content)
		q.frames[lastIndex] = frame
		q.bytes += len(frame.Content)
	default:
		q.frames = append(q.frames, frame)
		q.bytes += len(frame.Content)
	}
	for (q.bytes > q.maxBytes || len(q.frames) > 256) &&
		len(q.frames) > 0 {
		q.bytes -= len(q.frames[0].Content)
		q.frames = q.frames[1:]
	}
	q.mu.Unlock()

	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *localPreviewQueue) next(ctx context.Context) (localRunPreviewFrame, bool) {
	for {
		q.mu.Lock()
		if len(q.frames) > 0 {
			frame := q.frames[0]
			q.frames = q.frames[1:]
			q.bytes -= len(frame.Content)
			q.mu.Unlock()
			return frame, true
		}
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			return localRunPreviewFrame{}, false
		case <-q.notify:
		}
	}
}

func sameLocalPreviewStream(left, right localRunPreviewFrame) bool {
	return left.CompanyID == right.CompanyID &&
		left.RunID == right.RunID &&
		left.ConversationID == right.ConversationID &&
		left.ItemKey == right.ItemKey &&
		left.Channel == right.Channel
}

func normalizeLocalRunPreviewFrame(frame localRunPreviewFrame) (localRunPreviewFrame, bool) {
	if frame.CompanyID == "" || len(frame.CompanyID) > localPreviewMaxValueBytes ||
		frame.RunID == "" || len(frame.RunID) > localPreviewMaxValueBytes ||
		frame.ConversationID == "" || len(frame.ConversationID) > localPreviewMaxValueBytes ||
		frame.ItemKey == "" || len(frame.ItemKey) > localPreviewMaxValueBytes ||
		(frame.Channel != localPreviewChannelText && frame.Channel != localPreviewChannelReasoning) ||
		(frame.Operation != localPreviewOperationAppend &&
			frame.Operation != localPreviewOperationReplace) ||
		frame.Content == "" {
		return localRunPreviewFrame{}, false
	}
	frame.Type = "run.preview.delta"
	if len(frame.Content) > localPreviewMaxContentBytes {
		frame.Content = frame.Content[:localPreviewMaxContentBytes]
	}
	return frame, true
}

// publishLocalRunPreview is the execution integration hook. It is intentionally
// synchronous and nonblocking with respect to network I/O.
func (d *Daemon) publishLocalRunPreview(frame localRunPreviewFrame) {
	if d.localPreview == nil {
		return
	}
	d.localPreview.publish(frame)
}

func (d *Daemon) localPreviewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.localPreview == nil || d.localPreviewToken == "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !validLocalPreviewAuthorization(r.Header.Get("Authorization"), d.localPreviewToken) {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
			return
		}

		queue, unsubscribe, ok := d.localPreview.subscribe()
		if !ok {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		defer unsubscribe()
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(
			time.Now().Add(5 * time.Second),
		); err != nil &&
			!errors.Is(err, http.ErrNotSupported) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		encoder := json.NewEncoder(w)
		if err := encoder.Encode(localPreviewResetFrame{Type: "run.preview.reset"}); err != nil {
			return
		}
		flusher.Flush()
		for {
			frame, open := queue.next(r.Context())
			if !open {
				return
			}
			if err := controller.SetWriteDeadline(
				time.Now().Add(5 * time.Second),
			); err != nil &&
				!errors.Is(err, http.ErrNotSupported) {
				return
			}
			if err := encoder.Encode(frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func validLocalPreviewAuthorization(header, capability string) bool {
	provided, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || len(provided) != len(capability) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(capability)) == 1
}

// prepareLocalPreview rotates the reconstructible desktop-only capability and
// writes the reconnect configuration beside the daemon credential. A failure
// disables previews without preventing durable run execution. Keep the file
// after shutdown: the listener can close before cleanup completes, allowing a
// replacement daemon to publish here first. A retained capability is useless
// once its daemon exits and is rotated on the next desktop startup.
func (d *Daemon) prepareLocalPreview(listener net.Listener) error {
	if d.cfg.LaunchedBy != "desktop" {
		return nil
	}
	capability, err := newLocalPreviewCapability()
	if err != nil {
		return err
	}
	directory, err := ZenithTeamHome()
	if err != nil {
		return err
	}
	endpoint := "http://" + listener.Addr().String() + localPreviewPath
	if _, err := writeLocalPreviewConfig(directory, endpoint, capability); err != nil {
		return err
	}
	d.localPreviewToken = capability
	return nil
}

func newLocalPreviewCapability() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate local preview capability: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func writeLocalPreviewConfig(directory, endpoint, capability string) (string, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create local preview directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".local-preview-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create local preview config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", fmt.Errorf("secure local preview config: %w", err)
	}
	if err := json.NewEncoder(temporary).Encode(localPreviewConfig{
		Version:    1,
		Endpoint:   endpoint,
		Capability: capability,
	}); err != nil {
		temporary.Close()
		return "", fmt.Errorf("encode local preview config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync local preview config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close local preview config: %w", err)
	}
	path := filepath.Join(directory, "local-preview.json")
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", fmt.Errorf("install local preview config: %w", err)
	}
	return path, nil
}
