package notification

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // 短信宝官方 API 规范要求 MD5 哈希
	"crypto/sha1" //nolint:gosec // 阿里云 POP 签名协议强制要求 HMAC-SHA1
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

const (
	defaultAliyunSMSEndpoint  = "https://dysmsapi.aliyuncs.com"
	defaultTencentSMSHost     = "sms.tencentcloudapi.com"
	defaultTencentSMSEndpoint = "https://sms.tencentcloudapi.com"
	defaultTencentSMSRegion   = "ap-guangzhou"
	defaultTencentSMSVersion  = "2021-01-11"
	defaultSMSBaoEndpoint     = "https://api.smsbao.com/sms"
	defaultSMSTimeout         = 10 * time.Second
)

// SMSMessage 表示标准的短信通知载荷。
type SMSMessage struct {
	PhoneNumber     string
	SignName        string
	TemplateCode    string
	TemplateParam   map[string]string
	Content         string
	DriverTemplates map[string]string
	DriverSigns     map[string]string
	ParamSet        []string
	ParamOrder      []string
}

// NewSMSMessage 创建初始化为空的短信消息对象。
func NewSMSMessage() *SMSMessage {
	return &SMSMessage{
		TemplateParam:   make(map[string]string),
		DriverTemplates: make(map[string]string),
		DriverSigns:     make(map[string]string),
	}
}

// WithPhone 设置接收手机号。
func (m *SMSMessage) WithPhone(phone string) *SMSMessage {
	m.PhoneNumber = phone
	return m
}

// WithSignName 设置通用短信签名。
func (m *SMSMessage) WithSignName(sign string) *SMSMessage {
	m.SignName = sign
	return m
}

// WithTemplate 设置通用模板 ID 或模板编码。
func (m *SMSMessage) WithTemplate(code string) *SMSMessage {
	m.TemplateCode = code
	return m
}

// WithParam 设置单项模板变量。
func (m *SMSMessage) WithParam(key, val string) *SMSMessage {
	if m.TemplateParam == nil {
		m.TemplateParam = make(map[string]string)
	}
	m.TemplateParam[key] = val
	return m
}

// WithParams 批量设置模板变量。
func (m *SMSMessage) WithParams(params map[string]string) *SMSMessage {
	if m.TemplateParam == nil {
		m.TemplateParam = make(map[string]string)
	}
	maps.Copy(m.TemplateParam, params)
	return m
}

// WithParamSet 设置有序模板变量列表（适用于腾讯云等位置参数驱动）。
func (m *SMSMessage) WithParamSet(paramSet ...string) *SMSMessage {
	m.ParamSet = slices.Clone(paramSet)
	return m
}

// WithParamOrder 设置从 TemplateParam 提取有序切片的键名优先顺序。
func (m *SMSMessage) WithParamOrder(keys ...string) *SMSMessage {
	m.ParamOrder = slices.Clone(keys)
	return m
}

// WithContent 设置纯文本短信正文。
func (m *SMSMessage) WithContent(content string) *SMSMessage {
	m.Content = content
	return m
}

// WithDriverTemplate 为指定驱动配置专属模板标识。
func (m *SMSMessage) WithDriverTemplate(driver, template string) *SMSMessage {
	if m.DriverTemplates == nil {
		m.DriverTemplates = make(map[string]string)
	}
	m.DriverTemplates[driver] = template
	return m
}

// WithDriverSign 为指定驱动配置专属签名。
func (m *SMSMessage) WithDriverSign(driver, sign string) *SMSMessage {
	if m.DriverSigns == nil {
		m.DriverSigns = make(map[string]string)
	}
	m.DriverSigns[driver] = sign
	return m
}

// WithAliyunTemplate 针对阿里云短信配置专属模板 Code 及可选参数。
func (m *SMSMessage) WithAliyunTemplate(code string, params ...map[string]string) *SMSMessage {
	m.WithDriverTemplate("aliyun", code)
	if len(params) > 0 && params[0] != nil {
		m.WithParams(params[0])
	}
	return m
}

