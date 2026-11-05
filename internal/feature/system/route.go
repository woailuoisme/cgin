package system

import (
	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册根路径与系统健康探测端点，不挂载常规鉴权与限流中间件。
func RegisterRoutes(engine *gin.Engine, h *SystemHandler) {
	engine.GET("/", h.Index)
	engine.GET("/livez", h.Liveness)
	engine.GET("/readyz", h.Readiness)
}

// RegisterSystemRoutes 兼容别名。
func RegisterSystemRoutes(engine *gin.Engine, h *SystemHandler) {
	RegisterRoutes(engine, h)
}
