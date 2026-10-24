// Package smsbao 提供基于短信宝 (smsbao.com) 官方 HTTP API 的短信驱动实现。
package smsbao

import (
	"cmp"
	"context"
	"crypto/md5" //nolint:gosec // 短信宝官方 API 规范要求 MD5 哈希
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/channel/sms"
)

const (
	defaultEndpoint = "https://api.smsbao.com/sms"
	defaultTimeout  = 10 * time.Second
)

// Config 短信宝驱动配置。
type Config struct {
	Username string
	Password string // 原始密码或已 MD5 的密码哈希
	APIKey   string // 若配置了 APIKey 则优先作为密码使用
	SignName string // 默认短信签名
	Template string // 默认文本模板（支持 {sign}、{key} 占位符）
	Endpoint string
	Timeout  time.Duration
}

// Driver 短信宝驱动。
type Driver struct {
	cfg        Config
	passMD5Hex string
	client     *resty.Client
}

var _ sms.Driver = (*Driver)(nil)

// NewDriver 创建短信宝驱动实例。
func NewDriver(cfg Config) (*Driver, error) {
	if cfg.Username == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("smsbao: username is required")
	}

	secret := cmp.Or(cfg.APIKey, cfg.Password)
	if secret == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("smsbao: password or api_key is required")
	}

	// 计算 MD5 密码哈希（长度为 32 的十六进制直接认定为已哈希）
	passHash := secret
	if len(secret) != 32 {
		h := md5.Sum([]byte(secret)) //nolint:gosec // 短信宝官方 API 规范要求 MD5 哈希
		passHash = hex.EncodeToString(h[:])
	}

	endpoint := cmp.Or(cfg.Endpoint, defaultEndpoint)
	timeout := cmp.Or(cfg.Timeout, defaultTimeout)

	client := resty.New().
		SetBaseURL(endpoint).
		SetTimeout(timeout)

	return &Driver{
		cfg:        cfg,
		passMD5Hex: strings.ToLower(passHash),
		client:     client,
	}, nil
}

// Name 返回驱动标识名。
func (d *Driver) Name() string {
	return "smsbao"
}

// smsbaoErrorMessage 将短信宝响应状态码映射为清晰错误描述。
func smsbaoErrorMessage(code string) string {
	switch strings.TrimSpace(code) {
	case "0":
		return "success"
	case "30":
		return "password error"
	case "40":
		return "account does not exist"
	case "41":
		return "insufficient balance"
	case "42":
		return "account expired"
	case "43":
		return "ip address restricted"
	case "50":
		return "sensitive words contained"
	case "51":
		return "phone number is invalid"
	default:
		return "unknown error code: " + code
	}
}

// resolveContent 从短信载荷中解析最终文本内容（优先纯文本 -> 模板插值渲染 -> 验证码兜底）。
func (d *Driver) resolveContent(msg *sms.Message) string {
	if msg.Content != "" {
		return msg.Content
	}

	signName := msg.ResolveSign(d.Name(), d.cfg.SignName)
	templatePattern := msg.ResolveTemplate(d.Name(), d.cfg.Template)

	if templatePattern != "" {
		rendered := msg.RenderContent(templatePattern, signName)
		if rendered != "" {
			return rendered
		}
	}

	code := cmp.Or(msg.TemplateParam["code"], msg.TemplateParam["otp"])
	if code == "" && len(msg.ParamSet) > 0 {
		code = msg.ParamSet[0]
	}
	if code == "" {
		return ""
	}

	if signName != "" {
		return fmt.Sprintf("【%s】Your verification code is %s", signName, code)
	}
	return "Your verification code is " + code
}

// Send 执行短信发送。
func (d *Driver) Send(ctx context.Context, msg *sms.Message) error {
	phone := msg.PhoneNumber
	if phone == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("smsbao: phone number is required")
	}

	content := d.resolveContent(msg)
	if content == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("smsbao: sms content is required")
	}

	resp, err := d.client.R().
		SetContext(ctx).
		SetQueryParams(map[string]string{
			"u": d.cfg.Username,
			"p": d.passMD5Hex,
			"m": phone,
			"c": content,
		}).
		Get("")

	if err != nil {
		return oops.Code(errs.CodeInternal).In("SMSBaoDriver.Send").Wrap(err)
	}

	resCode := strings.TrimSpace(resp.String())
	if resCode != "0" {
		return oops.Code(errs.CodeSMSBaoSendFailed).
			With("code", resCode).
			With("phone", phone).
			Errorf("smsbao delivery failed: %s", smsbaoErrorMessage(resCode))
	}

	return nil
}
