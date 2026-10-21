package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

const (
	modelDiscoveryRedisPrefix       = "zenith-team:{runtime-model-discovery}:request:"
	modelDiscoveryRedisActivePrefix = "zenith-team:{runtime-model-discovery}:active:"
	modelDiscoveryRedisRetentionMS  = int64(modelDiscoveryRetention / time.Millisecond)
	modelDiscoveryPendingTimeoutMS  = int64(modelDiscoveryPendingTimeout / time.Millisecond)
	modelDiscoveryRunningTimeoutMS  = int64(modelDiscoveryRunningTimeout / time.Millisecond)
	modelDiscoveryPendingLifetimeMS = int64(modelDiscoveryPendingLifetime / time.Millisecond)
	modelDiscoveryRunningLifetimeMS = int64(modelDiscoveryRunningLifetime / time.Millisecond)
)

var enqueueModelDiscoveryScript = redis.NewScript(`
local activeID = redis.call('GET', KEYS[1])
if activeID then
  local requestKey = ARGV[1] .. activeID
  local status = redis.call('HGET', requestKey, 'status')
  if status == 'pending' then
    local created = tonumber(redis.call('HGET', requestKey, 'created_ms') or '0')
    if tonumber(ARGV[2]) - created <= tonumber(ARGV[3]) then
      return {0, activeID}
    end
  elseif status == 'running' then
    local started = tonumber(redis.call('HGET', requestKey, 'run_started_ms') or '0')
    if started > 0 and tonumber(ARGV[2]) - started <= tonumber(ARGV[4]) then
      return {0, activeID}
    end
  end
  redis.call('DEL', KEYS[1])
end
redis.call('HSET', KEYS[2],
  'id', ARGV[5],
  'runtime_id', ARGV[6],
  'company_id', ARGV[7],
  'provider', ARGV[8],
  'status', 'pending',
  'models', '[]',
  'error', '',
  'created_at', ARGV[9],
  'created_ms', ARGV[2],
  'updated_at', ARGV[9],
  'run_started_ms', '',
  'expires_ms', ARGV[10])
redis.call('PEXPIRE', KEYS[2], ARGV[11])
redis.call('SET', KEYS[1], ARGV[5], 'PX', ARGV[11])
return {1, ARGV[5]}
`)

var claimModelDiscoveryScript = redis.NewScript(`
local requestID = redis.call('GET', KEYS[1])
if not requestID then return '' end
local requestKey = ARGV[1] .. requestID
local status = redis.call('HGET', requestKey, 'status')
if not status then
  redis.call('DEL', KEYS[1])
  return ''
end
local nowMS = tonumber(ARGV[2])
if status == 'pending' then
  local created = tonumber(redis.call('HGET', requestKey, 'created_ms') or '0')
  if nowMS - created > tonumber(ARGV[3]) then
    redis.call('HSET', requestKey, 'status', 'timeout', 'models', '[]',
      'error', ARGV[6], 'updated_at', ARGV[5])
    redis.call('DEL', KEYS[1])
    return ''
  end
  redis.call('HSET', requestKey, 'status', 'running', 'updated_at', ARGV[5],
    'run_started_ms', ARGV[2], 'expires_ms', ARGV[8])
  redis.call('PEXPIRE', requestKey, ARGV[9])
  redis.call('PEXPIRE', KEYS[1], ARGV[9])
  return requestID
end
if status == 'running' then
  local started = tonumber(redis.call('HGET', requestKey, 'run_started_ms') or '0')
  if started > 0 and nowMS - started > tonumber(ARGV[4]) then
    redis.call('HSET', requestKey, 'status', 'timeout', 'models', '[]',
      'error', ARGV[7], 'updated_at', ARGV[5])
    redis.call('DEL', KEYS[1])
  end
  return ''
end
redis.call('DEL', KEYS[1])
return ''
`)

