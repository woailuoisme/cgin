package payment

import (
	"cmp"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dromara/carbon/v2"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/payment/enums"
)

const (
	// channelTimeLayout 兼容部分渠道返回的「空格分隔」时间格式。
	channelTimeLayout = "2006-01-02 15:04:05"
	// maxChannelErrorLength 限制渠道错误原文进入日志的长度，避免超大报文冲垮日志。
	maxChannelErrorLength = 512
)

// resolveNotifyURL 返回生效的通知地址，请求未覆盖时回退到应用默认配置。
func resolveNotifyURL(override, fallback string) string {
	return cmp.Or(strings.TrimSpace(override), fallback)
}

// resolveCredentialPath 将凭据文件路径与证书根目录合并。
// 绝对路径原样返回；相对路径优先与 baseDir 拼接；若拼接后文件存在则返回拼接路径，否则若相对原路径文件存在则使用原路径。
func resolveCredentialPath(baseDir, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return trimmed
	}
	base := strings.TrimSpace(baseDir)
	if base == "" {
		return trimmed
	}
	candidate := filepath.Join(base, trimmed)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	if _, err := os.Stat(trimmed); err == nil {
		return trimmed
	}
	return candidate
}

// credentialField 描述待校验的凭据字段与其配置值。
type credentialField struct {
	Name  string
	Value string
}

// requireCredentials 按顺序校验必填凭据，遇到首个缺失项即报错，保证报错顺序确定。
func requireCredentials(channel, appKey string, fields []credentialField) error {
	for _, field := range fields {
		if strings.TrimSpace(field.Value) == "" {
			return errs.ServiceUnavailable(
				fmt.Sprintf("%s application %s is missing required credential: %s", channel, appKey, field.Name),
			)
		}
	}

	return nil
}

// readCredentialFile 读取证书或私钥文件内容。
// 凭据只配置路径，内容在启动期一次性读入内存，文件本身既不入库也不进版本库。
// 若路径为相对路径且传入了 baseDir，将自动以 baseDir 为基准目录补全。
func readCredentialFile(baseDir, path string) ([]byte, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, errs.ServiceUnavailable("payment credential file path is not configured")
	}

	resolved := resolveCredentialPath(baseDir, trimmed)
	content, err := os.ReadFile(resolved)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to read payment credential file %s: %w", resolved, err))
	}
	if len(content) == 0 {
		return nil, errs.ServiceUnavailable("payment credential file is empty: " + resolved)
	}

	return content, nil
}

// decodeRSAPublicKey 从 PEM 内容中解析 RSA 公钥，同时兼容证书与裸公钥两种封装。
// 使用标准库解析而非引入额外依赖，避免为一个十余行的操作扩大直接依赖面。
func decodeRSAPublicKey(content []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(content)
	if block == nil {
		return nil, errors.New("no pem block found in public key content")
	}

	if block.Type == "CERTIFICATE" {
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate: %w", err)
		}

		publicKey, ok := certificate.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("certificate does not carry an rsa public key")
		}

		return publicKey, nil
	}

	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %w", err)
	}

	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not an rsa key")
	}

	return publicKey, nil
}

// resolveScenes 解析渠道应用声明的支付场景集合，并校验场景归属于该渠道。
func resolveScenes(raw []string, channel enums.PaymentChannel) (map[enums.PaymentScene]struct{}, error) {
	if len(raw) == 0 {
		return nil, errs.BadRequest("payment application declares no scene: " + channel.String())
	}

	scenes := make(map[enums.PaymentScene]struct{}, len(raw))
	for _, item := range raw {
		scene, err := enums.PaymentSceneFromString(strings.TrimSpace(item))
		if err != nil {
			return nil, errs.BadRequest(
				fmt.Sprintf("invalid payment scene declared by application for channel %s: %s", channel.String(), item),
			)
		}
		if scene.Channel() != channel {
			return nil, errs.BadRequest(
				fmt.Sprintf("payment scene %s does not belong to channel %s", item, channel.String()),
			)
		}

		scenes[scene] = struct{}{}
	}

	return scenes, nil
}

// sortedScenes 把场景集合展开为按字典序排序的切片，保证对外暴露顺序稳定。
func sortedScenes(scenes map[enums.PaymentScene]struct{}) []enums.PaymentScene {
	return slices.Sorted(maps.Keys(scenes))
}

