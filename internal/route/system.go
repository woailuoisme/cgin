package route

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/handler"
)

// RegisterSystemRoutes 注册根路径与系统健康探测端点，不挂载常规鉴权与限流中间件。
func RegisterSystemRoutes(engine *gin.Engine, h *handler.SystemHandler) {
	engine.GET("/", h.Index)
	engine.GET("/livez", h.Liveness)
	engine.GET("/readyz", h.Readiness)
}