// WithTencentTemplate 针对腾讯云短信配置专属模板 ID 及有序参数切片。
func (m *SMSMessage) WithTencentTemplate(templateID string, paramSet ...string) *SMSMessage {
	m.WithDriverTemplate("tencent", templateID)
	if len(paramSet) > 0 {
		m.WithParamSet(paramSet...)
	}
	return m
}

// WithSMSBaoTemplate 针对短信宝配置专属文本模板（支持 {key} 与 ${key} 占位符）。
func (m *SMSMessage) WithSMSBaoTemplate(template string) *SMSMessage {
	m.WithDriverTemplate("smsbao", template)
	return m
}

// ResolveTemplate 获取指定驱动对应的模板标识（优先级：厂商专属 -> 通用设置 -> 兜底默认值）。
func (m *SMSMessage) ResolveTemplate(driver string, fallback ...string) string {
	if m.DriverTemplates != nil {
		if t, ok := m.DriverTemplates[driver]; ok && t != "" {
			return t
		}
	}
	if m.TemplateCode != "" {
		return m.TemplateCode
	}
	for _, fb := range fallback {
		if fb != "" {
			return fb
		}
	}
	return ""
}

// ResolveSign 获取指定驱动对应的签名（优先级：厂商专属 -> 通用设置 -> 兜底默认值）。
func (m *SMSMessage) ResolveSign(driver string, fallback ...string) string {
	if m.DriverSigns != nil {
		if s, ok := m.DriverSigns[driver]; ok && s != "" {
			return s
		}
	}
	if m.SignName != "" {
		return m.SignName
	}
	for _, fb := range fallback {
		if fb != "" {
			return fb
		}
	}
	return ""
}

func extractSingleSMSParam(params map[string]string) ([]string, bool) {
	if len(params) == 1 {
		for _, val := range params {
			return []string{val}, true
		}
	}
	if code := cmp.Or(params["code"], params["otp"]); code != "" && len(params) <= 2 {
		if len(params) == 1 || (len(params) == 2 && params["code"] == params["otp"]) {
			return []string{code}, true
		}
	}
	return nil, false
}

func extractNumericSMSParams(params map[string]string) ([]string, bool) {
	type numPair struct {
		idx int
		val string
	}
	pairs := make([]numPair, 0, len(params))
	for k, v := range params {
		n, err := strconv.Atoi(k)
		if err != nil || n < 0 {
			return nil, false
		}
		pairs = append(pairs, numPair{idx: n, val: v})
	}
	if len(pairs) == 0 {
		return nil, false
	}

	slices.SortFunc(pairs, func(a, b numPair) int {
		return cmp.Compare(a.idx, b.idx)
	})
	return lo.Map(pairs, func(p numPair, _ int) string {
		return p.val
	}), true
}

// ResolveTencentParamSet 智能自适应推断腾讯云短信有序参数切片。
func (m *SMSMessage) ResolveTencentParamSet() []string {
	if len(m.ParamSet) > 0 {
		return slices.Clone(m.ParamSet)
	}
	if len(m.TemplateParam) == 0 {
		return nil
	}

	if len(m.ParamOrder) > 0 {
		var set []string
		for _, key := range m.ParamOrder {
			if val, ok := m.TemplateParam[key]; ok {
				set = append(set, val)
			}
		}
		if len(set) > 0 {
			return set
		}
	}

	if set, ok := extractSingleSMSParam(m.TemplateParam); ok {
		return set
	}
	if set, ok := extractNumericSMSParams(m.TemplateParam); ok {
		return set
	}

	keys := slices.Sorted(maps.Keys(m.TemplateParam))
	return lo.Map(keys, func(k string, _ int) string {
		return m.TemplateParam[k]
	})
}

