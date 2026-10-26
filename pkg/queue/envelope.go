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
// 严格对待结构错误：把坏载荷当作业务字段缺失会让排查失去线索。
var ErrMalformedEnvelope = errors.New("queue: malformed task envelope")

// TaskEnvelope 封装任务载荷与全链路追踪元数据信封。
type TaskEnvelope struct {
	TaskName     string            `json:"taskName"`
	Payload      json.RawMessage   `json:"payload"`
	Headers      map[string]string `json:"headers,omitempty"`
	DispatchedAt time.Time         `json:"dispatchedAt"`
}

// wrapEnvelope 将任务结构体序列化并封装进 TaskEnvelope 信封中。
// headers 只携带 request_id：它是唯一的跨进程透传上下文，其余元数据由 Job 自身表达。
func wrapEnvelope(ctx context.Context, job Job) ([]byte, error) {
	rawPayload, err := json.Marshal(job)
	if err != nil {
		return nil, fmt.Errorf("queue: failed to serialize job payload: %w", err)
	}

	if len(rawPayload) > MaxPayloadLimitBytes {
		return nil, fmt.Errorf("%w: size is %d bytes", ErrPayloadTooLarge, len(rawPayload))
	}

	var headers map[string]string
	if reqID, ok := extractRequestID(ctx); ok {
		headers = map[string]string{"request_id": reqID}
	}

	env := TaskEnvelope{
		TaskName:     job.TaskName(),
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

// unwrapEnvelope 从原始字节中解包 TaskEnvelope 信封并把载荷填充进 job。
// 空载荷、非 JSON、缺任务名、缺载荷一律报 ErrMalformedEnvelope，不做裸 JSON 回退。
func unwrapEnvelope(raw []byte, job Job) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrMalformedEnvelope)
	}

	var env TaskEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedEnvelope, err)
	}
	if env.TaskName == "" {
		return nil, fmt.Errorf("%w: missing task name", ErrMalformedEnvelope)
	}
	if len(env.Payload) == 0 {
		return nil, fmt.Errorf("%w: missing payload for task %q", ErrMalformedEnvelope, env.TaskName)
	}

	if err := json.Unmarshal(env.Payload, job); err != nil {
		return nil, fmt.Errorf("queue: failed to unmarshal envelope payload: %w", err)
	}

	if env.Headers == nil {
		return make(map[string]string), nil
	}

	return env.Headers, nil
}

// extractRequestID 从上下文提取非空的请求追踪 ID。
func extractRequestID(ctx context.Context) (string, bool) {
	if id := logger.RequestIDFromContext(ctx); id != "" {
		return id, true
	}
	return "", false
}
