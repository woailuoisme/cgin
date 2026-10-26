package wechat

import (
	"context"
	"time"
)

// AuthService 定义微信小程序身份与登录凭据校验契约 (ISP 细粒度接口)。
//
// 业务切片（如 client/auth）若仅处理小程序登录，可仅声明依赖此最小接口，无需感知其他子模块。
type AuthService interface {
	// Code2Session 用 wx.login() 返回的 code 校验并换取用户身份。
	Code2Session(ctx context.Context, code string) (*Session, error)
}

// PhoneService 定义微信小程序手机号快速验证契约 (ISP 细粒度接口)。
type PhoneService interface {
	// GetPhoneNumber 用小程序端 getPhoneNumber 回调返回的 code 换取用户绑定的手机号。
	GetPhoneNumber(ctx context.Context, code string) (*PhoneInfo, error)
}

// WxaCodeService 定义微信小程序码生成契约 (ISP 细粒度接口)。
type WxaCodeService interface {
	// GetUnlimited 生成数量不限的小程序码，返回 JPEG 字节流。
	GetUnlimited(ctx context.Context, params UnlimitedCodeParams) ([]byte, error)
}

// MiniProgramClient 是微信小程序客户端的聚合契约 (Facade 门面契约)。
//
// 该契约组合了各细粒度子服务契约，统一提供 AppID 标识与各子模块访问入口。
type MiniProgramClient interface {
	// AppID 返回该客户端绑定的小程序 AppID。
	AppID() string
	// Auth 获取认证服务接口。
	Auth() AuthService
	// Phone 获取手机号服务接口。
	Phone() PhoneService
	// WxaCode 获取小程序码服务接口。
	WxaCode() WxaCodeService
}

// TokenStore 定义 AccessToken 的外部分布式存储与排队锁契约。
type TokenStore interface {
	// Get 读取缓存中的 Token 凭据。
	Get(ctx context.Context, key string) (string, bool)
	// Set 写入 Token 凭据并指定有效期。
	Set(ctx context.Context, key string, token string, ttl time.Duration) error
	// Delete 丢弃指定的 Token 凭据。
	Delete(ctx context.Context, key string) error
	// Lock 尝试获取分布式回源锁，避免多节点并发击穿上游 API；
	// 获取成功时同时返回本次持有的唯一标识，供解锁时校验归属。
	Lock(ctx context.Context, key string, ttl time.Duration) (owner string, acquired bool, err error)
	// Unlock 释放分布式回源锁；仅当锁仍属于该持有者时才会删除，避免误删他人的锁。
	Unlock(ctx context.Context, key, owner string) error
}