// RenderContent 将模板字符串（支持 {key} 与 ${key} 占位符）与当前变量进行插值替换。
func (m *SMSMessage) RenderContent(template string, sign string) string {
	if template == "" {
		return ""
	}

	res := template
	if sign != "" {
		res = strings.ReplaceAll(res, "${sign}", sign)
		res = strings.ReplaceAll(res, "{sign}", sign)
	}
	for k, v := range m.TemplateParam {
		res = strings.ReplaceAll(res, "${"+k+"}", v)
		res = strings.ReplaceAll(res, "{"+k+"}", v)
	}
	paramSet := m.ResolveTencentParamSet()
	for i, val := range paramSet {
		idxStr := strconv.Itoa(i + 1)
		res = strings.ReplaceAll(res, "${"+idxStr+"}", val)
		res = strings.ReplaceAll(res, "{"+idxStr+"}", val)
	}
	return res
}

// SMSNotification 声明支持短信格式化的通知接口契约。
type SMSNotification interface {
	ToSMS(notifiable any) any
}

// SMSDriver 定义底层短信服务提供商（如阿里云、腾讯云、短信宝、日志驱动）的发送接口。
type SMSDriver interface {
	Name() string
	Send(ctx context.Context, msg *SMSMessage) error
}

// SMSChannel 短信通知渠道实现。
type SMSChannel struct {
	mu            sync.RWMutex
	drivers       map[string]SMSDriver
	defaultDriver string
	signName      string
	templateCode  string
}

var (
	_ Channel   = (*SMSChannel)(nil)
	_ Formatter = (*SMSChannel)(nil)
)

// NewSMSChannel 创建短信通知渠道实例。
func NewSMSChannel(defaultDriver string) *SMSChannel {
	return &SMSChannel{
		drivers:       make(map[string]SMSDriver),
		defaultDriver: defaultDriver,
	}
}

// WithDefaults 配置渠道默认短信签名与模板编码。
func (c *SMSChannel) WithDefaults(signName, templateCode string) *SMSChannel {
	c.signName = signName
	c.templateCode = templateCode
	return c
}

// Name 返回渠道标识名。
func (c *SMSChannel) Name() string {
	return "sms"
}

// RegisterDriver 注册短信底层传输驱动。
func (c *SMSChannel) RegisterDriver(name string, driver SMSDriver) *SMSChannel {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drivers[name] = driver
	if c.defaultDriver == "" {
		c.defaultDriver = name
	}
	return c
}

// Driver 获取指定驱动实例，若无传参则返回当前默认驱动。
func (c *SMSChannel) Driver(name ...string) SMSDriver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.drivers[cmp.Or(lo.FirstOr(name, ""), c.defaultDriver)]
}

