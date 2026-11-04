package wechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	//nolint:gosec // WeChat specification mandates SHA-1 for user data signature verification.
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-resty/resty/v2"

	"cgin/pkg/errs"
)

const (
	// phoneNumberPath 是手机号快速验证端点，用小程序端 getPhoneNumber 回调的 code 换取手机号。
	phoneNumberPath = "/wxa/business/getuserphonenumber"
	// maxWatermarkClockDrift 定义水印时间戳允许的最大时钟偏差，防止截获重放。
	maxWatermarkClockDrift = 5 * time.Minute
	// unlimitedCodePath 是数量不限的小程序码端点。
	unlimitedCodePath = "/wxa/getwxacodeunlimit"
	// maxSceneLength 是 scene 允许的最大可见字符数（按字符数而非字节数计）。
	maxSceneLength = 32
	// sceneSpecialRunes 是 scene 除数字与大小写字母外允许出现的特殊字符。
	sceneSpecialRunes = "!#$&'()*+,/:;=?@-._~"
)

// Session 是小程序登录成功后取得的用户身份。
//
// SessionKey 是对称会话密钥，仅供服务端解密与数据验签使用，绝对严禁下发给前端。
// 本结构在字段级别声明 json:"-"，并实现了 fmt.Stringer 与 fmt.GoStringer 进行强制脱敏，
// 杜绝因序列化、日志打印或反射操作导致的意外密钥泄露。
type Session struct {
	// OpenID 用户在该小程序内的唯一标识。
	OpenID string `json:"openid"`
	// UnionID 用户在微信开放平台账号下的唯一标识。
	UnionID string `json:"unionid,omitempty"`
	// SessionKey 会话密钥，仅供服务端使用。
	SessionKey string `json:"-"`
}

// String 格式化 Session 并脱敏输出 SessionKey。
func (s *Session) String() string {
	if s == nil {
		return "<nil>"
	}
	return fmt.Sprintf("Session{OpenID:%q, UnionID:%q, SessionKey:\"[REDACTED]\"}", s.OpenID, s.UnionID)
}

// GoString 实现 fmt.GoStringer 保证调试输出安全。
func (s *Session) GoString() string {
	return s.String()
}

// code2SessionResponse 是 code2session 的响应报文。
type code2SessionResponse struct {
	upstreamError
	OpenID     string `json:"openid"`
	SessionKey string `json:"session_key"`
	UnionID    string `json:"unionid"`
}

// Code2Session 用 wx.login() 返回的 code 校验并换取用户身份。
//
// 微信规定 code 只能使用一次且 5 分钟有效，因此本方法不做重试。
func (c *Client) Code2Session(ctx context.Context, code string) (*Session, error) {
	const operation = "wechat.auth.Code2Session"

	if c == nil || c.http == nil {
		return nil, errs.ServiceUnavailable("wechat miniprogram client is not configured")
	}

	trimmedCode := strings.TrimSpace(code)
	if trimmedCode == "" {
		return nil, errs.BadRequest("code is required")
	}

	response, err := c.http.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"appid":      c.appID,
			"secret":     c.appSecret,
			"js_code":    trimmedCode,
			"grant_type": grantTypeAuthorizationCode,
		}).
		Get(code2SessionPath)
	if err != nil {
		return nil, errs.ServiceUnavailable("wechat code2session request failed: " + c.redactError(err))
	}

	var payload code2SessionResponse
	if _, decodeErr := decodeBusinessResponse(operation, response, &payload); decodeErr != nil {
		return nil, decodeErr
	}

	if payload.OpenID == "" {
		return nil, errs.Internal(errors.New("wechat code2session succeeded without openid"))
	}

	return &Session{
		OpenID:     payload.OpenID,
		UnionID:    payload.UnionID,
		SessionKey: payload.SessionKey,
	}, nil
}

// Watermark 是微信为敏感数据附加的归属水印。
type Watermark struct {
	AppID     string `json:"appid"`
	Timestamp int64  `json:"timestamp"`
}

// Validate 校验水印的合法性（包括 AppID 归属及时钟漂移检查）。
func (w Watermark) Validate(expectedAppID string, now time.Time) error {
	if w.AppID != expectedAppID {
		return errs.Forbidden("wechat: phone number watermark does not belong to this miniprogram")
	}

	watermarkTime := time.Unix(w.Timestamp, 0)
	diff := now.Sub(watermarkTime)
	if diff < -maxWatermarkClockDrift || diff > maxWatermarkClockDrift {
		return errs.Forbidden("wechat: phone number watermark timestamp expired or invalid")
	}

	return nil
}

