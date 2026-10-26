package adminroute

import (
	"github.com/gin-gonic/gin"

	adminhandler "cgin/internal/handler/admin"
	"cgin/internal/middleware"
	"cgin/pkg/authorization"
)

// RegisterAdminUserRoutes 注册后台用户管理端点（/v1/admin/users）。
// 每个端点各自声明所需权限码，权限矩阵见 pkg/authorization/rbac/policy.csv。
func RegisterAdminUserRoutes(router *gin.RouterGroup, h *adminhandler.UserHandler, mw *middleware.Middleware) {
	group := router.Group("/admin/users", mw.Auth("admin"), mw.Throttle(middleware.TierAuthenticated))

	group.GET("", mw.Can(authorization.ResourceUsers, authorization.ActionList), h.ListUsers)
	group.GET("/export", mw.Can(authorization.ResourceUsers, authorization.ActionList), h.ExportUsers)
	group.POST("", mw.Can(authorization.ResourceUsers, authorization.ActionCreate), h.CreateUser)
	group.GET("/:id", mw.Can(authorization.ResourceUsers, authorization.ActionRead), h.GetUser)
	group.PATCH("/:id", mw.Can(authorization.ResourceUsers, authorization.ActionUpdate), h.UpdateUser)
	group.DELETE("/:id", mw.Can(authorization.ResourceUsers, authorization.ActionDelete), h.DeleteUser)
}
