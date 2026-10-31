package handler

import (
	"github.com/gin-gonic/gin"

	"cgin/internal/dto"
	"cgin/pkg/media"
	"cgin/pkg/response"
)

// MediaHandler 是媒体上传与预签名凭据端点的 HTTP 适配层。
type MediaHandler struct {
	engine media.Engine
}

// NewMediaHandler 构造 MediaHandler。
func NewMediaHandler(engine media.Engine) *MediaHandler {
	return &MediaHandler{engine: engine}
}

// Presign 处理 POST /v1/media/presign。
//
// @Summary      申请 S3 预签名直传凭证
// @Description  客户端上传文件前调用此接口获取带有效期的 S3 预签名直传 URL 与对象相对路径 (Key)，客户端随后直接通过 HTTP PUT 将二进制流写入 S3。
// @Tags         媒体中心
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        payload body dto.PresignUploadBody true "预签名上传申请参数"
// @Success      200 {object} dto.PresignUploadResource "预签名上传凭证"
// @Failure      400 {object} errs.Response "参数非法或不满足配额规则"
// @Failure      401 {object} errs.Response "未认证"
// @x-order      1
// @Router       /media/presign [post]
func (h *MediaHandler) Presign(c *gin.Context) {
	var body dto.PresignUploadBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	res, err := h.engine.Presign(c.Request.Context(), media.PresignRequest{
		Category: body.Category,
		FileName: body.FileName,
		FileSize: body.FileSize,
		MimeType: body.MimeType,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}

	response.OK(c, dto.PresignUploadResource{
		UploadURL: res.UploadURL,
		Path:      res.Path,
		Method:    res.Method,
		Headers:   res.Headers,
		ExpiresIn: res.ExpiresIn,
	})
}