// Format 将领域通知对象转换为短信载荷 *SMSMessage。
func (c *SMSChannel) Format(n Notification, notifiable any) (any, error) {
	if smsNotif, ok := n.(SMSNotification); ok {
		return toSMSMessage(smsNotif.ToSMS(notifiable))
	}

	if cf, ok := n.(ChannelFormatter); ok {
		if raw := cf.Format(c.Name(), notifiable); raw != nil {
			return toSMSMessage(raw)
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedSMS).
		With("notification", n).
		Errorf("notification does not implement SMSNotification or ChannelFormatter")
}

func toSMSMessage(raw any) (*SMSMessage, error) {
	switch v := raw.(type) {
	case *SMSMessage:
		return v, nil
	case SMSMessage:
		return &v, nil
	case string:
		return NewSMSMessage().WithContent(v), nil
	default:
		return nil, oops.Code(errs.CodeInvalidSMSMessage).Errorf("unsupported sms message payload type: %T", raw)
	}
}

// Send 执行短信渠道的通知发送逻辑。
func (c *SMSChannel) Send(ctx context.Context, notifiable any, message any) error {
	msg, ok := message.(*SMSMessage)
	if !ok {
		return oops.Code(errs.CodeBadRequest).Errorf("sms: expected *SMSMessage, got %T", message)
	}

	if msg.PhoneNumber == "" && notifiable != nil {
		if target := ToNotifiable(notifiable); target != nil {
			if phone := target.RouteNotificationFor("sms"); phone != "" {
				msg.WithPhone(phone)
			}
		}
	}

	if msg.PhoneNumber == "" {
		return oops.Code(errs.CodeSMSMissingPhone).Errorf("sms: no phone number found for notifiable")
	}

	if msg.SignName == "" {
		msg.SignName = c.signName
	}
	if msg.TemplateCode == "" {
		msg.TemplateCode = c.templateCode
	}

	driver := c.Driver()
	if driver == nil {
		return oops.Code(errs.CodeSMSDriverNotFound).Errorf("sms: default driver %s is not registered", c.defaultDriver)
	}

	return driver.Send(ctx, msg)
}

// SendOTP 发送一次性短信验证码（便捷直发方法）。
func (c *SMSChannel) SendOTP(ctx context.Context, phoneNumber string, code string) error {
	msg := NewSMSMessage().
		WithPhone(phoneNumber).
		WithSignName(c.signName).
		WithTemplate(c.templateCode).
		WithParam("code", code).
		WithParam("otp", code).
		WithContent(code)

	driver := c.Driver()
	if driver == nil {
		return oops.Code(errs.CodeSMSDriverNotFound).Errorf("sms: default driver %s is not registered", c.defaultDriver)
	}

	return driver.Send(ctx, msg)
}

// ============================================================================
// 1. Aliyun SMS Driver (阿里云 Dysmsapi POP RPC 驱动)
// ============================================================================

// AliyunSMSConfig 阿里云短信配置。
type AliyunSMSConfig struct {
	AccessKeyID     string
	AccessKeySecret string
	SignName        string
	TemplateCode    string
	Endpoint        string
	Timeout         time.Duration
}

// AliyunSMSDriver 阿里云短信驱动。
type AliyunSMSDriver struct {
	cfg    AliyunSMSConfig
	client *resty.Client
}

var _ SMSDriver = (*AliyunSMSDriver)(nil)

// NewAliyunSMSDriver 创建阿里云短信驱动实例。
func NewAliyunSMSDriver(cfg AliyunSMSConfig) (*AliyunSMSDriver, error) {
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("aliyun sms: access_key_id and access_key_secret are required")
	}

	endpoint := cmp.Or(cfg.Endpoint, defaultAliyunSMSEndpoint)
	timeout := cmp.Or(cfg.Timeout, defaultSMSTimeout)

	client := resty.New().
		SetBaseURL(endpoint).
		SetTimeout(timeout)

	return &AliyunSMSDriver{
		cfg:    cfg,
		client: client,
	}, nil
}

// Name 返回驱动标识名。
func (d *AliyunSMSDriver) Name() string {
	return "aliyun"
}

type aliyunSendSmsResponse struct {
	Code      string `json:"Code"`
	Message   string `json:"Message"`
	BizID     string `json:"BizId"`
	RequestID string `json:"RequestId"`
}

func specialAliyunURLQueryEscape(s string) string {
	escaped := url.QueryEscape(s)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	escaped = strings.ReplaceAll(escaped, "*", "%2A")
	escaped = strings.ReplaceAll(escaped, "%7E", "~")
	return escaped
}