// PhoneInfo 是手机号快速验证返回的用户手机号信息。
type PhoneInfo struct {
	// PhoneNumber 用户绑定的手机号，国外号码会带区号。
	PhoneNumber string `json:"phoneNumber"`
	// PurePhoneNumber 不带区号的手机号。
	PurePhoneNumber string `json:"purePhoneNumber"`
	// CountryCode 国家区号，例如 86。
	CountryCode string `json:"countryCode"`
	// Watermark 数据归属水印，由本包校验后再返回。
	Watermark Watermark `json:"watermark"`
}

// E164 把微信返回的手机号规格化为标准 E.164 格式（例如 +8613800138000）。
//
// 微信的 PhoneNumber 只在国外号码上才带区号，直接落库会导致同一号码存在多种形态，
// 统一规格化为 E.164 格式能保证在账号体系与用户唯一索引中的一致性。
func (p *PhoneInfo) E164() (string, error) {
	if p == nil {
		return "", errs.Internal(errors.New("wechat phone info is nil"))
	}

	pure := strings.TrimSpace(p.PurePhoneNumber)
	country := strings.TrimSpace(p.CountryCode)

	if pure == "" || country == "" {
		return "", errs.Internal(errors.New("wechat returned incomplete phone number"))
	}

	return "+" + strings.TrimPrefix(country, "+") + pure, nil
}

// phoneNumberResponse 是 getuserphonenumber 的响应报文。
type phoneNumberResponse struct {
	upstreamError
	PhoneInfo PhoneInfo `json:"phone_info"`
}

// GetPhoneNumber 用小程序端 getPhoneNumber 回调返回的 code 换取用户手机号。
//
// 该接口为微信按成功次数收费接口，且 code 只能使用一次。
// 为防止网络抖动导致的重复计费，本方法严格禁用网络层盲目重试；
// 仅当 access_token 凭据失效（40001/40014/42001）时触发一次刷新重试。
func (c *Client) GetPhoneNumber(ctx context.Context, code string) (*PhoneInfo, error) {
	const operation = "wechat.phone.GetPhoneNumber"

	if c == nil || c.http == nil {
		return nil, errs.ServiceUnavailable("wechat miniprogram client is not configured")
	}

	trimmedCode := strings.TrimSpace(code)
	if trimmedCode == "" {
		return nil, errs.BadRequest("code is required")
	}

	var payload phoneNumberResponse

	err := c.withAccessToken(ctx, func(callCtx context.Context, token string) (int, error) {
		payload = phoneNumberResponse{}

		response, requestErr := c.http.R().
			SetContext(callCtx).
			SetQueryParam("access_token", token).
			SetBody(map[string]string{"code": trimmedCode}).
			Post(phoneNumberPath)
		if requestErr != nil {
			return 0, errs.ServiceUnavailable("wechat getuserphonenumber request failed: " + c.redactError(requestErr))
		}

		return decodeBusinessResponse(operation, response, &payload)
	})
	if err != nil {
		return nil, err
	}

	if payload.PhoneInfo.PhoneNumber == "" && payload.PhoneInfo.PurePhoneNumber == "" {
		return nil, errs.Internal(errors.New("wechat returned empty phone number"))
	}

	// 归属水印与时间戳防重放校验
	if err := payload.PhoneInfo.Watermark.Validate(c.appID, time.Now()); err != nil {
		return nil, err
	}

	return &payload.PhoneInfo, nil
}

// EnvVersion 指定小程序码指向的小程序版本。
type EnvVersion string

// 小程序码可用的小程序版本。
const (
	EnvVersionRelease EnvVersion = "release"
	EnvVersionTrial   EnvVersion = "trial"
	EnvVersionDevelop EnvVersion = "develop"
)

// IsValid 判断小程序版本取值是否合法。
func (v EnvVersion) IsValid() bool {
	switch v {
	case EnvVersionRelease, EnvVersionTrial, EnvVersionDevelop:
		return true
	default:
		return false
	}
}

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

