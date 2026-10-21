package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const (
	modelDiscoveryPendingTimeout  = 30 * time.Second
	modelDiscoveryRunningTimeout  = 60 * time.Second
	modelDiscoveryRetention       = 2 * time.Minute
	modelDiscoveryPendingLifetime = modelDiscoveryPendingTimeout + modelDiscoveryRetention
	modelDiscoveryRunningLifetime = modelDiscoveryRunningTimeout + modelDiscoveryRetention
)

const ModelDiscoveryPendingWork = "model_discovery"

var (
	ErrRuntimeNotFound          = errors.New("runtime not found")
	ErrRuntimeAccessDenied      = errors.New("runtime access denied")
	ErrRuntimeOffline           = errors.New("runtime is offline")
	ErrRuntimeInvalidInput      = errors.New("runtime input is invalid")
	ErrModelDiscoveryNotFound   = errors.New("model discovery not found")
	ErrModelDiscoveryNotRunning = errors.New("model discovery is not running")
	ErrModelDiscoveryInvalid    = errors.New("model discovery result is invalid")
)

type ModelDiscoveryStatus string

const (
	ModelDiscoveryPending   ModelDiscoveryStatus = "pending"
	ModelDiscoveryRunning   ModelDiscoveryStatus = "running"
	ModelDiscoveryCompleted ModelDiscoveryStatus = "completed"
	ModelDiscoveryFailed    ModelDiscoveryStatus = "failed"
	ModelDiscoveryTimeout   ModelDiscoveryStatus = "timeout"
)

type ModelDiscoveryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ModelDiscoveryRequest struct {
	ID        uuid.UUID                    `json:"requestId"`
	RuntimeID uuid.UUID                    `json:"runtimeId"`
	Status    ModelDiscoveryStatus         `json:"status"`
	Models    []provideragent.RuntimeModel `json:"models"`
	Error     *ModelDiscoveryError         `json:"error,omitempty"`
	CreatedAt time.Time                    `json:"createdAt"`
	UpdatedAt time.Time                    `json:"updatedAt"`

	CompanyID  uuid.UUID  `json:"-"`
	Provider   string     `json:"-"`
	RunStarted *time.Time `json:"-"`
	ExpiresAt  time.Time  `json:"-"`
}

type ModelDiscoverySeed struct {
	RuntimeID uuid.UUID
	CompanyID uuid.UUID
	Provider  string
}

type ModelDiscoveryResult struct {
	Status ModelDiscoveryStatus
	Models []provideragent.RuntimeModel
	Error  *ModelDiscoveryError
}

type ModelDiscoveryStore interface {
	Enqueue(context.Context, ModelDiscoverySeed) (ModelDiscoveryRequest, bool, error)
	Get(context.Context, uuid.UUID) (ModelDiscoveryRequest, error)
	Claim(context.Context, uuid.UUID) (*ModelDiscoveryRequest, error)
	Finish(context.Context, uuid.UUID, uuid.UUID, ModelDiscoveryResult) (ModelDiscoveryRequest, error)
}

type ModelDiscoveryStoreOptions struct {
	Now   func() time.Time
	NewID func() uuid.UUID
}

func (s *DefaultRuntimeService) InitiateModelDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
) (ModelDiscoveryRequest, error) {
	runtime, err := s.runtimeForCompany(ctx, principal, runtimeID)
	if err != nil {
		return ModelDiscoveryRequest{}, err
	}
	if runtime.Status != "online" {
		return ModelDiscoveryRequest{}, ErrRuntimeOffline
	}
	request, _, err := s.modelDiscoveries.Enqueue(ctx, ModelDiscoverySeed{
		RuntimeID: runtime.ID, CompanyID: runtime.CompanyID, Provider: runtime.Provider,
	})
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("enqueue runtime model discovery: %w", err)
	}
	if s.bus != nil {
		s.bus.Publish(context.Background(), TopicRuntime, events.Event{
			EntityType: TopicRuntime,
			Name:       EventRuntimePendingWork,
			CompanyID:  runtime.CompanyID,
			CreatedAt:  time.Now().UTC(),
			Actor:      events.SystemActor(),
			Payload:    PendingWork{RuntimeID: runtime.ID.String(), Kind: ModelDiscoveryPendingWork},
		})
	}
	return request, nil
}

