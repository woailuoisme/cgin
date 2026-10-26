// Package adminhandler 汇集管理端 HTTP 适配层。
//
// 包名带 admin 前缀，避免与客户端 handler 包同名遮蔽。
package adminhandler

import (
	"github.com/gin-gonic/gin"

	admindto "cgin/internal/dto/admin"
	adminservice "cgin/internal/service/admin"
	"cgin/pkg/authentication"
	"cgin/pkg/response"
	"cgin/pkg/validator"
)

// AuthHandler 是管理员认证的 HTTP 适配层。
type AuthHandler struct {
	svc *adminservice.AuthService
}

// NewAuthHandler 构造 AuthHandler。
func NewAuthHandler(svc *adminservice.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// SignInEmail 处理 POST /v1/admin/auth/sign-in/email。
//
// @Summary      管理员邮箱密码登录
// @Description  校验管理员邮箱与口令；邮箱不存在、口令错误或角色非系统管理员统一返回凭据错误，防止账号枚举。登录成功后颁发双令牌。
// @Tags         管理员认证
// @Accept       json
// @Produce      json
// @Param        payload body admindto.SignInEmailBody true "管理员登录凭据"
// @Success      200 {object} admindto.SessionResource "登录成功并颁发管理端双令牌"
// @x-order      1
// @Router       /admin/auth/sign-in/email [post]
func (h *AuthHandler) SignInEmail(c *gin.Context) {
	var body admindto.SignInEmailBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	session, err := h.svc.SignInEmail(c.Request.Context(), body)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, session)
}

// GetSession 处理 GET /v1/admin/auth/get-session。
//
// @Summary      获取当前管理员会话
// @Description  根据当前有效的管理员访问令牌，返回当前管理员的档案资料与权限角色。
// @Tags         管理员认证
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} admindto.GetSessionResource "当前管理员会话档案"
// @x-order      2
// @Router       /admin/auth/get-session [get]
func (h *AuthHandler) GetSession(c *gin.Context) {
	id, err := authentication.RequireIdentity(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}

	res, err := h.svc.GetSession(c.Request.Context(), id.UserID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, res)
}

// Refresh 处理 POST /v1/admin/auth/token/refresh。
//
// @Summary      刷新管理员双令牌
// @Description  使用未过期的管理员刷新令牌轮换一对新的双令牌，旧刷新令牌立即作废。
// @Tags         管理员认证
// @Accept       json
// @Produce      json
// @Param        payload body admindto.RefreshTokenBody true "刷新令牌凭据"
// @Success      200 {object} admindto.SessionResource "刷新成功并颁发新双令牌"
// @x-order      3
// @Router       /admin/auth/token/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	var body admindto.RefreshTokenBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	session, err := h.svc.Refresh(c.Request.Context(), body)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, session)
}

// SignOut 处理 POST /v1/admin/auth/sign-out。
//
// @Summary      管理员登出
// @Description  作废当前管理员访问令牌。若同时传入刷新令牌则一并作废，否则续签通道仍然敞开。
// @Tags         管理员认证
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        payload body admindto.SignOutBody false "可选的刷新令牌"
// @Success      204 "已登出，无响应体"
// @x-order      4
// @Router       /admin/auth/sign-out [post]
func (h *AuthHandler) SignOut(c *gin.Context) {
	if _, err := authentication.RequireIdentity(c.Request.Context()); err != nil {
		_ = c.Error(err)
		return
	}

	var body admindto.SignOutBody
	if err := validator.BindOptionalJSON(c, &body); err != nil {
		_ = c.Error(err)
		return
	}

	if err := h.svc.SignOut(c.Request.Context(), body); err != nil {
		_ = c.Error(err)
		return
	}
	response.NoContent(c)
}

// ChangePassword 处理 POST /v1/admin/auth/change-password。
//
// @Summary      管理员修改口令
// @Description  校验当前管理员口令后写入新口令。
// @Tags         管理员认证
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        payload body admindto.ChangePasswordBody true "当前口令与新口令"
// @Success      204 "修改成功，无响应体"
// @x-order      5
// @Router       /admin/auth/change-password [post]
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	id, err := authentication.RequireIdentity(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}

	var body admindto.ChangePasswordBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	if err := h.svc.ChangePassword(c.Request.Context(), id.UserID, body); err != nil {
		_ = c.Error(err)
		return
	}
	response.NoContent(c)
}
