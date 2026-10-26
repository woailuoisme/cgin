// Package handler 汇集客户端 HTTP 适配层：参数绑定、身份提取与响应写回。
package handler

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/dto"
	"cgin/internal/service"
	"cgin/pkg/authentication"
	"cgin/pkg/response"
	"cgin/pkg/validator"
)

// ClientAuthHandler 是认证端点的 HTTP 适配层：只做绑定、取身份、调用服务、写响应。
type ClientAuthHandler struct {
	svc *service.ClientAuthService
}

// NewClientAuthHandler 构造 ClientAuthHandler。
func NewClientAuthHandler(svc *service.ClientAuthService) *ClientAuthHandler {
	return &ClientAuthHandler{svc: svc}
}

// SignUpEmail 处理 POST /v1/auth/sign-up/email。
//
// @Summary      邮箱注册
// @Description  创建邮箱密码账号，并按 Q15 的约定直接完成登录、返回一对令牌。
// @Description  注册出来的 emailVerified 恒为 false；是否要求先验证邮箱由 REQUIRE_EMAIL_VERIFICATION 决定。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Param        payload body dto.SignUpEmailBody true "注册信息"
// @Success      201 {object} dto.SessionResource "注册成功并已登录"
// @x-order      1
// @Router       /auth/sign-up/email [post]
func (h *ClientAuthHandler) SignUpEmail(c *gin.Context) {
	var body dto.SignUpEmailBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	session, err := h.svc.SignUpEmail(c.Request.Context(), body)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.Created(c, session)
}

// SignInEmail 处理 POST /v1/auth/sign-in/email。
//
// @Summary      邮箱密码登录
// @Description  邮箱不存在与口令错误返回同一个错误码，避免账号枚举。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Param        payload body dto.SignInEmailBody true "登录凭据"
// @Success      200 {object} dto.SessionResource "登录成功"
// @x-order      2
// @Router       /auth/sign-in/email [post]
func (h *ClientAuthHandler) SignInEmail(c *gin.Context) {
	var body dto.SignInEmailBody
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

// SignInUsername 处理 POST /v1/auth/sign-in/username。
//
// @Summary      用户名密码登录
// @Description  与邮箱登录共用同一套凭据校验，失败时的错误码与状态码完全一致。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Param        payload body dto.SignInUsernameBody true "登录凭据"
// @Success      200 {object} dto.SessionResource "登录成功"
// @x-order      3
// @Router       /auth/sign-in/username [post]
func (h *ClientAuthHandler) SignInUsername(c *gin.Context) {
	var body dto.SignInUsernameBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	session, err := h.svc.SignInUsername(c.Request.Context(), body)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, session)
}

// SignOut 处理 POST /v1/auth/sign-out。请求体可省略。
//
// @Summary      登出
// @Description  作废当前访问令牌。同时传入刷新令牌才会一并作废，否则续签通道仍然敞开。
// @Description  传入的刷新令牌已失效或不属于当前用户时静默忽略，因为访问令牌已经作废。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        payload body dto.SignOutBody false "可选的刷新令牌"
// @Success      204 "已登出，无响应体"
// @x-order      6
// @Router       /auth/sign-out [post]
func (h *ClientAuthHandler) SignOut(c *gin.Context) {
	if _, err := authentication.RequireIdentity(c.Request.Context()); err != nil {
		_ = c.Error(err)
		return
	}

	var body dto.SignOutBody
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

// GetSession 处理 GET /v1/auth/get-session。
//
// @Summary      读取当前身份
// @Description  纯只读、不续签：访问令牌过期即 401，客户端自行用 /auth/token/refresh 换新令牌（Q13）。
// @Tags         认证中心
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} dto.GetSessionResource "当前登录用户"
// @x-order      4
// @Router       /auth/get-session [get]
func (h *ClientAuthHandler) GetSession(c *gin.Context) {
	identity, err := authentication.RequireIdentity(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}

	session, err := h.svc.GetSession(c.Request.Context(), identity.UserID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, session)
}