// GetUnlimitedCode 生成数量不限的小程序码，返回 JPEG 字节流。
//
// 只返回字节流、不落盘：小程序码是永久有效的，预生成、去重与分发属于业务语义，由调用方决定。
func (c *Client) GetUnlimitedCode(ctx context.Context, params UnlimitedCodeParams) ([]byte, error) {
	const operation = "wechat.wxacode.GetUnlimitedCode"

	if c == nil || c.http == nil {
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

	err := c.withAccessToken(ctx, func(callCtx context.Context, token string) (int, error) {
		code = nil

		response, requestErr := c.http.R().
			SetContext(callCtx).
			SetQueryParam("access_token", token).
			SetBody(body).
			Post(unlimitedCodePath)
		if requestErr != nil {
			return 0, errs.ServiceUnavailable("wechat getwxacodeunlimit request failed: " + c.redactError(requestErr))
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

// isSceneRuneAllowed 判断字符是否属于 scene 允许的字符集。
func isSceneRuneAllowed(r rune) bool {
	isDigit := r >= '0' && r <= '9'
	isLower := r >= 'a' && r <= 'z'
	isUpper := r >= 'A' && r <= 'Z'

	return isDigit || isLower || isUpper || strings.ContainsRune(sceneSpecialRunes, r)
}

// isJSONResponse 判断响应是否为 JSON 报文。
func isJSONResponse(response *resty.Response) bool {
	if strings.Contains(response.Header().Get("Content-Type"), "json") {
		return true
	}

	return bytes.HasPrefix(bytes.TrimSpace(response.Body()), []byte("{"))
}

// Decrypt 使用 SessionKey 与 IV 解密微信小程序敏感数据（AES-128-CBC + PKCS#7）。
//
// 输入均为 Base64 编码的字符串，成功解密后返回原始 JSON 字节切片。
func Decrypt(sessionKey, encryptedData, iv string) ([]byte, error) {
	keyBytes, err := base64.StdEncoding.DecodeString(sessionKey)
	if err != nil {
		return nil, errs.BadRequest("wechat crypto: invalid base64 session_key")
	}
	if len(keyBytes) != aes.BlockSize {
		return nil, errs.BadRequest(fmt.Sprintf("wechat crypto: invalid session_key length, expected %d bytes", aes.BlockSize))
	}

	ivBytes, err := base64.StdEncoding.DecodeString(iv)
	if err != nil {
		return nil, errs.BadRequest("wechat crypto: invalid base64 iv")
	}
	if len(ivBytes) != aes.BlockSize {
		return nil, errs.BadRequest(fmt.Sprintf("wechat crypto: invalid iv length, expected %d bytes", aes.BlockSize))
	}

	cipherBytes, err := base64.StdEncoding.DecodeString(encryptedData)
	if err != nil {
		return nil, errs.BadRequest("wechat crypto: invalid base64 encrypted_data")
	}
	if len(cipherBytes) == 0 || len(cipherBytes)%aes.BlockSize != 0 {
		return nil, errs.BadRequest("wechat crypto: invalid encrypted_data length, not aligned to block size")
	}

	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("wechat crypto: create aes cipher failed: %w", err))
	}

	plainText := make([]byte, len(cipherBytes))
	mode := cipher.NewCBCDecrypter(block, ivBytes)
	mode.CryptBlocks(plainText, cipherBytes)

	unpadded, err := pkcs7Unpad(plainText, aes.BlockSize)
	if err != nil {
		return nil, errs.BadRequest("wechat crypto: invalid pkcs7 padding in decrypted data")
	}

	return unpadded, nil
}

// VerifySignature 校验微信用户数据的 SHA-1 签名（sha1(rawData + sessionKey)）。
//
// 使用 subtle.ConstantTimeCompare 预防计时侧信道攻击。
func VerifySignature(sessionKey, rawData, signature string) bool {
	if sessionKey == "" || signature == "" {
		return false
	}

	//nolint:gosec // WeChat requires SHA-1 for signature verification.
	sum := sha1.Sum([]byte(rawData + sessionKey))
	computed := hex.EncodeToString(sum[:])

	return subtle.ConstantTimeCompare([]byte(computed), []byte(signature)) == 1
}

// pkcs7Unpad 剥离 PKCS#7 填充。
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	length := len(data)
	if length == 0 || length%blockSize != 0 {
		return nil, errors.New("invalid data length")
	}

	padding := data[length-1]
	padLen := int(padding)
	if padLen == 0 || padLen > blockSize || padLen > length {
		return nil, errors.New("invalid padding value")
	}

	// 验证所有 padding 字节是否一致
	for i := length - padLen; i < length; i++ {
		if data[i] != padding {
			return nil, errors.New("inconsistent padding bytes")
		}
	}

	return data[:length-padLen], nil
}