var getModelDiscoveryScript = redis.NewScript(`
local status = redis.call('HGET', KEYS[1], 'status')
if not status then return 0 end
local nowMS = tonumber(ARGV[1])
local timedOut = false
local timeoutError = ''
if status == 'pending' then
  local created = tonumber(redis.call('HGET', KEYS[1], 'created_ms') or '0')
  if nowMS - created > tonumber(ARGV[2]) then
    timedOut = true
    timeoutError = ARGV[5]
  end
elseif status == 'running' then
  local started = tonumber(redis.call('HGET', KEYS[1], 'run_started_ms') or '0')
  if started > 0 and nowMS - started > tonumber(ARGV[3]) then
    timedOut = true
    timeoutError = ARGV[6]
  end
end
if timedOut then
  redis.call('HSET', KEYS[1], 'status', 'timeout', 'models', '[]',
    'error', timeoutError, 'updated_at', ARGV[4])
  local runtimeID = redis.call('HGET', KEYS[1], 'runtime_id')
  local requestID = redis.call('HGET', KEYS[1], 'id')
  local activeKey = ARGV[7] .. runtimeID
  if redis.call('GET', activeKey) == requestID then redis.call('DEL', activeKey) end
end
return 1
`)

var finishModelDiscoveryScript = redis.NewScript(`
local status = redis.call('HGET', KEYS[1], 'status')
if not status then return -1 end
if redis.call('HGET', KEYS[1], 'runtime_id') ~= ARGV[1] then return -1 end
local nowMS = tonumber(ARGV[2])
if status == 'pending' then
  local created = tonumber(redis.call('HGET', KEYS[1], 'created_ms') or '0')
  if nowMS - created > tonumber(ARGV[3]) then
    redis.call('HSET', KEYS[1], 'status', 'timeout', 'models', '[]',
      'error', ARGV[7], 'updated_at', ARGV[6])
    if redis.call('GET', KEYS[2]) == ARGV[10] then redis.call('DEL', KEYS[2]) end
    return 1
  end
  return -2
end
if status == 'running' then
  local started = tonumber(redis.call('HGET', KEYS[1], 'run_started_ms') or '0')
  if started > 0 and nowMS - started > tonumber(ARGV[4]) then
    redis.call('HSET', KEYS[1], 'status', 'timeout', 'models', '[]',
      'error', ARGV[11], 'updated_at', ARGV[6])
    if redis.call('GET', KEYS[2]) == ARGV[10] then redis.call('DEL', KEYS[2]) end
    return 1
  end
  redis.call('HSET', KEYS[1], 'status', ARGV[5], 'models', ARGV[12],
    'error', ARGV[13], 'updated_at', ARGV[6], 'expires_ms', ARGV[8])
  redis.call('PEXPIRE', KEYS[1], ARGV[9])
  if redis.call('GET', KEYS[2]) == ARGV[10] then redis.call('DEL', KEYS[2]) end
  return 1
end
return 1
`)

type RedisModelDiscoveryStore struct {
	client *redis.Client
	now    func() time.Time
	newID  func() uuid.UUID
}

var _ ModelDiscoveryStore = (*RedisModelDiscoveryStore)(nil)

