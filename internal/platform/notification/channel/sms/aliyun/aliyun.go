// Package aliyun 提供基于阿里云短信服务 (Dysmsapi) 官方 POP RPC 协议的短信驱动实现。
package aliyun

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // 阿里云 POP 签名协议强制要求 HMAC-SHA1
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/channel/sms"
)

const (
	defaultEndpoint = "https://dysmsapi.aliyuncs.com"
	defaultTimeout  = 10 * time.Second
)

// Config 阿里云短信配置。
type Config struct {
	AccessKeyID     string
	AccessKeySecret string
	SignName        string
	TemplateCode    string
	Endpoint        string
	Timeout         time.Duration
}

// Driver 阿里云短信驱动。
type Driver struct {
	cfg    Config
	client *resty.Client
}

var _ sms.Driver = (*Driver)(nil)

// NewDriver 创建阿里云短信驱动实例。
func NewDriver(cfg Config) (*Driver, error) {
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("aliyun sms: access_key_id and access_key_secret are required")
	}

	endpoint := cmp.Or(cfg.Endpoint, defaultEndpoint)
	timeout := cmp.Or(cfg.Timeout, defaultTimeout)

	client := resty.New().
		SetBaseURL(endpoint).
		SetTimeout(timeout)

	return &Driver{
		cfg:    cfg,
		client: client,
	}, nil
}

// Name 返回驱动标识名。
func (d *Driver) Name() string {
	return "aliyun"
}

type aliyunSendSmsResponse struct {
	Code      string `json:"Code"`
	Message   string `json:"Message"`
	BizID     string `json:"BizId"`
	RequestID string `json:"RequestId"`
}

// specialURLQueryEscape 遵循阿里云 POP 规范对字符串进行百分号编码。
func specialURLQueryEscape(s string) string {
	escaped := url.QueryEscape(s)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	escaped = strings.ReplaceAll(escaped, "*", "%2A")
	escaped = strings.ReplaceAll(escaped, "%7E", "~")
	return escaped
}

// signRequest 计算阿里云 POP RPC HMAC-SHA1 请求签名。
func signRequest(httpMethod string, params map[string]string, secret string) string {
	keys := slices.Sorted(maps.Keys(params))
	canonicalizedParts := lo.Map(keys, func(k string, _ int) string {
		return specialURLQueryEscape(k) + "=" + specialURLQueryEscape(params[k])
	})
	canonicalizedQueryString := strings.Join(canonicalizedParts, "&")

	stringToSign := httpMethod + "&" + specialURLQueryEscape("/") + "&" + specialURLQueryEscape(canonicalizedQueryString)
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Send 执行阿里云短信发送。
func (d *Driver) Send(ctx context.Context, msg *sms.Message) error {
	if msg.PhoneNumber == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("aliyun sms: phone number is required")
	}

	signName := msg.ResolveSign(d.Name(), d.cfg.SignName)
	if signName == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("aliyun sms: sign_name is required")
	}

	templateCode := msg.ResolveTemplate(d.Name(), d.cfg.TemplateCode)
	if templateCode == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("aliyun sms: template_code is required")
	}

	nonce, err := uuid.NewV7()
	if err != nil {
		return oops.Code(errs.CodeInternal).In("AliyunDriver.NewV7").Wrap(err)
	}

	params := map[string]string{
		"Action":           "SendSms",
		"Version":          "2017-05-25",
		"Format":           "JSON",
		"PhoneNumbers":     msg.PhoneNumber,
		"SignName":         signName,
		"TemplateCode":     templateCode,
		"AccessKeyId":      d.cfg.AccessKeyID,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureVersion": "1.0",
		"SignatureNonce":   nonce.String(),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}

	if len(msg.TemplateParam) > 0 {
		paramBytes, err := json.Marshal(msg.TemplateParam)
		if err != nil {
			return oops.Code(errs.CodeInternal).In("AliyunDriver.MarshalParam").Wrap(err)
		}
		params["TemplateParam"] = string(paramBytes)
	}

	signature := signRequest("POST", params, d.cfg.AccessKeySecret)
	params["Signature"] = signature

	var res aliyunSendSmsResponse
	resp, err := d.client.R().
		SetContext(ctx).
		SetFormData(params).
		SetResult(&res).
		Post("")

	if err != nil {
		return oops.Code(errs.CodeInternal).In("AliyunDriver.Send").Wrap(err)
	}

	if resp.IsError() {
		return oops.Code(errs.CodeAliyunHTTPError).
			With("status", resp.StatusCode()).
			Errorf("aliyun sms http request failed with status: %d", resp.StatusCode())
	}

	if res.Code != "OK" {
		return oops.Code(errs.CodeAliyunAPIError).
			With("code", res.Code).
			With("message", res.Message).
			With("request_id", res.RequestID).
			Errorf("aliyun sms delivery failed: %s (code: %s)", res.Message, res.Code)
	}

	return nil
}