func signAliyunSMSRequest(httpMethod string, params map[string]string, secret string) string {
	keys := slices.Sorted(maps.Keys(params))
	canonicalizedParts := lo.Map(keys, func(k string, _ int) string {
		return specialAliyunURLQueryEscape(k) + "=" + specialAliyunURLQueryEscape(params[k])
	})
	canonicalizedQueryString := strings.Join(canonicalizedParts, "&")

	stringToSign := httpMethod + "&" + specialAliyunURLQueryEscape("/") + "&" + specialAliyunURLQueryEscape(canonicalizedQueryString)
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Send 执行阿里云短信发送。
func (d *AliyunSMSDriver) Send(ctx context.Context, msg *SMSMessage) error {
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
		return oops.Code(errs.CodeInternal).In("AliyunSMSDriver.NewV7").Wrap(err)
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
			return oops.Code(errs.CodeInternal).In("AliyunSMSDriver.MarshalParam").Wrap(err)
		}
		params["TemplateParam"] = string(paramBytes)
	}

	params["Signature"] = signAliyunSMSRequest("POST", params, d.cfg.AccessKeySecret)

	var res aliyunSendSmsResponse
	resp, err := d.client.R().
		SetContext(ctx).
		SetFormData(params).
		SetResult(&res).
		Post("")
	if err != nil {
		return oops.Code(errs.CodeInternal).In("AliyunSMSDriver.Send").Wrap(err)
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

// ============================================================================
// 2. Tencent SMS Driver (腾讯云 TC3-HMAC-SHA256 短信驱动)
// ============================================================================

// TencentSMSConfig 腾讯云短信配置。
type TencentSMSConfig struct {
	SecretID   string
	SecretKey  string
	AppID      string
	SignName   string
	TemplateID string
	Region     string
	Endpoint   string
	Timeout    time.Duration
}

// TencentSMSDriver 腾讯云短信驱动。
type TencentSMSDriver struct {
	cfg    TencentSMSConfig
	client *resty.Client
}

var _ SMSDriver = (*TencentSMSDriver)(nil)

// NewTencentSMSDriver 创建腾讯云短信驱动实例。
func NewTencentSMSDriver(cfg TencentSMSConfig) (*TencentSMSDriver, error) {
	if cfg.SecretID == "" || cfg.SecretKey == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("tencent sms: secret_id and secret_key are required")
	}

	endpoint := cmp.Or(cfg.Endpoint, defaultTencentSMSEndpoint)
	timeout := cmp.Or(cfg.Timeout, defaultSMSTimeout)

	client := resty.New().
		SetBaseURL(endpoint).
		SetTimeout(timeout)

	return &TencentSMSDriver{
		cfg:    cfg,
		client: client,
	}, nil
}

// Name 返回驱动标识名。
func (d *TencentSMSDriver) Name() string {
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

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

func buildTencentAuthorization(secretID, secretKey, action string, timestamp int64, dateStr string, payload string) string {
	canonicalHeaders := fmt.Sprintf("content-type:application/json; charset=utf-8\nhost:%s\nx-tc-action:%s\n", defaultTencentSMSHost, strings.ToLower(action))
	signedHeaders := "content-type;host;x-tc-action"
	hashedRequestPayload := sha256Hex(payload)

	canonicalRequest := fmt.Sprintf("POST\n/\n\n%s\n%s\n%s",
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
func (d *TencentSMSDriver) Send(ctx context.Context, msg *SMSMessage) error {
	if msg.PhoneNumber == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("tencent sms: phone number is required")
	}

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

	reqBody := tencentSendSmsRequest{
		PhoneNumberSet:   []string{formattedPhone},
		SmsSdkAppID:      d.cfg.AppID,
		SignName:         signName,
		TemplateID:       templateID,
		TemplateParamSet: msg.ResolveTencentParamSet(),
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return oops.Code(errs.CodeInternal).In("TencentSMSDriver.Marshal").Wrap(err)
	}
	bodyStr := string(bodyBytes)

	now := time.Now().UTC()
	timestamp := now.Unix()
	dateStr := now.Format("2006-01-02")
	action := "SendSms"

	authHeader := buildTencentAuthorization(d.cfg.SecretID, d.cfg.SecretKey, action, timestamp, dateStr, bodyStr)
	region := cmp.Or(d.cfg.Region, defaultTencentSMSRegion)

	var wrapper tencentResponseWrapper
	resp, err := d.client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json; charset=utf-8").
		SetHeader("Host", defaultTencentSMSHost).
		SetHeader("X-TC-Action", action).
		SetHeader("X-TC-Timestamp", strconv.FormatInt(timestamp, 10)).
		SetHeader("X-TC-Version", defaultTencentSMSVersion).
		SetHeader("X-TC-Region", region).
		SetHeader("Authorization", authHeader).
		SetBody(bodyStr).
		SetResult(&wrapper).
		Post("")
	if err != nil {
		return oops.Code(errs.CodeInternal).In("TencentSMSDriver.Send").Wrap(err)
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
			return oops.Code(errs.CodeTencentDeliveryFailed).
				With("code", status.Code).
				With("message", status.Message).
				Errorf("tencent sms delivery failed: %s (code: %s)", status.Message, status.Code)
		}
	}

	return nil
}

// ============================================================================
// 3. SMSBao Driver (短信宝 HTTP API 驱动)
// ============================================================================

// SMSBaoConfig 短信宝驱动配置。
type SMSBaoConfig struct {
	Username string
	Password string // 原始密码或已 MD5 的密码哈希
	APIKey   string // 若配置了 APIKey 则优先作为密码使用
	SignName string // 默认短信签名
	Template string // 默认文本模板（支持 {sign}、{key} 占位符）
	Endpoint string
	Timeout  time.Duration
}

// SMSBaoDriver 短信宝驱动。
type SMSBaoDriver struct {
	cfg        SMSBaoConfig
	passMD5Hex string
	client     *resty.Client
}

var _ SMSDriver = (*SMSBaoDriver)(nil)

// NewSMSBaoDriver 创建短信宝驱动实例。
func NewSMSBaoDriver(cfg SMSBaoConfig) (*SMSBaoDriver, error) {
	if cfg.Username == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("smsbao: username is required")
	}

	secret := cmp.Or(cfg.APIKey, cfg.Password)
	if secret == "" {
		return nil, oops.Code(errs.CodeBadRequest).Errorf("smsbao: password or api_key is required")
	}

	passHash := secret
	if len(secret) != 32 {
		h := md5.Sum([]byte(secret)) //nolint:gosec // 短信宝官方 API 规范要求 MD5 哈希
		passHash = hex.EncodeToString(h[:])
	}

	endpoint := cmp.Or(cfg.Endpoint, defaultSMSBaoEndpoint)
	timeout := cmp.Or(cfg.Timeout, defaultSMSTimeout)

	client := resty.New().
		SetBaseURL(endpoint).
		SetTimeout(timeout)

	return &SMSBaoDriver{
		cfg:        cfg,
		passMD5Hex: strings.ToLower(passHash),
		client:     client,
	}, nil
}