func (s *DefaultRuntimeService) GetModelDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	requestID uuid.UUID,
) (ModelDiscoveryRequest, error) {
	// A cached catalog is machine state: only the registrant reads it.
	if _, err := s.runtimeForCompany(ctx, principal, runtimeID); err != nil {
		return ModelDiscoveryRequest{}, err
	}
	request, err := s.modelDiscoveries.Get(ctx, requestID)
	if err != nil {
		return ModelDiscoveryRequest{}, err
	}
	if request.CompanyID != principal.CompanyID || request.RuntimeID != runtimeID {
		return ModelDiscoveryRequest{}, ErrModelDiscoveryNotFound
	}
	return request, nil
}

func (s *DefaultRuntimeService) ClaimModelDiscovery(
	ctx context.Context,
	runtimeID uuid.UUID,
) (*ModelDiscoveryRequest, error) {
	request, err := s.modelDiscoveries.Claim(ctx, runtimeID)
	if err != nil {
		return nil, fmt.Errorf("claim runtime model discovery: %w", err)
	}
	return request, nil
}

func (s *DefaultRuntimeService) FinishModelDiscovery(
	ctx context.Context,
	runtimeID uuid.UUID,
	requestID uuid.UUID,
	result ModelDiscoveryResult,
) (ModelDiscoveryRequest, error) {
	normalized, err := normalizeModelDiscoveryResult(result)
	if err != nil {
		return ModelDiscoveryRequest{}, err
	}
	request, err := s.modelDiscoveries.Finish(ctx, runtimeID, requestID, normalized)
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("finish runtime model discovery: %w", err)
	}
	return request, nil
}

func normalizeModelDiscoveryResult(result ModelDiscoveryResult) (ModelDiscoveryResult, error) {
	switch result.Status {
	case ModelDiscoveryCompleted:
		if result.Error != nil {
			return ModelDiscoveryResult{}, ErrModelDiscoveryInvalid
		}
		models, err := provideragent.NormalizeRuntimeModels(result.Models)
		if err != nil {
			return ModelDiscoveryResult{}, fmt.Errorf("%w: %w", ErrModelDiscoveryInvalid, err)
		}
		return ModelDiscoveryResult{Status: ModelDiscoveryCompleted, Models: models}, nil
	case ModelDiscoveryFailed:
		if len(result.Models) != 0 || result.Error == nil ||
			result.Error.Code != "discovery_failed" {
			return ModelDiscoveryResult{}, ErrModelDiscoveryInvalid
		}
		message := strings.TrimSpace(result.Error.Message)
		if message == "" || len(message) > 512 {
			return ModelDiscoveryResult{}, ErrModelDiscoveryInvalid
		}
		return ModelDiscoveryResult{
			Status: ModelDiscoveryFailed,
			Models: []provideragent.RuntimeModel{},
			Error:  &ModelDiscoveryError{Code: "discovery_failed", Message: message},
		}, nil
	default:
		return ModelDiscoveryResult{}, ErrModelDiscoveryInvalid
	}
}

