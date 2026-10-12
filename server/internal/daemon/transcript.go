package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/daemon/execenv"
	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
	"boardly.dev/boardly/server/pkg/redact"
)

// runStreamOutcome is everything the Run orchestrator needs after the deep
// provider-stream module has consumed the ordered execution interface.
type runStreamOutcome struct {
	Result     agent.Result
	ToolCount  int32
	Transcript RunTranscriptProof
	// PendingItem is the completed item whose append was sent but never
	// acknowledged. It travels with the outcome so the terminal transition can
	// replay it and discover what the server actually committed.
	PendingItem *protocol.RunTranscriptAppendRequest
}

// runStreamPresentation is how a Run's generated files reach its chat: the
// workdir the daemon copies them into, the presenter, and the Run's own and
// hidden folders, which bound what the daemon reads for it (Access TECH B3e,
// B7b).
type runStreamPresentation struct {
	workDir   string
	presenter RunOutputPresenter
	launch    execenv.SeatbeltOptions
}

// errTranscriptDeliveryUnavailable means completed-item delivery failed
// continuously for the whole delivery window — in practice, the daemon WS
// cannot be established (a proxy that blocks upgrades) or the server refuses
// every append. Mapped to a retryable infrastructure failure so auto-retry
// resumes the work when the lane returns, instead of the run hanging in
// "running" with the idle watchdog suppressed and nothing logged.
var errTranscriptDeliveryUnavailable = errors.New(
	"transcript delivery unavailable: completed items cannot reach the server",
)

// appendTranscriptItem delivers one completed provider item exclusively over
// the daemon WebSocket. An uncertain response is safe to retry because the
// server arbitrates the immutable natural identity (runId, itemKey).
func (d *Daemon) appendTranscriptItem(
	ctx context.Context,
	request protocol.RunTranscriptAppendRequest,
) (protocol.RunTranscriptAppendReceipt, error) {
	// The retry interval backs off to a cap and the wait is bounded by the
	// delivery window, not just ctx: delivery is lossless while the lane can
	// plausibly return, but a WS that never comes up must eventually surface
	// as a retryable failure rather than a silent forever-running run.
	const (
		initialRetryBackoff = 100 * time.Millisecond
		maxRetryBackoff     = 5 * time.Second
		attemptsPerWarn     = 6 // ≈ one Warn per 30s once backoff saturates
	)
	backoff := initialRetryBackoff
	started := time.Now()
	attempt := 0
	for {
		var receipt protocol.RunTranscriptAppendReceipt
		status, err := d.wsRPC.CallIfRPCV1Supported(
			ctx,
			protocol.DaemonRPCMethodRunTranscriptAppend,
			30*time.Second,
			request,
			&receipt,
		)
		if err == nil {
			return receipt, nil
		}
		if status >= 400 && status < 500 && status != 429 {
			return protocol.RunTranscriptAppendReceipt{}, fmt.Errorf(
				"append run transcript item: %w",
				err,
			)
		}
		if ctx.Err() != nil {
			return protocol.RunTranscriptAppendReceipt{}, fmt.Errorf(
				"append run transcript item: %w",
				ctx.Err(),
			)
		}
		attempt++
		if attempt == 1 || attempt%attemptsPerWarn == 0 {
			slog.Warn(
				"transcript append retrying; completed item not yet durable",
				"run_id", request.RunID,
				"item_key", request.ItemKey,
				"attempt", attempt,
				"elapsed", time.Since(started).Round(time.Second).String(),
				"status", status,
				"error", err,
			)
		}
		if window := d.transcriptDeliveryWindow(); time.Since(started) >= window {
			return protocol.RunTranscriptAppendReceipt{}, fmt.Errorf(
				"append run transcript item after %s: %w (last error: %w)",
				window, errTranscriptDeliveryUnavailable, err,
			)
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return protocol.RunTranscriptAppendReceipt{}, fmt.Errorf(
				"append run transcript item: %w",
				ctx.Err(),
			)
		case <-timer.C:
		}
		backoff *= 2
		if backoff > maxRetryBackoff {
			backoff = maxRetryBackoff
		}
	}
}

