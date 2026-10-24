package idempotency

import "encoding/json"

// response 是幂等缓存回放所需的完整 HTTP 响应快照。
// 用 JSON 序列化，避免为单一结构引入代码生成依赖；缓存体积主要由响应体决定，
// 编码开销可忽略。
type response struct {
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
	StatusCode int                 `json:"statusCode"`
}

// marshal 序列化响应快照。
func (r *response) marshal() ([]byte, error) {
	return json.Marshal(r)
}

// unmarshal 反序列化响应快照。
func (r *response) unmarshal(data []byte) error {
	return json.Unmarshal(data, r)
}
