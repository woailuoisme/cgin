package wechat

import (
	"context"
	"errors"
	"strings"
	"time"

	"bgin/internal/platform/errs"
)

// phoneNumberPath 是手机号快速验证端点，用小程序端 getPhoneNumber 回调的 code 换取手机号。
const phoneNumberPath = "/wxa/business/getuserphonenumber"

// maxWatermarkClockDrift 定义水印时间戳允许的最大时钟偏差，防止截获重放。
const maxWatermarkClockDrift = 5 * time.Minute

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

// phoneService 实现 PhoneService 接口。
type phoneService struct {
	client *Client
}

var _ PhoneService = (*phoneService)(nil)

// GetPhoneNumber 用小程序端 getPhoneNumber 回调返回的 code 换取用户手机号。
//
// 该接口为微信按成功次数收费接口，且 code 只能使用一次。
// 为防止网络抖动导致的重复计费，本方法严格禁用网络层盲目重试；
// 仅当 access_token 凭据失效（40001/40014/42001）时触发一次刷新重试。
func (s *phoneService) GetPhoneNumber(ctx context.Context, code string) (*PhoneInfo, error) {
	const operation = "wechat.phone.GetPhoneNumber"

	if s == nil || s.client == nil || s.client.http == nil {
		return nil, errs.ServiceUnavailable("wechat miniprogram client is not configured")
	}

	trimmedCode := strings.TrimSpace(code)
	if trimmedCode == "" {
		return nil, errs.BadRequest("code is required")
	}

	var payload phoneNumberResponse

	err := s.client.withAccessToken(ctx, func(callCtx context.Context, token string) (int, error) {
		payload = phoneNumberResponse{}

		response, requestErr := s.client.http.R().
			SetContext(callCtx).
			SetQueryParam("access_token", token).
			SetBody(map[string]string{"code": trimmedCode}).
			Post(phoneNumberPath)
		if requestErr != nil {
			return 0, errs.ServiceUnavailable("wechat getuserphonenumber request failed: " + s.client.redactError(requestErr))
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
	if err := payload.PhoneInfo.Watermark.Validate(s.client.appID, time.Now()); err != nil {
		return nil, err
	}

	return &payload.PhoneInfo, nil
}