// defaultTranscriptDeliveryWindow bounds how long one completed item may
// retry before the run fails retryably. Generous: it must ride out server
// deploys and network blips, and only convert the truly wedged case.
const defaultTranscriptDeliveryWindow = 12 * time.Minute

func (d *Daemon) transcriptDeliveryWindow() time.Duration {
	if d.transcriptDeliveryWindowOverride > 0 {
		return d.transcriptDeliveryWindowOverride
	}
	return defaultTranscriptDeliveryWindow
}

// executeRunStream owns the provider-neutral streaming contract. Disposable
// deltas go only to the bounded local preview adapter. Each completed item is
// normalized, redacted, and synchronously acknowledged by PostgreSQL before
// the next provider event is consumed. Finished therefore cannot overtake the
// durable transcript prefix.
func (d *Daemon) executeRunStream(
	ctx context.Context,
	provider agent.Provider,
	prompt string,
	opts agent.ExecOptions,
	runLog *slog.Logger,
	run Run,
	codexHome string,
	presentation ...runStreamPresentation,
) (runStreamOutcome, error) {
	var generated runStreamPresentation
	if len(presentation) > 0 {
		generated = presentation[0]
	}
	head := RunTranscriptProof{}
	agentCtx, agentCancel := context.WithCancel(ctx)
	defer agentCancel()

	execution, err := provider.Execute(agentCtx, prompt, opts)
	if err != nil {
		return runStreamOutcome{Transcript: head}, wrapRunPreparationError(
			runPreparationPhaseProviderSpawn,
			err,
		)
	}
	if execution == nil || execution.Events == nil {
		return runStreamOutcome{Transcript: head}, wrapRunPreparationError(
			runPreparationPhaseProviderSpawn,
			errors.New("provider returned no execution event stream"),
		)
	}
	d.runningRuns.Add(1)
	defer d.runningRuns.Add(-1)
	runLog.Debug("provider started, consuming ordered events")

	var streamCtx context.Context
	var streamCancel context.CancelFunc
	if opts.Timeout > 0 {
		streamCtx, streamCancel = context.WithTimeout(agentCtx, opts.Timeout+30*time.Second)
	} else {
		streamCtx, streamCancel = context.WithCancel(agentCtx)
	}
	defer streamCancel()

	var toolCount atomic.Int32
	var inFlightTools atomic.Int32
	activeTools := make(map[string]struct{})
	seenTools := make(map[string]struct{})
	var appendInFlight atomic.Bool
	var lastActivityAt atomic.Int64
	lastActivityAt.Store(time.Now().UnixNano())
	var idleWatchdogFired atomic.Bool
	idleWindow := d.cfg.AgentIdleWatchdog
	if idleWindow > 0 && opts.IdleWatchdogTimeout > 0 && opts.IdleWatchdogTimeout < idleWindow {
		idleWindow = opts.IdleWatchdogTimeout
	}
	var idleWatchdogThreshold atomic.Int64
	idleWatchdogThreshold.Store(int64(idleWindow))
	if idleWindow > 0 {
		go d.runIdleWatchdog(
			agentCtx,
			idleWindow,
			d.cfg.AgentToolWatchdog,
			&lastActivityAt,
			&inFlightTools,
			&idleWatchdogFired,
			&idleWatchdogThreshold,
			&appendInFlight,
			agentCancel,
			runLog,
			run.ID,
		)
	}

	var sessionPinned atomic.Bool
	for {
		select {
		case event, open := <-execution.Events:
			if !open {
				return runStreamOutcome{
					ToolCount: toolCount.Load(), Transcript: head,
				}, errors.New("provider event stream closed before Finished")
			}
			lastActivityAt.Store(time.Now().UnixNano())
			switch event := event.(type) {
			case agent.ItemDelta:
				d.publishRunItemDelta(run, event)
			case agent.ToolActivity:
				if activityErr := applyToolActivity(
					activeTools,
					seenTools,
					&toolCount,
					&inFlightTools,
					event,
				); activityErr != nil {
					cancelAndDrainExecution(agentCancel, execution.Events)
					return runStreamOutcome{
						ToolCount: toolCount.Load(), Transcript: head,
					}, activityErr
				}
			case agent.CompletedItem:
				if err := d.presentGeneratedFiles(
					streamCtx, generated, event, runLog,
				); err != nil {
					// Artifact publication must not turn a successful provider image
					// into a failed Run; the image remains in the provider transcript.
					runLog.Warn("publish generated image artifact failed", "error", err)
				}
				request, mapErr := transcriptAppendRequest(run.ID, event)
				if mapErr != nil {
					cancelAndDrainExecution(agentCancel, execution.Events)
					return runStreamOutcome{
						ToolCount: toolCount.Load(), Transcript: head,
					}, mapErr
				}
				applyCompletedItemToolActivity(
					activeTools,
					seenTools,
					&toolCount,
					&inFlightTools,
					event,
				)
				appendInFlight.Store(true)
				receipt, appendErr := d.appendTranscriptItem(streamCtx, request)
				if appendErr != nil {
					appendInFlight.Store(false)
					cancelAndDrainExecution(agentCancel, execution.Events)
					// The frame was sent, so the item may well be committed
					// while the head below still says it is not. Carrying it
					// out is what lets the terminal transition find out.
					return runStreamOutcome{
						ToolCount: toolCount.Load(), Transcript: head,
						PendingItem: &request,
					}, appendErr
				}
				if updateErr := updateRunTranscriptHead(&head, request, receipt); updateErr != nil {
					appendInFlight.Store(false)
					cancelAndDrainExecution(agentCancel, execution.Events)
					return runStreamOutcome{
						ToolCount: toolCount.Load(), Transcript: head,
						PendingItem: &request,
					}, updateErr
				}
				event.Acknowledge()
				lastActivityAt.Store(time.Now().UnixNano())
				appendInFlight.Store(false)
			case agent.SessionReady:
				if event.SessionID != "" && !sessionPinned.Swap(true) {
					d.pinRunSessionWhenReady(streamCtx, run.ID, codexHome, event.SessionID, runLog)
				}
			case agent.Finished:
				result := event.Result
				if idleWatchdogFired.Load() {
					result.Status = agent.ResultIdleWatchdog
					if result.Error == "" {
						result.Error = idleWatchdogReason(
							time.Duration(idleWatchdogThreshold.Load()),
						)
					}
				}
				return runStreamOutcome{
					Result: result, ToolCount: toolCount.Load(), Transcript: head,
				}, nil
			default:
				cancelAndDrainExecution(agentCancel, execution.Events)
				return runStreamOutcome{
					ToolCount: toolCount.Load(), Transcript: head,
				}, fmt.Errorf("provider emitted unsupported event %T", event)
			}
		case <-streamCtx.Done():
			cancelAndDrainExecution(agentCancel, execution.Events)
			result := resultForRunStreamStop(
				streamCtx.Err(),
				idleWatchdogFired.Load(),
				time.Duration(idleWatchdogThreshold.Load()),
			)
			return runStreamOutcome{
				Result: result, ToolCount: toolCount.Load(), Transcript: head,
			}, nil
		}
	}
}