// Refresh 处理 POST /v1/auth/token/refresh。
//
// @Summary      续签令牌
// @Description  用刷新令牌换取新的一对令牌；旧的刷新令牌立即作废（单次消费 + 轮换）。
// @Description  响应里的 role 取库中最新值而非旧令牌声明，因此改角色会在续签时自动生效。
// @Description  客户端必须始终持有最新一枚刷新令牌，任何"缓存旧令牌重试"的实现都会在第二次使用时失败。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Param        payload body dto.RefreshTokenBody true "刷新令牌"
// @Success      200 {object} dto.SessionResource "新的一对令牌"
// @x-order      5
// @Router       /auth/token/refresh [post]
func (h *ClientAuthHandler) Refresh(c *gin.Context) {
	var body dto.RefreshTokenBody
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

// ChangePassword 处理 POST /v1/auth/change-password。
//
// @Summary      修改口令
// @Description  校验当前口令后写入新口令。已签发的令牌不会因此失效——这是 ADR-0001 记录在案的取舍。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        payload body dto.ChangePasswordBody true "当前口令与新口令"
// @Success      204 "修改成功，无响应体"
// @x-order      9
// @Router       /auth/change-password [post]
func (h *ClientAuthHandler) ChangePassword(c *gin.Context) {
	identity, err := authentication.RequireIdentity(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}

	var body dto.ChangePasswordBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	if err := h.svc.ChangePassword(c.Request.Context(), identity.UserID, body); err != nil {
		_ = c.Error(err)
		return
	}
	response.NoContent(c)
}

// ForgetPassword 处理 POST /v1/auth/forget-password，投递重置链接。
//
// @Summary      请求密码重置链接
// @Description  向邮箱投递一条携带一次性令牌的链接，令牌寿命为 PASSWORD_RESET_TTL。
// @Description  邮箱不存在时同样返回 200，避免通过该端点枚举账号。
// @Description  与 /auth/request-password-reset 的区别：本端点投递链接，那个端点投递 6 位验证码。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Param        payload body dto.ForgetPasswordBody true "账号邮箱"
// @Success      200 {object} dto.StatusResource "已受理（邮箱不存在时也返回此结果）"
// @x-order      10
// @Router       /auth/forget-password [post]
func (h *ClientAuthHandler) ForgetPassword(c *gin.Context) {
	var body dto.ForgetPasswordBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	result, err := h.svc.ForgetPassword(c.Request.Context(), c.GetHeader("Accept-Language"), body)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, result)
}

// RequestPasswordReset 处理 POST /v1/auth/request-password-reset，投递重置验证码。
//
// @Summary      请求密码重置验证码
// @Description  向邮箱投递一枚 6 位数字验证码，寿命为 OTP_TTL，用户随后连同新口令提交给 /auth/reset-password。
// @Description  邮箱不存在时同样返回 200，避免通过该端点枚举账号。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Param        payload body dto.RequestPasswordResetBody true "账号邮箱"
// @Success      200 {object} dto.StatusResource "已受理（邮箱不存在时也返回此结果）"
// @x-order      11
// @Router       /auth/request-password-reset [post]
func (h *ClientAuthHandler) RequestPasswordReset(c *gin.Context) {
	var body dto.RequestPasswordResetBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	result, err := h.svc.RequestPasswordReset(c.Request.Context(), c.GetHeader("Accept-Language"), body)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, result)
}

// ResetPassword 处理 POST /v1/auth/reset-password。
//
// @Summary      重置口令
// @Description  两条重置路径互斥且都必须带 email：带 token 走链接路径，带 otp 走验证码路径，两者都不给将得到 422。
// @Description  凭据在库中以「用途 + 邮箱」为标识存储并只存 HMAC 摘要，因此缺了 email 无法反查。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Param        payload body dto.ResetPasswordBody true "重置凭据与新口令"
// @Success      204 {string} string "口令已重置"
// @x-order      12
// @Router       /auth/reset-password [post]
func (h *ClientAuthHandler) ResetPassword(c *gin.Context) {
	var body dto.ResetPasswordBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	if _, err := h.svc.ResetPassword(c.Request.Context(), body); err != nil {
		_ = c.Error(err)
		return
	}

	// api.md §5.1.4 规定重置口令返回 204：没有需要回传的业务数据，
	// 空响应体也让"重置成功"不附带任何可被误用的信息。
	response.NoContent(c)
}

// SendVerificationOTP 处理 POST /v1/auth/send-verification-otp。
//
// @Summary      发送邮箱验证码
// @Description  向邮箱投递一枚 6 位数字验证码，寿命为 OTP_TTL，用于 /auth/verify-email。
// @Description  邮箱不存在时同样返回 200，避免向未认证调用方暴露邮箱是否已注册；
// @Description  邮箱已通过验证时也直接返回 200 且不重复发信。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Param        payload body dto.SendVerificationOTPBody true "待验证的邮箱"
// @Success      200 {object} dto.StatusResource "已受理（邮箱不存在或已验证时也返回此结果）"
// @x-order      7
// @Router       /auth/send-verification-otp [post]
func (h *ClientAuthHandler) SendVerificationOTP(c *gin.Context) {
	var body dto.SendVerificationOTPBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	result, err := h.svc.SendVerificationOTP(c.Request.Context(), c.GetHeader("Accept-Language"), body)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, result)
}

// VerifyEmail 处理 POST /v1/auth/verify-email。
//
// @Summary      验证邮箱
// @Description  核销验证码并把 emailVerified 置为 true。凭据一次性使用，核销后立即失效。
// @Tags         认证中心
// @Accept       json
// @Produce      json
// @Param        payload body dto.VerifyEmailBody true "邮箱与验证码"
// @Success      200 {object} dto.StatusResource "验证通过"
// @x-order      8
// @Router       /auth/verify-email [post]
func (h *ClientAuthHandler) VerifyEmail(c *gin.Context) {
	var body dto.VerifyEmailBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	result, err := h.svc.VerifyEmail(c.Request.Context(), body)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, result)
}
