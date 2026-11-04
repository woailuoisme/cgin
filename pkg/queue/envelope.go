package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cgin/pkg/logger"
)

const (
	// MaxPayloadWarnBytes 触发警示日志的载荷大小阈值（512KB）。
	MaxPayloadWarnBytes = 512 * 1024

	// MaxPayloadLimitBytes 拒绝入队的绝对安全上限（2MB），防止大载荷挤爆 Redis 内存。
	MaxPayloadLimitBytes = 2 * 1024 * 1024
)

// ErrPayloadTooLarge 当任务载荷超过安全阈值时返回。
var ErrPayloadTooLarge = errors.New("queue: task payload exceeds 2MB limit")

// ErrMalformedEnvelope 当任务载荷不是合法信封时返回。
var ErrMalformedEnvelope = errors.New("queue: malformed task envelope")

// TaskEnvelope 封装任务载荷与全链路追踪元数据信封。
type TaskEnvelope struct {
	TaskName     string            `json:"taskName"`
	Payload      json.RawMessage   `json:"payload"`
	Headers      map[string]string `json:"headers,omitempty"`
	DispatchedAt time.Time         `json:"dispatchedAt"`
}

// packEnvelope 将任务结构体或任意载荷序列化并封装进 TaskEnvelope 信封中。
func packEnvelope(ctx context.Context, taskName string, payload any) ([]byte, error) {
	if taskName == "" {
		return nil, errors.New("queue: task name cannot be empty")
	}

	var rawPayload []byte
	var err error
	if payload == nil {
		rawPayload = []byte("{}")
	} else if b, ok := payload.([]byte); ok {
		rawPayload = b
	} else {
		rawPayload, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("queue: failed to serialize task payload: %w", err)
		}
	}

	if len(rawPayload) > MaxPayloadLimitBytes {
		return nil, fmt.Errorf("%w: size is %d bytes", ErrPayloadTooLarge, len(rawPayload))
	}

	var headers map[string]string
	if reqID, ok := extractRequestID(ctx); ok {
		headers = map[string]string{"request_id": reqID}
	}

	env := TaskEnvelope{
		TaskName:     taskName,
		Payload:      rawPayload,
		Headers:      headers,
		DispatchedAt: time.Now().UTC(),
	}

	envBytes, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("queue: failed to marshal task envelope: %w", err)
	}

	return envBytes, nil
}

// unpackEnvelope 从原始字节中解包 TaskEnvelope 信封。
func unpackEnvelope(raw []byte) (TaskEnvelope, error) {
	if len(raw) == 0 {
		return TaskEnvelope{}, fmt.Errorf("%w: empty payload", ErrMalformedEnvelope)
	}

	var env TaskEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return TaskEnvelope{}, fmt.Errorf("%w: %w", ErrMalformedEnvelope, err)
	}
	if env.TaskName == "" {
		return TaskEnvelope{}, fmt.Errorf("%w: missing task name", ErrMalformedEnvelope)
	}
	if len(env.Payload) == 0 {
		return TaskEnvelope{}, fmt.Errorf("%w: missing payload for task %q", ErrMalformedEnvelope, env.TaskName)
	}

	if env.Headers == nil {
		env.Headers = make(map[string]string)
	}

	return env, nil
}

// extractRequestID 从上下文提取非空的请求追踪 ID。
func extractRequestID(ctx context.Context) (string, bool) {
	id := logger.RequestIDFromContext(ctx)
	return id, id != ""
}
