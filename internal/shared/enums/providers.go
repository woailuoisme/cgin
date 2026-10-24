// Package enums 定义系统跨领域通用枚举与常量。
package enums

// ProviderID 代表认证凭证提供方领域枚举。
type ProviderID string

// Provider 为 ProviderID 的通用领域别名。
type Provider = ProviderID

// 凭证提供方常量定义。
const (
	// ProviderCredential 本地账密认证提供方。
	ProviderCredential ProviderID = "credential"
	// ProviderGoogle Google OAuth 第三方认证提供方。
	ProviderGoogle ProviderID = "google"
	// ProviderGitHub GitHub OAuth 第三方认证提供方。
	ProviderGitHub ProviderID = "github"
	// ProviderWeChatMiniProgram 微信小程序认证提供方。
	// 与未来可能出现的公众号网页授权刻意区分为两个提供方：微信的 openid 是
	// 应用级标识，同一个用户在公众号与小程序下得到的 openid 并不相同。
	ProviderWeChatMiniProgram ProviderID = "wechat-miniprogram"
)

// String 返回凭证提供方的字符串表现形式。
func (p ProviderID) String() string {
	return string(p)
}

// IsValid 检查凭证提供方是否合法。
func (p ProviderID) IsValid() bool {
	return p == ProviderCredential || p == ProviderGoogle || p == ProviderGitHub || p == ProviderWeChatMiniProgram
}
