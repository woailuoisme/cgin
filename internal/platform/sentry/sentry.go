// Package sentry 提供面向 Sentry / GlitchTip 兼容服务的异常捕获、链路追踪与上下文桥接。
package sentry

import (
	"cmp"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/samber/lo"

	"bgin/internal/config"
)

var (
	initializedMu sync.RWMutex
	initialized   bool
)

// Init 依据配置初始化 Sentry / GlitchTip 客户端并返回优雅停机 Flush 闭包。
// 若未激活或未配置有效 DSN，则静默保持未初始化状态，返回空操作 Flush。
func Init(cfg config.SentryConfig, appName, appEnv, appVersion string) (func(), error) {
	if !cfg.IsEnabled() {
		return func() {}, nil
	}
	dsn := cfg.EffectiveDSN()
	if dsn == "" {
		return func() {}, nil
	}

	env := cmp.Or(cfg.Environment, appEnv)
	release := cmp.Or(cfg.Release, lo.Ternary(appVersion != "", appName+"@"+appVersion, appName))

	tracesRate := min(max(cfg.TracesSampleRate, 0), 1)

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      env,
		Release:          release,
		TracesSampleRate: tracesRate,
		Debug:            cfg.Debug,
		AttachStacktrace: true,
		BeforeSend:       sanitizeEvent,
	})
	if err != nil {
		return nil, err
	}

	initializedMu.Lock()
	initialized = true
	initializedMu.Unlock()

	flush := func() {
		Flush(2 * time.Second)
	}
	return flush, nil
}

// Flush 等待所有未完成的 Sentry 事件在指定超时时间内发送完毕。
func Flush(timeout time.Duration) {
	if IsInitialized() {
		sentry.Flush(timeout)
	}
}

// IsInitialized 检查 Sentry SDK 是否已成功初始化。
func IsInitialized() bool {
	initializedMu.RLock()
	defer initializedMu.RUnlock()
	return initialized
}

// sanitizeEvent 检查并脱敏事件中的敏感 Header 与 Extra 载荷，防止秘钥凭证外泄。
func sanitizeEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return nil
	}

	// 脱敏 HTTP 请求头
	if event.Request != nil && len(event.Request.Headers) > 0 {
		sensitiveHeaders := map[string]struct{}{
			"authorization":       {},
			"cookie":              {},
			"set-cookie":          {},
			"x-csrf-token":        {},
			"x-xsrf-token":        {},
			"proxy-authorization": {},
		}
		for k := range event.Request.Headers {
			if _, exists := sensitiveHeaders[strings.ToLower(k)]; exists {
				event.Request.Headers[k] = "[REDACTED]"
			}
		}
	}

	// 脱敏 Contexts 键值字典
	if len(event.Contexts) > 0 {
		sensitiveKeys := map[string]struct{}{
			"password":      {},
			"token":         {},
			"secret":        {},
			"access_token":  {},
			"refresh_token": {},
			"api_key":       {},
			"otp":           {},
		}
		for ctxName, ctxData := range event.Contexts {
			for k, v := range ctxData {
				if _, exists := sensitiveKeys[strings.ToLower(k)]; exists {
					event.Contexts[ctxName][k] = "[REDACTED]"
				} else if strVal, ok := v.(string); ok && len(strVal) > 2000 {
					event.Contexts[ctxName][k] = strVal[:2000] + "...[TRUNCATED]"
				}
			}
		}
	}

	return event
}

// CaptureException 捕获并向 Sentry / GlitchTip 发送异常对象。
func CaptureException(err error) *sentry.EventID {
	if !IsInitialized() || err == nil {
		return nil
	}
	return sentry.CaptureException(err)
}

// CaptureExceptionWithContext 优先提取 context 中的 Sentry Hub 捕获异常，保留请求级作用域与标签。
func CaptureExceptionWithContext(ctx context.Context, err error) *sentry.EventID {
	if !IsInitialized() || err == nil {
		return nil
	}
	if ctx != nil {
		if hub := sentry.GetHubFromContext(ctx); hub != nil {
			return hub.CaptureException(err)
		}
	}
	return sentry.CaptureException(err)
}

// CaptureMessage 捕获并向 Sentry / GlitchTip 发送纯文本诊断消息。
func CaptureMessage(msg string) *sentry.EventID {
	if !IsInitialized() || msg == "" {
		return nil
	}
	return sentry.CaptureMessage(msg)
}

// CaptureMessageWithContext 优先使用上下文中的 Sentry Hub 捕获诊断消息。
func CaptureMessageWithContext(ctx context.Context, msg string) *sentry.EventID {
	if !IsInitialized() || msg == "" {
		return nil
	}
	if ctx != nil {
		if hub := sentry.GetHubFromContext(ctx); hub != nil {
			return hub.CaptureMessage(msg)
		}
	}
	return sentry.CaptureMessage(msg)
}

// SetUser 为当前上下文绑定用户身份元信息（对标 Laravel Sentry::setUser）。
func SetUser(ctx context.Context, id, email, username, ip string) {
	if !IsInitialized() || ctx == nil {
		return
	}
	hub := cmp.Or(sentry.GetHubFromContext(ctx), sentry.CurrentHub())
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetUser(sentry.User{
			ID:        id,
			Email:     email,
			Username:  username,
			IPAddress: ip,
		})
	})
}

// SetTag 为当前上下文设置单条键值标签（例如 request_id）。
func SetTag(ctx context.Context, key, value string) {
	if !IsInitialized() || ctx == nil || key == "" || value == "" {
		return
	}
	hub := cmp.Or(sentry.GetHubFromContext(ctx), sentry.CurrentHub())
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag(key, value)
	})
}