// validateCreate 校验收款创建请求的公共必填项与场景约束。
func validateCreate(req CreateRequest) error {
	if !req.Scene.IsValid() {
		return errs.BadRequest("invalid payment scene: " + req.Scene.String())
	}
	if strings.TrimSpace(req.OutTradeNo) == "" {
		return errs.BadRequest("out trade no is required")
	}
	if req.EffectiveAmount() <= 0 {
		return errs.BadRequest("payment amount must be positive")
	}
	if strings.TrimSpace(req.Subject) == "" {
		return errs.BadRequest("payment subject is required")
	}
	if req.Scene.RequiresPayerIdentity() && strings.TrimSpace(req.PayerID) == "" {
		return errs.BadRequest("payer identity is required for this payment scene: " + req.Scene.String())
	}

	return nil
}

// validateRefund 校验退款请求的必填项与金额关系。
func validateRefund(req RefundRequest) error {
	if strings.TrimSpace(req.OutTradeNo) == "" {
		return errs.BadRequest("out trade no is required")
	}
	if strings.TrimSpace(req.OutRefundNo) == "" {
		return errs.BadRequest("out refund no is required")
	}
	refundAmount := req.EffectiveRefundAmount()
	if refundAmount <= 0 {
		return errs.BadRequest("refund amount must be positive")
	}
	totalAmount := req.EffectiveTotalAmount()
	if totalAmount > 0 && refundAmount > totalAmount {
		return errs.BadRequest("refund amount must not exceed total amount")
	}

	return nil
}

// FormatYuanAmount 把以分为单位的金额格式化为人民币元字符串（如 12345 -> "123.45"）。
//
// 这是「渠道边界金额编解码器」的编码方向：部分渠道（支付宝）以元为单位传输金额，
// 而本项目内部一律以分为单位存储，跨边界时必须显式换算。
// 不复用 internal/shared/resource 的同名展示格式化函数，避免把 HTTP 响应层引入基础设施层。
func FormatYuanAmount(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}

	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// ParseYuanAmount 把人民币元字符串解析为以分为单位的整型金额，空串视为零。
//
// 这是「渠道边界金额编解码器」的解码方向。全程使用字符串与整数运算，绝不引入浮点误差；
// 小数位超过两位时按分向下取整，避免因渠道回传高精度尾数而产生不存在的分位。
func ParseYuanAmount(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}

	if strings.HasPrefix(trimmed, "-") {
		return 0, fmt.Errorf("negative yuan amount %q is not allowed", value)
	}

	integerPart, fractionPart, _ := strings.Cut(trimmed, ".")
	fractionPart = (fractionPart + "00")[:2]

	yuan, err := strconv.ParseInt(integerPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid yuan amount %q: %w", value, err)
	}

	fen, err := strconv.ParseInt(fractionPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid yuan fraction in %q: %w", value, err)
	}

	return yuan*100 + fen, nil
}

// channelTimeOrNow 解析渠道时间字符串，缺失或无法解析时回退为当前 UTC 时间。
func channelTimeOrNow(value string) time.Time {
	if parsed, ok := parseChannelTime(value); ok {
		return parsed
	}

	return time.Now().UTC()
}

// optionalChannelTime 解析可缺失的渠道时间字符串，缺失或无法解析时返回空指针。
func optionalChannelTime(value string) *time.Time {
	if parsed, ok := parseChannelTime(value); ok {
		return &parsed
	}
	return nil
}

var channelTimeLayouts = []string{time.RFC3339, channelTimeLayout}

// parseChannelTime 解析渠道可能返回的多种时间格式。
//
// 多布局模糊解析交给 carbon（AGENTS §4.6：非标日期字符串解析统一用 carbon），
// 解析结果统一归一到 UTC 再落库比对。
func parseChannelTime(value string) (time.Time, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, false
	}

	parsed := carbon.ParseByLayouts(trimmed, channelTimeLayouts)
	if !parsed.IsValid() {
		return time.Time{}, false
	}

	return parsed.StdTime().UTC(), true
}

// truncateForLog 截断过长的渠道错误原文，防止日志被大报文冲垮。
func truncateForLog(value string) string {
	if len(value) <= maxChannelErrorLength {
		return value
	}

	return value[:maxChannelErrorLength] + "...(truncated)"
}

// upstreamUnavailable 统一包装渠道调用失败：错误码固定为上游不可用，
// 文案携带渠道前缀、应用标识与操作名，便于按应用维度定位问题。
func upstreamUnavailable(channel, appKey, operation string, cause error) error {
	return errs.Wrap(
		errs.CodeServiceUnavailable,
		fmt.Sprintf("%s request failed: application=%s, operation=%s", channel, appKey, operation),
		cause,
	)
}
