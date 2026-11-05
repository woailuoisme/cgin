package v1

import (
	"github.com/gin-gonic/gin"

	"cgin/pkg/media"
	"cgin/pkg/response"
)

// Handler 是媒体上传与预签名凭据端点的 HTTP 适配层。
type Handler struct {
	uploader media.Uploader
}

// MediaHandler 是 Handler 的别名，保证向后兼容。
type MediaHandler = Handler

// NewHandler 构造 Handler。
func NewHandler(uploader media.Uploader) *Handler {
	return &Handler{uploader: uploader}
}

// NewMediaHandler 构造 MediaHandler。
func NewMediaHandler(uploader media.Uploader) *Handler {
	return NewHandler(uploader)
}

// Presign 处理 POST /v1/media/presign。
//
// @Summary      申请 S3 预签名直传凭证
// @Description  客户端上传文件前调用此接口获取带有效期的 S3 预签名直传 URL 与对象相对路径 (Key)，客户端随后直接通过 HTTP PUT 将二进制流写入 S3。
// @Tags         媒体中心
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        payload body PresignUploadBody true "预签名上传申请参数"
// @Success      200 {object} PresignUploadResource "预签名上传凭证"
// @Failure      400 {object} errs.Response "参数非法或不满足配额规则"
// @Failure      401 {object} errs.Response "未认证"
// @x-order      1
// @Router       /media/presign [post]
func (h *Handler) Presign(c *gin.Context) {
	var body PresignUploadBody
	if err := c.ShouldBindJSON(&body); err != nil {
		_ = c.Error(err)
		return
	}

	res, err := h.uploader.Presign(c.Request.Context(), media.PresignRequest{
		Category: body.Category,
		FileName: body.FileName,
		FileSize: body.FileSize,
		MimeType: body.MimeType,
	})
	if err != nil {
		_ = c.Error(err)
		return
	}

	response.OK(c, PresignUploadResource{
		UploadURL: res.UploadURL,
		Path:      res.Path,
		Method:    res.Method,
		Headers:   res.Headers,
		ExpiresIn: res.ExpiresIn,
	})
}
