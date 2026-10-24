// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与加密工具。
package realtime

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// CentrifugoSignatureHeader 定义 Centrifugo 代理请求携带的签名请求头。
const CentrifugoSignatureHeader = "X-Centrifugo-Signature"

// CentrifugoSignatureLegacyHeader 兼容早期版本 Centrifugo 代理请求签名头。
const CentrifugoSignatureLegacyHeader = "X-Signature"

// VerifySignature 校验 Centrifugo 代理请求的 HMAC-SHA256 签名。
// 空密钥或空签名一律判定失败（fail-closed）：缺少密钥即无从鉴别调用方身份，
// 放行等于把订阅鉴权端点敞开；配置层已保证启用 realtime 时密钥非空。
func VerifySignature(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))

	return subtle.ConstantTimeCompare([]byte(signature), []byte(expectedMAC)) == 1
}