func NewRedisModelDiscoveryStore(
	client *redis.Client,
	options ModelDiscoveryStoreOptions,
) (*RedisModelDiscoveryStore, error) {
	if client == nil {
		return nil, errors.New("runtime model discovery Redis client is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewID == nil {
		options.NewID = uuid.New
	}
	return &RedisModelDiscoveryStore{client: client, now: options.Now, newID: options.NewID}, nil
}

func (s *RedisModelDiscoveryStore) Enqueue(
	ctx context.Context,
	seed ModelDiscoverySeed,
) (ModelDiscoveryRequest, bool, error) {
	now := s.now().UTC()
	requestID := s.newID()
	result, err := enqueueModelDiscoveryScript.Run(
		ctx,
		s.client,
		[]string{redisModelDiscoveryActiveKey(seed.RuntimeID), redisModelDiscoveryRequestKey(requestID)},
		modelDiscoveryRedisPrefix,
		now.UnixMilli(),
		modelDiscoveryPendingTimeoutMS,
		modelDiscoveryRunningTimeoutMS,
		requestID.String(),
		seed.RuntimeID.String(),
		seed.CompanyID.String(),
		seed.Provider,
		now.Format(time.RFC3339Nano),
		now.Add(modelDiscoveryPendingLifetime).UnixMilli(),
		modelDiscoveryPendingLifetimeMS,
	).Slice()
	if err != nil {
		return ModelDiscoveryRequest{}, false, fmt.Errorf("enqueue runtime model discovery in Redis: %w", err)
	}
	created, err := redisInteger(result[0])
	if err != nil {
		return ModelDiscoveryRequest{}, false, err
	}
	resolvedID, err := redisUUID(result[1])
	if err != nil {
		return ModelDiscoveryRequest{}, false, err
	}
	request, err := s.Get(ctx, resolvedID)
	return request, created == 1, err
}

func (s *RedisModelDiscoveryStore) Get(
	ctx context.Context,
	requestID uuid.UUID,
) (ModelDiscoveryRequest, error) {
	now := s.now().UTC()
	pendingError, runningError, err := redisTimeoutErrors()
	if err != nil {
		return ModelDiscoveryRequest{}, err
	}
	found, err := getModelDiscoveryScript.Run(
		ctx,
		s.client,
		[]string{redisModelDiscoveryRequestKey(requestID)},
		now.UnixMilli(),
		modelDiscoveryPendingTimeoutMS,
		modelDiscoveryRunningTimeoutMS,
		now.Format(time.RFC3339Nano),
		pendingError,
		runningError,
		modelDiscoveryRedisActivePrefix,
	).Int64()
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("read runtime model discovery from Redis: %w", err)
	}
	if found == 0 {
		return ModelDiscoveryRequest{}, ErrModelDiscoveryNotFound
	}
	values, err := s.client.HGetAll(ctx, redisModelDiscoveryRequestKey(requestID)).Result()
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("hydrate runtime model discovery from Redis: %w", err)
	}
	return modelDiscoveryRequestFromRedis(values)
}

func (s *RedisModelDiscoveryStore) Claim(
	ctx context.Context,
	runtimeID uuid.UUID,
) (*ModelDiscoveryRequest, error) {
	now := s.now().UTC()
	pendingError, runningError, err := redisTimeoutErrors()
	if err != nil {
		return nil, err
	}
	requestID, err := claimModelDiscoveryScript.Run(
		ctx,
		s.client,
		[]string{redisModelDiscoveryActiveKey(runtimeID)},
		modelDiscoveryRedisPrefix,
		now.UnixMilli(),
		modelDiscoveryPendingTimeoutMS,
		modelDiscoveryRunningTimeoutMS,
		now.Format(time.RFC3339Nano),
		pendingError,
		runningError,
		now.Add(modelDiscoveryRunningLifetime).UnixMilli(),
		modelDiscoveryRunningLifetimeMS,
	).Text()
	if errors.Is(err, redis.Nil) || requestID == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim runtime model discovery in Redis: %w", err)
	}
	parsedID, err := uuid.Parse(requestID)
	if err != nil {
		return nil, fmt.Errorf("hydrate runtime model discovery id from Redis: %w", err)
	}
	request, err := s.Get(ctx, parsedID)
	if err != nil {
		return nil, err
	}
	return &request, nil
}

func (s *RedisModelDiscoveryStore) Finish(
	ctx context.Context,
	runtimeID uuid.UUID,
	requestID uuid.UUID,
	result ModelDiscoveryResult,
) (ModelDiscoveryRequest, error) {
	now := s.now().UTC()
	models, err := json.Marshal(result.Models)
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("encode runtime model discovery models: %w", err)
	}
	errorJSON := ""
	if result.Error != nil {
		rawError, marshalErr := json.Marshal(result.Error)
		if marshalErr != nil {
			return ModelDiscoveryRequest{}, fmt.Errorf(
				"encode runtime model discovery error: %w",
				marshalErr,
			)
		}
		errorJSON = string(rawError)
	}
	pendingError, runningError, err := redisTimeoutErrors()
	if err != nil {
		return ModelDiscoveryRequest{}, err
	}
	code, err := finishModelDiscoveryScript.Run(
		ctx,
		s.client,
		[]string{
			redisModelDiscoveryRequestKey(requestID), redisModelDiscoveryActiveKey(runtimeID),
		},
		runtimeID.String(),
		now.UnixMilli(),
		modelDiscoveryPendingTimeoutMS,
		modelDiscoveryRunningTimeoutMS,
		string(result.Status),
		now.Format(time.RFC3339Nano),
		pendingError,
		now.Add(modelDiscoveryRetention).UnixMilli(),
		modelDiscoveryRedisRetentionMS,
		requestID.String(),
		runningError,
		string(models),
		errorJSON,
	).Int64()
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("finish runtime model discovery in Redis: %w", err)
	}
	switch code {
	case -1:
		return ModelDiscoveryRequest{}, ErrModelDiscoveryNotFound
	case -2:
		return ModelDiscoveryRequest{}, ErrModelDiscoveryNotRunning
	}
	return s.Get(ctx, requestID)
}

