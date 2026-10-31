package dto

import (
	"github.com/samber/lo"

	"cgin/pkg/media"
)

// SetDefaultEngine 设置全局默认媒体引擎，供 DTO 投影时构建带防篡改签名的访问地址。
func SetDefaultEngine(engine media.Engine) {
	media.SetDefaultEngine(engine)
}

// PresignUploadBody 描述客户端申请 S3 预签名直传凭证的请求载荷。
type PresignUploadBody struct {
	// 业务存储类目（如 avatars, covers, documents, attachments）
	Category string `json:"category" validate:"required,oneof=avatars covers documents attachments" example:"avatars"`
	// 上传的原始文件名（用于清洗及保留扩展名）
	FileName string `json:"file_name" validate:"required,min=1,max=255" example:"avatar.png"`
	// 文件预期大小（字节单位，用于前置配额校验）
	FileSize int64 `json:"file_size" validate:"required,gt=0,lte=104857600" example:"1048576"`
	// 文件的 MIME 类型（如 image/png, image/jpeg）
	MimeType string `json:"mime_type" validate:"required" example:"image/png"`
}

// PresignUploadResource 描述向客户端签发的预签名上传凭证。
type PresignUploadResource struct {
	// S3 预签名直传目标地址（带临时安全签名参数）
	UploadURL string `json:"upload_url" example:"https://s3.amazonaws.com/bucket/uploads/avatars/20261031/uuid.png?X-Amz-..."`
	// 对象在存储桶中的相对路径（Key），客户端直传成功后向业务接口提交此路径
	Path string `json:"path" example:"uploads/avatars/20261031/uuid.png"`
	// 直传 HTTP 方法（固定为 PUT）
	Method string `json:"method" example:"PUT"`
	// 客户端执行直传时必须携带的 HTTP 请求头
	Headers map[string]string `json:"headers"`
	// 凭证有效倒计时（秒）
	ExpiresIn int64 `json:"expires_in" example:"900"`
}

// ImageVariantResource 封装标准化的富媒体图片复合响应对象。
type ImageVariantResource = media.ImageVariants

// BuildAvatar 根据存储相对路径构建复合头像对象；路径为空时安全返回 nil。
func BuildAvatar(path *string, engine ...media.Engine) *ImageVariantResource {
	return media.BuildImageVariants(lo.FromPtr(path), engine...)
}
