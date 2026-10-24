// Package tencent 提供基于腾讯云短信服务 (SMS) 官方 TC3-HMAC-SHA256 签名协议的短信驱动实现。
package tencent

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/channel/sms"
)

const (
	defaultHost     = "sms.tencentcloudapi.com"
	defaultEndpoint = "https://sms.tencentcloudapi.com"
	defaultRegion   = "ap-guangzhou"
	defaultVersion  = "2021-01-11"
	defaultTimeout  = 10 * time.Second
)

// Config 腾讯云短信配置。
type Config struct {
	SecretID   string
	SecretKey  string
	AppID      string
	SignName   string
	TemplateID string
	Region     string
	Endpoint   string
	Timeout    time.Duration
}

// Driver 腾讯云短信驱动。
type Driver struct {
	cfg    Config
	client *resty.Client
}

var _ sms.Driver = (*Driver)(nil)

// NewDriver 创建腾讯云短信驱动实例。
func NewDriver(cfg Config) (*Driver, error) {
	if cfg.SecretID == "" || cfg.SecretKey == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("tencent sms: secret_id and secret_key are required")
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
	return "tencent"
}

type tencentSendSmsRequest struct {
	PhoneNumberSet   []string `json:"PhoneNumberSet"`
	SmsSdkAppID      string   `json:"SmsSdkAppId"`
	SignName         string   `json:"SignName"`
	TemplateID       string   `json:"TemplateId"`
	TemplateParamSet []string `json:"TemplateParamSet,omitempty"`
}

type tencentSendStatus struct {
	SerialNo    string `json:"SerialNo"`
	PhoneNumber string `json:"PhoneNumber"`
	Fee         int    `json:"Fee"`
	Code        string `json:"Code"`
	Message     string `json:"Message"`
}

type tencentError struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

type tencentResponseWrapper struct {
	Response struct {
		SendStatusSet []tencentSendStatus `json:"SendStatusSet"`
		RequestID     string              `json:"RequestId"`
		Error         *tencentError       `json:"Error,omitempty"`
	} `json:"Response"`
}

// hmacSHA256 计算 HMAC-SHA256 签名。
func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// sha256Hex 计算字符串的 SHA256 十六进制摘要。
func sha256Hex(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

// buildAuthorization 计算腾讯云 V3 签名 Header。
func buildAuthorization(secretID, secretKey, action string, timestamp int64, dateStr string, payload string) string {
	canonicalURI := "/"
	canonicalQueryString := ""
	canonicalHeaders := fmt.Sprintf("content-type:application/json; charset=utf-8\nhost:%s\nx-tc-action:%s\n", defaultHost, strings.ToLower(action))
	signedHeaders := "content-type;host;x-tc-action"
	hashedRequestPayload := sha256Hex(payload)

	canonicalRequest := fmt.Sprintf("POST\n%s\n%s\n%s\n%s\n%s",
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		hashedRequestPayload,
	)

	algorithm := "TC3-HMAC-SHA256"
	credentialScope := dateStr + "/sms/tc3_request"
	hashedCanonicalRequest := sha256Hex(canonicalRequest)

	stringToSign := fmt.Sprintf("%s\n%d\n%s\n%s",
		algorithm,
		timestamp,
		credentialScope,
		hashedCanonicalRequest,
	)

	secretDate := hmacSHA256([]byte("TC3"+secretKey), dateStr)
	secretService := hmacSHA256(secretDate, "sms")
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))

	return fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm,
		secretID,
		credentialScope,
		signedHeaders,
		signature,
	)
}

// Send 执行腾讯云短信发送。
func (d *Driver) Send(ctx context.Context, msg *sms.Message) error {
	if msg.PhoneNumber == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("tencent sms: phone number is required")
	}

	// 格式化手机号为国际 E.164 规范（如国内手机号加 +86）
	formattedPhone := msg.PhoneNumber
	if !strings.HasPrefix(formattedPhone, "+") {
		formattedPhone = "+86" + formattedPhone
	}

	signName := msg.ResolveSign(d.Name(), d.cfg.SignName)
	if signName == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("tencent sms: sign_name is required")
	}

	templateID := msg.ResolveTemplate(d.Name(), d.cfg.TemplateID)
	if templateID == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("tencent sms: template_id is required")
	}

	templateParamSet := msg.ResolveTencentParamSet()

	reqBody := tencentSendSmsRequest{
		PhoneNumberSet:   []string{formattedPhone},
		SmsSdkAppID:      d.cfg.AppID,
		SignName:         signName,
		TemplateID:       templateID,
		TemplateParamSet: templateParamSet,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return oops.Code(errs.CodeInternal).In("TencentDriver.Marshal").Wrap(err)
	}
	bodyStr := string(bodyBytes)

	now := time.Now().UTC()
	timestamp := now.Unix()
	dateStr := now.Format("2006-01-02")
	action := "SendSms"

	authHeader := buildAuthorization(d.cfg.SecretID, d.cfg.SecretKey, action, timestamp, dateStr, bodyStr)
	region := cmp.Or(d.cfg.Region, defaultRegion)

	var wrapper tencentResponseWrapper
	resp, err := d.client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json; charset=utf-8").
		SetHeader("Host", defaultHost).
		SetHeader("X-TC-Action", action).
		SetHeader("X-TC-Timestamp", strconv.FormatInt(timestamp, 10)).
		SetHeader("X-TC-Version", defaultVersion).
		SetHeader("X-TC-Region", region).
		SetHeader("Authorization", authHeader).
		SetBody(bodyStr).
		SetResult(&wrapper).
		Post("")

	if err != nil {
		return oops.Code(errs.CodeInternal).In("TencentDriver.Send").Wrap(err)
	}

	if resp.IsError() {
		return oops.Code(errs.CodeTencentHTTPError).
			With("status", resp.StatusCode()).
			Errorf("tencent sms http request failed with status: %d", resp.StatusCode())
	}

	if wrapper.Response.Error != nil {
		return oops.Code(errs.CodeTencentAPIError).
			With("code", wrapper.Response.Error.Code).
			With("message", wrapper.Response.Error.Message).
			With("request_id", wrapper.Response.RequestID).
			Errorf("tencent sms request error: %s (code: %s)", wrapper.Response.Error.Message, wrapper.Response.Error.Code)
	}

	if len(wrapper.Response.SendStatusSet) > 0 {
		status := wrapper.Response.SendStatusSet[0]
		if strings.ToLower(status.Code) != "ok" {
			// 不把收件手机号写进错误上下文：它会随错误链落入日志。
			return oops.Code(errs.CodeTencentDeliveryFailed).
				With("code", status.Code).
				With("message", status.Message).
				Errorf("tencent sms delivery failed: %s (code: %s)", status.Message, status.Code)
		}
	}

	return nil
}