func (d *Daemon) presentGeneratedFiles(
	ctx context.Context,
	presentation runStreamPresentation,
	item agent.CompletedItem,
	runLog *slog.Logger,
) error {
	if presentation.presenter == nil || presentation.workDir == "" || item.Role != agent.RoleTool {
		return nil
	}
	for _, part := range item.Parts {
		result, ok := part.(agent.ToolResultPart)
		if !ok || result.Status != agent.ToolResultStatusCompleted || result.FilePath == "" {
			continue
		}
		if err := presentGeneratedFile(ctx, presentation, item, result); err != nil {
			return err
		}
	}
	return nil
}

func presentGeneratedFile(
	ctx context.Context,
	presentation runStreamPresentation,
	item agent.CompletedItem,
	result agent.ToolResultPart,
) error {
	presenter, workDir := presentation.presenter, presentation.workDir
	sourcePath := result.FilePath
	// The provider writes the image in folders the Agent can change, and the
	// daemon reads it outside the sandbox, so it opens only a file the Run
	// itself may read (Access TECH B3e, B7b).
	source, err := openRunFile(sourcePath, presentation.launch)
	if err != nil {
		return fmt.Errorf("open generated image: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("stat generated image: %w", err)
	}
	if info.Size() <= 0 || info.Size() > 100<<20 {
		return fmt.Errorf("generated image size is outside the supported range")
	}
	name := filepath.Base(sourcePath)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "generated-image.png"
	}
	destinationDir := filepath.Join(workDir, ".zenith-generated")
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return fmt.Errorf("create generated image directory: %w", err)
	}
	itemKey := strings.NewReplacer("/", "_", `\`, "_").Replace(item.Key)
	if itemKey == "" {
		itemKey = "image"
	}
	label, summary := generatedArtifactMetadata(item, result, name)
	destinationName := artifactSlug(label) + "-" + itemKey[:minInt(len(itemKey), 8)] + filepath.Ext(name)
	destination := filepath.Join(destinationDir, destinationName)
	target, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create generated image copy: %w", err)
		}
	} else {
		if _, err := io.Copy(target, source); err != nil {
			target.Close()
			return fmt.Errorf("copy generated image: %w", err)
		}
		if err := target.Close(); err != nil {
			return fmt.Errorf("close generated image copy: %w", err)
		}
	}
	relative, err := filepath.Rel(workDir, destination)
	if err != nil {
		return fmt.Errorf("resolve generated image path: %w", err)
	}
	_, err = presenter.PresentOutput(ctx, tools.PresentOutputInput{
		Type:    presentedArtifactType(name),
		Title:   label,
		Summary: summary,
		Items: []tools.PresentOutputItem{{
			Role:  "primary",
			Label: label,
			Target: tools.PresentOutputTarget{
				Kind:                  "workspace_file",
				WorkspaceRelativePath: relative,
			},
		}},
	})
	return err
}

func generatedArtifactMetadata(item agent.CompletedItem, result agent.ToolResultPart, fallback string) (string, string) {
	label := strings.TrimSuffix(fallback, filepath.Ext(fallback))
	summary := "File generated outside Zenith Studio and saved to this chat."
	if result.Name == "image_generation" {
		label = "Generated image"
		if prompt := completedToolPrompt(item); prompt != "" {
			for _, prefix := range []string{"Asset type:", "Subject:", "Use case:"} {
				for _, line := range strings.Split(prompt, "\n") {
					if strings.HasPrefix(line, prefix) {
						label = strings.TrimSpace(strings.TrimPrefix(line, prefix))
						break
					}
				}
				if label != "Generated image" {
					break
				}
			}
		}
		summary = "Image generated from the chat request and saved to this conversation."
	}
	if label == "" {
		label = "Generated file"
	}
	return capitalizeFirst(label), summary
}

func completedToolPrompt(item agent.CompletedItem) string {
	for _, part := range item.Parts {
		call, ok := part.(agent.ToolCallPart)
		if ok && call.Name == "image_generation" {
			prompt, _ := call.Input["revisedPrompt"].(string)
			return prompt
		}
	}
	return ""
}

func artifactSlug(value string) string {
	value = strings.ToLower(value)
	value = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, value)
	return strings.Trim(value, "-")
}

func capitalizeFirst(value string) string {
	if value == "" {
		return value
	}
	runes := []rune(value)
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func presentedArtifactType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	contentType := mime.TypeByExtension(ext)
	if strings.HasPrefix(contentType, "image/") || strings.HasPrefix(contentType, "video/") {
		return "media"
	}
	return "file"
}

// cancelAndDrainExecution releases provider resources after the daemon can no
// longer process the stream. Remaining events are deliberately discarded: the
// durable transcript head returned with the original error must not advance.
func cancelAndDrainExecution(cancel context.CancelFunc, events <-chan agent.Event) {
	cancel()
	//nolint:revive // empty-block: discarding every remaining event is the drain.
	for range events {
	}
}

func transcriptAppendRequest(
	runID uuid.UUID,
	item agent.CompletedItem,
) (protocol.RunTranscriptAppendRequest, error) {
	if item.Key == "" || strings.TrimSpace(item.Key) != item.Key {
		return protocol.RunTranscriptAppendRequest{}, errors.New(
			"provider completed item has no canonical stable key",
		)
	}
	role, err := transcriptRole(item.Role)
	if err != nil {
		return protocol.RunTranscriptAppendRequest{}, err
	}
	parts := make([]protocol.RunItemPart, 0, len(item.Parts))
	for _, part := range item.Parts {
		switch part := part.(type) {
		case agent.TextPart:
			parts = append(parts, protocol.RunItemPart{
				Type: protocol.RunItemPartTypeText, Text: redact.Redact(part.Text), Final: part.Final,
			})
		case agent.ReasoningPart:
			parts = append(parts, protocol.RunItemPart{
				Type: protocol.RunItemPartTypeReasoning, Text: redact.Redact(part.Text),
			})
		case agent.ToolCallPart:
			inputValue := part.Input
			if inputValue == nil {
				inputValue = map[string]any{}
			}
			input, marshalErr := json.Marshal(inputValue)
			if marshalErr != nil {
				return protocol.RunTranscriptAppendRequest{}, fmt.Errorf(
					"encode provider tool call %q: %w",
					part.Name,
					marshalErr,
				)
			}
			parts = append(parts, protocol.RunItemPart{
				Type: protocol.RunItemPartTypeToolCall, CallID: part.CallID,
				Name: part.Name, Input: redact.JSON(input),
			})
		case agent.ToolResultPart:
			parts = append(parts, protocol.RunItemPart{
				Type: protocol.RunItemPartTypeToolResult, CallID: part.CallID,
				Name: part.Name, Output: redact.JSONOrText(part.Output),
				Status:   protocol.RunToolResultStatus(part.Status),
				ExitCode: part.ExitCode, DurationMs: part.DurationMs,
			})
		case agent.ErrorPart:
			parts = append(parts, protocol.RunItemPart{
				Type: protocol.RunItemPartTypeError, Text: redact.Redact(part.Message),
			})
		default:
			return protocol.RunTranscriptAppendRequest{}, fmt.Errorf(
				"provider completed item contains unsupported part %T",
				part,
			)
		}
	}
	if len(parts) == 0 {
		return protocol.RunTranscriptAppendRequest{}, errors.New(
			"provider completed item has no durable parts",
		)
	}
	request := protocol.RunTranscriptAppendRequest{
		RunID: runID, ItemKey: item.Key, Role: role, Parts: parts,
	}
	content, err := json.Marshal(struct {
		ItemKey string                 `json:"itemKey"`
		Role    protocol.RunItemRole   `json:"role"`
		Parts   []protocol.RunItemPart `json:"parts"`
	}{ItemKey: request.ItemKey, Role: request.Role, Parts: request.Parts})
	if err != nil {
		return protocol.RunTranscriptAppendRequest{}, fmt.Errorf(
			"encode provider completed item %q: %w",
			item.Key,
			err,
		)
	}
	if len(content) > protocol.RunTranscriptMaxItemBytes {
		return protocol.RunTranscriptAppendRequest{}, fmt.Errorf(
			"provider completed item %q exceeds %d-byte transcript limit",
			item.Key,
			protocol.RunTranscriptMaxItemBytes,
		)
	}
	return request, nil
}

// transcriptRole validates the provider role against the closed wire
// vocabulary. The two vocabularies are now identical (agent/tool/system), so
// this is a guard, not a translation.
func transcriptRole(role agent.Role) (protocol.RunItemRole, error) {
	switch role {
	case agent.RoleAgent:
		return protocol.RunItemRoleAgent, nil
	case agent.RoleTool:
		return protocol.RunItemRoleTool, nil
	case agent.RoleSystem:
		return protocol.RunItemRoleSystem, nil
	default:
		return "", fmt.Errorf("provider completed item has unsupported role %q", role)
	}
}

func applyToolActivity(
	active map[string]struct{},
	seen map[string]struct{},
	total, inFlight *atomic.Int32,
	activity agent.ToolActivity,
) error {
	if activity.ItemKey == "" {
		return errors.New("provider tool activity has no stable item key")
	}
	switch activity.Phase {
	case agent.ToolActivityPhaseStarted:
		startToolActivity(active, seen, total, inFlight, activity.ItemKey)
	default:
		return fmt.Errorf("provider tool activity has unsupported phase %q", activity.Phase)
	}
	return nil
}

func applyCompletedItemToolActivity(
	active map[string]struct{},
	seen map[string]struct{},
	total, inFlight *atomic.Int32,
	item agent.CompletedItem,
) {
	for _, part := range item.Parts {
		switch part := part.(type) {
		case agent.ToolCallPart:
			startToolActivity(active, seen, total, inFlight, part.CallID)
		case agent.ToolResultPart:
			finishToolActivity(active, inFlight, part.CallID)
		}
	}
}

func startToolActivity(
	active map[string]struct{},
	seen map[string]struct{},
	total, inFlight *atomic.Int32,
	key string,
) {
	if key == "" {
		return
	}
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	active[key] = struct{}{}
	total.Add(1)
	inFlight.Add(1)
}

func finishToolActivity(active map[string]struct{}, inFlight *atomic.Int32, key string) {
	if key == "" {
		return
	}
	if _, exists := active[key]; !exists {
		return
	}
	delete(active, key)
	inFlight.Add(-1)
}

// updateRunTranscriptHead advances the durable head to what PostgreSQL just
// acknowledged. It reads the mapped request rather than the provider item so
// the terminal replay, which no longer holds the item, advances the head by
// exactly the same rule as the stream does.
func updateRunTranscriptHead(
	head *RunTranscriptProof,
	request protocol.RunTranscriptAppendRequest,
	receipt protocol.RunTranscriptAppendReceipt,
) error {
	if receipt.RunID != request.RunID || receipt.ItemKey != request.ItemKey ||
		receipt.Seq < 1 || receipt.ThroughSeq < receipt.Seq ||
		receipt.ThroughSeq < head.ThroughSeq || !validSHA256Hex(receipt.ItemDigest) {
		return fmt.Errorf(
			"server returned an invalid transcript append receipt for item %q",
			request.ItemKey,
		)
	}
	head.ThroughSeq = receipt.ThroughSeq
	if requestHasAgentText(request) {
		head.FinalItemKey = request.ItemKey
	}
	return nil
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// requestHasAgentText reports whether this item is a candidate for the run's
// final answer: agent-authored, with words in it.
func requestHasAgentText(request protocol.RunTranscriptAppendRequest) bool {
	if request.Role != protocol.RunItemRoleAgent {
		return false
	}
	for _, part := range request.Parts {
		if part.Type == protocol.RunItemPartTypeText && part.Text != "" {
			return true
		}
	}
	return false
}

func (d *Daemon) publishRunItemDelta(run Run, delta agent.ItemDelta) {
	var channel string
	switch delta.Channel {
	case agent.DeltaChannelText:
		channel = localPreviewChannelText
	case agent.DeltaChannelReasoning:
		channel = localPreviewChannelReasoning
	default:
		return
	}
	d.publishLocalRunPreview(localRunPreviewFrame{
		CompanyID:      run.CompanyID,
		RunID:          run.ID.String(),
		ConversationID: run.ConversationID,
		ItemKey:        delta.ItemKey,
		Channel:        channel,
		Operation:      string(delta.Operation),
		Content:        redact.Redact(delta.Content),
	})
}

func (d *Daemon) pinRunSessionWhenReady(
	ctx context.Context,
	runID uuid.UUID,
	codexHome string,
	sessionID string,
	runLog *slog.Logger,
) {
	// ctx is the stream context and dies the moment the stream returns —
	// exactly when Codex may still be flushing the rollout to disk, the case
	// waitCodexRolloutPresent exists for. Detach from that cancellation,
	// bounded: the wait may outlive the stream by the grace window, never the
	// process by more.
	const rolloutFlushGrace = 10 * time.Second
	waitCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		rolloutFlushGrace,
	)
	go func() {
		defer cancel()
		if !waitCodexRolloutPresent(waitCtx, codexHome, sessionID) {
			runLog.Debug(
				"skip pinning provider session: resume state not present before run ended",
				"session_id", sessionID,
			)
			return
		}
		pinCtx, pinCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pinCancel()
		if err := d.client.PinRunSession(pinCtx, runID, sessionID); err != nil {
			runLog.Debug("pin session failed", "error", err)
		}
	}()
}

func resultForRunStreamStop(err error, idle bool, idleThreshold time.Duration) agent.Result {
	if idle {
		return agent.Result{
			Status: agent.ResultIdleWatchdog, Error: idleWatchdogReason(idleThreshold),
		}
	}
	if errors.Is(err, context.Canceled) {
		return agent.Result{
			Status: agent.ResultCancelled,
			Error:  "run cancelled by upstream context (server cancel or daemon shutdown)",
		}
	}
	return agent.Result{
		Status: agent.ResultTimeout, Error: "agent did not finish within the configured timeout",
	}
}

func idleWatchdogReason(window time.Duration) string {
	return fmt.Sprintf(
		"agent produced no new events for %s; force-stopped by idle watchdog",
		window,
	)
}

// runIdleWatchdog cancels an execution after provider silence. An active tool
// receives the larger tool budget, while a synchronous transcript append
// suppresses the timeout because the consumer, not the provider, is waiting.
func (d *Daemon) runIdleWatchdog(
	agentCtx context.Context,
	window, toolWindow time.Duration,
	lastActivityAt *atomic.Int64,
	inFlightTools *atomic.Int32,
	fired *atomic.Bool,
	firedThreshold *atomic.Int64,
	appendInFlight *atomic.Bool,
	cancel context.CancelFunc,
	runLog *slog.Logger,
	runID uuid.UUID,
) {
	interval := window / 2
	if window >= time.Minute && interval < 30*time.Second {
		interval = 30 * time.Second
	}
	if interval <= 0 {
		interval = window
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-agentCtx.Done():
			return
		case <-ticker.C:
			threshold := window
			toolInFlight := inFlightTools.Load() > 0
			if toolInFlight {
				if toolWindow <= 0 {
					continue
				}
				threshold = toolWindow
			}
			// A successful append publishes its fresh activity timestamp before
			// clearing appendInFlight. Read the flag first so observing false also
			// observes that timestamp; the opposite order can combine the stale
			// pre-append timestamp with the post-append false flag and cancel a
			// healthy Run immediately after its durable acknowledgement.
			if appendInFlight.Load() {
				continue
			}
			idleFor := time.Since(time.Unix(0, lastActivityAt.Load()))
			if idleFor < threshold {
				continue
			}
			runLog.Warn(
				"idle watchdog firing: no provider activity, force-stopping run",
				"run", runID,
				"idle_for", idleFor.Round(time.Second).String(),
				"threshold", threshold.String(),
				"tool_in_flight", toolInFlight,
			)
			firedThreshold.Store(int64(threshold))
			fired.Store(true)
			cancel()
			return
		}
	}
}
