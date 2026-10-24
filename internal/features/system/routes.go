// Package system 提供根路径 (/)、存活 (/livez) 与就绪 (/readyz) 系统健康探测端点。
package system

import "github.com/gin-gonic/gin"

// RegisterRoutes 注册系统健康探测与根路径路由，端点不挂载常规鉴权与限流中间件。
func RegisterRoutes(engine *gin.Engine, handler *Handler) {
	engine.GET("/", handler.Index)
	engine.GET("/livez", handler.Liveness)
	engine.GET("/readyz", handler.Readiness)
}