func redisModelDiscoveryRequestKey(requestID uuid.UUID) string {
	return modelDiscoveryRedisPrefix + requestID.String()
}

func redisModelDiscoveryActiveKey(runtimeID uuid.UUID) string {
	return modelDiscoveryRedisActivePrefix + runtimeID.String()
}

func redisTimeoutErrors() (string, string, error) {
	pending, err := json.Marshal(ModelDiscoveryError{
		Code: "pending_timeout", Message: "daemon did not claim model discovery within 30 seconds",
	})
	if err != nil {
		return "", "", fmt.Errorf("encode pending model discovery timeout: %w", err)
	}
	running, err := json.Marshal(ModelDiscoveryError{
		Code: "running_timeout", Message: "daemon did not finish model discovery within 60 seconds",
	})
	if err != nil {
		return "", "", fmt.Errorf("encode running model discovery timeout: %w", err)
	}
	return string(pending), string(running), nil
}

func modelDiscoveryRequestFromRedis(values map[string]string) (ModelDiscoveryRequest, error) {
	if len(values) == 0 {
		return ModelDiscoveryRequest{}, ErrModelDiscoveryNotFound
	}
	requestID, err := uuid.Parse(values["id"])
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("decode runtime model discovery id: %w", err)
	}
	runtimeID, err := uuid.Parse(values["runtime_id"])
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("decode runtime model discovery runtime id: %w", err)
	}
	companyID, err := uuid.Parse(values["company_id"])
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("decode runtime model discovery company id: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, values["created_at"])
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("decode runtime model discovery created time: %w", err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, values["updated_at"])
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("decode runtime model discovery updated time: %w", err)
	}
	expiresMS, err := strconv.ParseInt(values["expires_ms"], 10, 64)
	if err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("decode runtime model discovery expiry: %w", err)
	}
	var models []provideragent.RuntimeModel
	if err := json.Unmarshal([]byte(values["models"]), &models); err != nil {
		return ModelDiscoveryRequest{}, fmt.Errorf("decode runtime model discovery models: %w", err)
	}
	if models == nil {
		models = []provideragent.RuntimeModel{}
	}
	var discoveryError *ModelDiscoveryError
	if rawError := values["error"]; rawError != "" {
		discoveryError = &ModelDiscoveryError{}
		if err := json.Unmarshal([]byte(rawError), discoveryError); err != nil {
			return ModelDiscoveryRequest{}, fmt.Errorf("decode runtime model discovery error: %w", err)
		}
	}
	request := ModelDiscoveryRequest{
		ID: requestID, RuntimeID: runtimeID, CompanyID: companyID,
		Provider: values["provider"], Status: ModelDiscoveryStatus(values["status"]),
		Models: models, Error: discoveryError, CreatedAt: createdAt, UpdatedAt: updatedAt,
		ExpiresAt: time.UnixMilli(expiresMS).UTC(),
	}
	if rawStarted := values["run_started_ms"]; rawStarted != "" {
		startedMS, parseErr := strconv.ParseInt(rawStarted, 10, 64)
		if parseErr != nil {
			return ModelDiscoveryRequest{}, fmt.Errorf("decode runtime model discovery start: %w", parseErr)
		}
		started := time.UnixMilli(startedMS).UTC()
		request.RunStarted = &started
	}
	return request, nil
}

func redisInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("decode runtime model discovery Redis integer: %w", err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("decode runtime model discovery Redis integer: %T", value)
	}
}

func redisUUID(value any) (uuid.UUID, error) {
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case []byte:
		raw = string(typed)
	default:
		return uuid.Nil, fmt.Errorf("decode runtime model discovery Redis id: %T", value)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("decode runtime model discovery Redis id: %w", err)
	}
	return id, nil
}