// Name 返回驱动标识名。
func (d *SMSBaoDriver) Name() string {
	return "smsbao"
}

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

func (d *SMSBaoDriver) resolveContent(msg *SMSMessage) string {
	if msg.Content != "" {
		return msg.Content
	}

	signName := msg.ResolveSign(d.Name(), d.cfg.SignName)
	templatePattern := msg.ResolveTemplate(d.Name(), d.cfg.Template)

	if templatePattern != "" {
		if rendered := msg.RenderContent(templatePattern, signName); rendered != "" {
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

// Send 执行短信宝发送。
func (d *SMSBaoDriver) Send(ctx context.Context, msg *SMSMessage) error {
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

// ============================================================================
// 4. Log SMS Driver (开发调试结构化日志驱动)
// ============================================================================

// LogSMSDriver 结构化日志短信驱动。
type LogSMSDriver struct {
	logger *zerolog.Logger
}

var _ SMSDriver = (*LogSMSDriver)(nil)

// NewLogSMSDriver 创建日志短信驱动实例，未注入时回退全局默认日志器。
func NewLogSMSDriver(logs ...*zerolog.Logger) *LogSMSDriver {
	base := cmp.Or(append(logs, logger.Default())...)
	sub := base.With().Str("component", "sms_log_driver").Logger()
	return &LogSMSDriver{logger: &sub}
}

// Name 返回驱动标识名。
func (d *LogSMSDriver) Name() string {
	return "log"
}

// Send 记录短信发送的结构化日志。
func (d *LogSMSDriver) Send(_ context.Context, msg *SMSMessage) error {
	event := d.logger.Info().
		Str("phone", msg.PhoneNumber).
		Str("sign_name", msg.SignName).
		Str("template_code", msg.TemplateCode)
	if len(msg.TemplateParam) > 0 {
		event = event.Any("params", msg.TemplateParam)
	}
	if msg.Content != "" {
		event = event.Str("content", msg.Content)
	}
	event.Msg("sms notification logged via log driver")
	return nil
}
