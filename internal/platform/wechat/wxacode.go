package wechat

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/go-resty/resty/v2"

	"bgin/internal/platform/errs"
)

const (
	// unlimitedCodePath 是数量不限的小程序码端点。
	unlimitedCodePath = "/wxa/getwxacodeunlimit"
	// maxSceneLength 是 scene 允许的最大可见字符数（按字符数而非字节数计）。
	maxSceneLength = 32
	// sceneSpecialRunes 是 scene 除数字与大小写字母外允许出现的特殊字符。
	sceneSpecialRunes = "!#$&'()*+,/:;=?@-._~"
)

// EnvVersion 指定小程序码指向的小程序版本。
type EnvVersion string

// 小程序码可用的小程序版本。
const (
	EnvVersionRelease EnvVersion = "release"
	EnvVersionTrial   EnvVersion = "trial"
	EnvVersionDevelop EnvVersion = "develop"
)

// UnlimitedCodeParams 是获取数量不限小程序码的入参。
type UnlimitedCodeParams struct {
	// Scene 是扫码进入页面时携带的参数，最多 32 个可见字符。
	Scene string
	// Page 是扫码进入的页面路径，形如 pages/index/index，不能带前导斜杠；留空表示主页。
	Page string
	// EnvVersion 指定打开哪个版本，留空等同于 release。
	EnvVersion EnvVersion
	// Width 是码的宽度（像素），留空由微信按默认值处理。
	Width int
	// IsHyaline 为 true 时生成透明底色的小程序码。
	IsHyaline bool
}

// wxaCodeService 实现 WxaCodeService 接口。
type wxaCodeService struct {
	client *Client
}

var _ WxaCodeService = (*wxaCodeService)(nil)

// GetUnlimited 生成数量不限的小程序码，返回 JPEG 字节流。
//
// 只返回字节流、不落盘：小程序码是永久有效的，预生成、去重与分发属于业务语义，由调用方决定。
func (s *wxaCodeService) GetUnlimited(ctx context.Context, params UnlimitedCodeParams) ([]byte, error) {
	const operation = "wechat.wxacode.GetUnlimited"

	if s == nil || s.client == nil || s.client.http == nil {
		return nil, errs.ServiceUnavailable("wechat miniprogram client is not configured")
	}

	params = params.Normalize()
	if err := params.Validate(); err != nil {
		return nil, err
	}

	body := map[string]any{"scene": params.Scene}
	if params.Page != "" {
		body["page"] = params.Page
	}
	if params.EnvVersion != "" {
		body["env_version"] = string(params.EnvVersion)
	}
	if params.Width > 0 {
		body["width"] = params.Width
	}
	if params.IsHyaline {
		body["is_hyaline"] = true
	}

	var code []byte

	err := s.client.withAccessToken(ctx, func(callCtx context.Context, token string) (int, error) {
		code = nil

		response, requestErr := s.client.http.R().
			SetContext(callCtx).
			SetQueryParam("access_token", token).
			SetBody(body).
			Post(unlimitedCodePath)
		if requestErr != nil {
			return 0, errs.ServiceUnavailable("wechat getwxacodeunlimit request failed: " + s.client.redactError(requestErr))
		}

		if !isJSONResponse(response) {
			code = response.Body()

			return 0, nil
		}

		var payload upstreamError
		if errCode, decodeErr := decodeBusinessResponse(operation, response, &payload); decodeErr != nil {
			return errCode, decodeErr
		}

		return 0, errs.Internal(errors.New("wechat getwxacodeunlimit returned an unexpected json body"))
	})
	if err != nil {
		return nil, err
	}

	if len(code) == 0 {
		return nil, errs.Internal(errors.New("wechat getwxacodeunlimit succeeded without image body"))
	}

	return code, nil
}

// Normalize 清洗参数，避免把首尾空白拼进 scene 或页面路径。
func (p UnlimitedCodeParams) Normalize() UnlimitedCodeParams {
	p.Scene = strings.TrimSpace(p.Scene)
	p.Page = strings.TrimSpace(p.Page)

	return p
}

// Validate 在本地拦截注定被上游拒绝的参数。
func (p UnlimitedCodeParams) Validate() error {
	if p.Scene == "" {
		return errs.BadRequest("scene is required")
	}

	if utf8.RuneCountInString(p.Scene) > maxSceneLength {
		return errs.BadRequest("scene must not exceed 32 visible characters")
	}

	for _, r := range p.Scene {
		if !isSceneRuneAllowed(r) {
			return errs.BadRequest("scene contains characters not supported by wechat")
		}
	}

	if strings.HasPrefix(p.Page, "/") {
		return errs.BadRequest("page must not start with a slash")
	}

	if p.EnvVersion != "" && !p.EnvVersion.IsValid() {
		return errs.BadRequest("env_version must be one of release, trial, develop")
	}

	return nil
}

// isSceneRuneAllowed 判断字符是否属于 scene 允许的字符集。
func isSceneRuneAllowed(r rune) bool {
	isDigit := r >= '0' && r <= '9'
	isLower := r >= 'a' && r <= 'z'
	isUpper := r >= 'A' && r <= 'Z'

	return isDigit || isLower || isUpper || strings.ContainsRune(sceneSpecialRunes, r)
}

// IsValid 判断小程序版本取值是否合法。
func (v EnvVersion) IsValid() bool {
	switch v {
	case EnvVersionRelease, EnvVersionTrial, EnvVersionDevelop:
		return true
	default:
		return false
	}
}

// isJSONResponse 判断响应是否为 JSON 报文。
func isJSONResponse(response *resty.Response) bool {
	if strings.Contains(response.Header().Get("Content-Type"), "json") {
		return true
	}

	return bytes.HasPrefix(bytes.TrimSpace(response.Body()), []byte("{"))
}
