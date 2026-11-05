package v1

import (
	"github.com/gin-gonic/gin"

	clientprofile "cgin/internal/feature/client/profile"
	"cgin/pkg/authentication"
	"cgin/pkg/response"
)

// Handler 是本人档案端点的 HTTP 适配层。
type Handler struct {
	svc *clientprofile.ProfileService
}

// ProfileHandler 是 Handler 的别名，保证向后兼容。
type ProfileHandler = Handler

// NewHandler 构造 Handler。
func NewHandler(svc *clientprofile.ProfileService) *Handler {
	return &Handler{svc: svc}
}

// NewProfileHandler 构造 ProfileHandler。
func NewProfileHandler(svc *clientprofile.ProfileService) *Handler {
	return NewHandler(svc)
}

// GetProfile 处理 GET /v1/users/me。
//
// @Summary      读取本人档案
// @Description  获取当前登录用户的完整个人档案。
// @Tags         个人资料
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} ProfileResource "本人档案"
// @x-order      1
// @Router       /users/me [get]
func (h *Handler) GetProfile(c *gin.Context) {
	identity, err := authentication.RequireIdentity(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	userID := identity.UserID

	profile, err := h.svc.Get(c.Request.Context(), userID)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, profile)
}

// UpdateProfile 处理 PATCH /v1/users/me。
//
// @Summary      更新本人档案
// @Description  局部更新个人资料，支持更新昵称、头像、用户名、展示名与手机号。
// @Tags         个人资料
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        payload body UpdateProfileBody true "待更新字段，可只提交其中一部分"
// @Success      200 {object} ProfileResource "更新后的档案"
// @x-order      2
// @Router       /users/me [patch]
func (h *Handler) UpdateProfile(c *gin.Context) {
	identity, err := authentication.RequireIdentity(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	userID := identity.UserID

	var body UpdateProfileBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	profile, err := h.svc.Update(c.Request.Context(), userID, body.FieldSet())
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, profile)
}

// UploadAvatar 处理 POST /v1/users/me/avatar。
//
// @Summary      更新本人头像
// @Description  保存客户端直传 S3 成功后的头像相对路径，自动校验存储对象存在性并清理历史旧头像。
// @Tags         个人资料
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        payload body UpdateAvatarBody true "直传 S3 成功后的头像相对路径"
// @Success      200 {object} ProfileResource "更新头像后的本人档案"
// @Failure      400 {object} errs.Response "路径非法或存储对象不存在"
// @Failure      401 {object} errs.Response "未认证"
// @x-order      3
// @Router       /users/me/avatar [post]
func (h *Handler) UploadAvatar(c *gin.Context) {
	identity, err := authentication.RequireIdentity(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}
	userID := identity.UserID

	var body UpdateAvatarBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	profile, err := h.svc.UpdateAvatar(c.Request.Context(), userID, body.AvatarPath)
	if err != nil {
		_ = c.Error(err)
		return
	}

	response.OK(c, profile)
}