// runtimeForCompany reads one runtime as the principal may see it: the
// company fence and the registrant filter are the query's (ADR 0014), so a
// foreign id, an unknown id and another member's machine are the same
// ErrRuntimeNotFound — and a row that arrives is the caller's own, which is
// all the machine RPCs need to know.
func (s *DefaultRuntimeService) runtimeForCompany(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
) (Runtime, error) {
	row, err := s.store.Queries().GetRuntimeForCompany(ctx, generated.GetRuntimeForCompanyParams{
		CompanyID:    principal.CompanyID,
		ID:           runtimeID,
		ViewerUserID: principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Runtime{}, ErrRuntimeNotFound
	}
	if err != nil {
		return Runtime{}, fmt.Errorf("load runtime: %w", err)
	}
	return runtimeFromDetailRow(row), nil
}

type InMemoryModelDiscoveryStore struct {
	mu              sync.Mutex
	requests        map[uuid.UUID]*ModelDiscoveryRequest
	activeByRuntime map[uuid.UUID]uuid.UUID
	now             func() time.Time
	newID           func() uuid.UUID
}

var _ ModelDiscoveryStore = (*InMemoryModelDiscoveryStore)(nil)

func NewInMemoryModelDiscoveryStore(options ModelDiscoveryStoreOptions) *InMemoryModelDiscoveryStore {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewID == nil {
		options.NewID = uuid.New
	}
	return &InMemoryModelDiscoveryStore{
		requests:        make(map[uuid.UUID]*ModelDiscoveryRequest),
		activeByRuntime: make(map[uuid.UUID]uuid.UUID),
		now:             options.Now,
		newID:           options.NewID,
	}
}

func (s *InMemoryModelDiscoveryStore) Enqueue(
	_ context.Context,
	seed ModelDiscoverySeed,
) (ModelDiscoveryRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	s.cleanup(now)
	if activeID, ok := s.activeByRuntime[seed.RuntimeID]; ok {
		if active, exists := s.requests[activeID]; exists {
			s.applyTimeout(active, now)
			if modelDiscoveryActive(active.Status) {
				return cloneModelDiscoveryRequest(*active), false, nil
			}
		}
	}

	request := &ModelDiscoveryRequest{
		ID: s.newID(), RuntimeID: seed.RuntimeID, CompanyID: seed.CompanyID,
		Provider: seed.Provider, Status: ModelDiscoveryPending,
		Models: []provideragent.RuntimeModel{}, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(modelDiscoveryPendingLifetime),
	}
	s.requests[request.ID] = request
	s.activeByRuntime[seed.RuntimeID] = request.ID
	return cloneModelDiscoveryRequest(*request), true, nil
}

func (s *InMemoryModelDiscoveryStore) Get(
	_ context.Context,
	requestID uuid.UUID,
) (ModelDiscoveryRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	s.cleanup(now)
	request, ok := s.requests[requestID]
	if !ok {
		return ModelDiscoveryRequest{}, ErrModelDiscoveryNotFound
	}
	s.applyTimeout(request, now)
	return cloneModelDiscoveryRequest(*request), nil
}

func (s *InMemoryModelDiscoveryStore) Claim(
	_ context.Context,
	runtimeID uuid.UUID,
) (*ModelDiscoveryRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	s.cleanup(now)
	requestID, ok := s.activeByRuntime[runtimeID]
	if !ok {
		return nil, nil
	}
	request, ok := s.requests[requestID]
	if !ok {
		delete(s.activeByRuntime, runtimeID)
		return nil, nil
	}
	s.applyTimeout(request, now)
	if request.Status != ModelDiscoveryPending {
		return nil, nil
	}
	request.Status = ModelDiscoveryRunning
	request.UpdatedAt = now
	request.RunStarted = &now
	request.ExpiresAt = now.Add(modelDiscoveryRunningLifetime)
	cloned := cloneModelDiscoveryRequest(*request)
	return &cloned, nil
}

func (s *InMemoryModelDiscoveryStore) Finish(
	_ context.Context,
	runtimeID uuid.UUID,
	requestID uuid.UUID,
	result ModelDiscoveryResult,
) (ModelDiscoveryRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	s.cleanup(now)
	request, ok := s.requests[requestID]
	if !ok || request.RuntimeID != runtimeID {
		return ModelDiscoveryRequest{}, ErrModelDiscoveryNotFound
	}
	s.applyTimeout(request, now)
	if modelDiscoveryTerminal(request.Status) {
		return cloneModelDiscoveryRequest(*request), nil
	}
	if request.Status != ModelDiscoveryRunning {
		return ModelDiscoveryRequest{}, ErrModelDiscoveryNotRunning
	}
	request.Status = result.Status
	request.Models = cloneRuntimeModels(result.Models)
	request.Error = cloneModelDiscoveryError(result.Error)
	request.UpdatedAt = now
	request.ExpiresAt = now.Add(modelDiscoveryRetention)
	delete(s.activeByRuntime, runtimeID)
	return cloneModelDiscoveryRequest(*request), nil
}

func (s *InMemoryModelDiscoveryStore) cleanup(now time.Time) {
	for id, request := range s.requests {
		if now.After(request.ExpiresAt) {
			delete(s.requests, id)
			if s.activeByRuntime[request.RuntimeID] == id {
				delete(s.activeByRuntime, request.RuntimeID)
			}
		}
	}
}

func (s *InMemoryModelDiscoveryStore) applyTimeout(request *ModelDiscoveryRequest, now time.Time) {
	var timeoutError *ModelDiscoveryError
	var expiresAt time.Time
	switch {
	case request.Status == ModelDiscoveryPending &&
		now.Sub(request.CreatedAt) > modelDiscoveryPendingTimeout:
		timeoutError = &ModelDiscoveryError{
			Code: "pending_timeout", Message: "daemon did not claim model discovery within 30 seconds",
		}
		expiresAt = request.CreatedAt.Add(modelDiscoveryPendingLifetime)
	case request.Status == ModelDiscoveryRunning && request.RunStarted != nil &&
		now.Sub(*request.RunStarted) > modelDiscoveryRunningTimeout:
		timeoutError = &ModelDiscoveryError{
			Code: "running_timeout", Message: "daemon did not finish model discovery within 60 seconds",
		}
		expiresAt = request.RunStarted.Add(modelDiscoveryRunningLifetime)
	}
	if timeoutError == nil {
		return
	}
	request.Status = ModelDiscoveryTimeout
	request.Error = timeoutError
	request.Models = []provideragent.RuntimeModel{}
	request.UpdatedAt = now
	request.ExpiresAt = expiresAt
	if s.activeByRuntime[request.RuntimeID] == request.ID {
		delete(s.activeByRuntime, request.RuntimeID)
	}
}

func modelDiscoveryActive(status ModelDiscoveryStatus) bool {
	return status == ModelDiscoveryPending || status == ModelDiscoveryRunning
}

func modelDiscoveryTerminal(status ModelDiscoveryStatus) bool {
	return status == ModelDiscoveryCompleted || status == ModelDiscoveryFailed ||
		status == ModelDiscoveryTimeout
}

func cloneModelDiscoveryRequest(request ModelDiscoveryRequest) ModelDiscoveryRequest {
	request.Models = cloneRuntimeModels(request.Models)
	request.Error = cloneModelDiscoveryError(request.Error)
	if request.RunStarted != nil {
		started := *request.RunStarted
		request.RunStarted = &started
	}
	return request
}

func cloneModelDiscoveryError(discoveryError *ModelDiscoveryError) *ModelDiscoveryError {
	if discoveryError == nil {
		return nil
	}
	cloned := *discoveryError
	return &cloned
}

func cloneRuntimeModels(models []provideragent.RuntimeModel) []provideragent.RuntimeModel {
	if models == nil {
		return nil
	}
	cloned := make([]provideragent.RuntimeModel, len(models))
	for index, model := range models {
		cloned[index] = model
		if model.Reasoning != nil {
			reasoning := *model.Reasoning
			reasoning.Levels = append([]provideragent.RuntimeReasoningLevel(nil), model.Reasoning.Levels...)
			cloned[index].Reasoning = &reasoning
		}
	}
	return cloned
}
