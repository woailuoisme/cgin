package queue

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"
)

const (
	// MaxPayloadWarnBytes 触发警示日志的载荷大小阈值（512KB）。
	MaxPayloadWarnBytes = 512 * 1024

	// MaxPayloadLimitBytes 拒绝入队的绝对安全上限（2MB），防止大载荷挤爆 Redis 内存。
	MaxPayloadLimitBytes = 2 * 1024 * 1024
)

// ErrPayloadTooLarge 当任务载荷超过安全阈值时返回。
var ErrPayloadTooLarge = errors.New("queue: task payload exceeds 2MB limit")

// TaskEnvelope 封装任务载荷与全链路追踪元数据信封。
type TaskEnvelope struct {
	TaskName     string            `json:"taskName"`
	Payload      json.RawMessage   `json:"payload"`
	Headers      map[string]string `json:"headers,omitempty"`
	DispatchedAt time.Time         `json:"dispatchedAt"`
}

// wrapEnvelope 将任务结构体序列化并封装进 TaskEnvelope 信封中。
func wrapEnvelope(ctx context.Context, job Job, customHeaders map[string]string) ([]byte, error) {
	rawPayload, err := json.Marshal(job)
	if err != nil {
		return nil, fmt.Errorf("queue: failed to serialize job payload: %w", err)
	}

	if len(rawPayload) > MaxPayloadLimitBytes {
		return nil, fmt.Errorf("%w: size is %d bytes", ErrPayloadTooLarge, len(rawPayload))
	}

	headers := make(map[string]string, len(customHeaders)+1)
	maps.Copy(headers, customHeaders)

	// 捕获或透传 RequestID
	if _, exists := headers["request_id"]; !exists {
		if reqID, ok := extractRequestID(ctx); ok {
			headers["request_id"] = reqID
		}
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

// unwrapEnvelope 从原始字节中解包，兼容支持 TaskEnvelope 信封与历史遗留裸 JSON 载荷。
func unwrapEnvelope(raw []byte, job Job) (map[string]string, error) {
	if len(raw) == 0 {
		return make(map[string]string), nil
	}

	// 尝试作为 TaskEnvelope 解码
	var env TaskEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.TaskName != "" && len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, job); err != nil {
			return env.Headers, fmt.Errorf("queue: failed to unmarshal envelope payload: %w", err)
		}
		return env.Headers, nil
	}

	// 兼容兜底：若不是标准信封，直接反序列化为历史遗留裸 JSON
	if err := json.Unmarshal(raw, job); err != nil {
		return nil, fmt.Errorf("queue: failed to unmarshal raw payload: %w", err)
	}
	return make(map[string]string), nil
}

func extractRequestID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v1, _ := ctx.Value("request_id").(string)
	v2, _ := ctx.Value("X-Request-ID").(string)
	if id := cmp.Or(v1, v2); id != "" {
		return id, true
	}
	return "", false
}
