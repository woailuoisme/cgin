package idempotency

// response 是幂等回放所需的完整 HTTP 响应快照。
//
// 用 JSON 序列化：结构简单，不值得引入代码生成；开销由响应体主导，编码成本可忽略。
type response struct {
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
	StatusCode int                 `json:"statusCode"`
}
