package middleware

import (
	"github.com/gin-gonic/gin"

	"bgin/internal/middleware/global"
)

// IdempotencyOption 定义幂等中间件函数式配置项。
type IdempotencyOption = global.IdempotencyOption

// Idempotency 构造路由/路由组级请求幂等中间件。
// 推荐挂载在特定变更路由（POST/PATCH）或 AuthGuard 之后，以直接享受已认证身份（user:<id>）的严格隔离。
func (m *Middleware) Idempotency(opts ...IdempotencyOption) gin.HandlerFunc {
	return global.NewIdempotency(m.cache, opts...)
}
