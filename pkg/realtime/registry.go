// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与协议定义。
package realtime

import (
	"context"
	"strings"
	"sync"

	"github.com/samber/oops"

	"cgin/pkg/errs"
)

// ChannelAuthorization 定义私有/成员频道的订阅授权回调函数签名。
type ChannelAuthorization func(ctx context.Context, user User, channel string, params map[string]string) error

type routeTable[T any] struct {
	exact map[string]T
	trie  *trie[T]
}

func newRouteTable[T any]() *routeTable[T] {
	return &routeTable[T]{
		exact: make(map[string]T),
		trie:  newTrie[T](),
	}
}

func (t *routeTable[T]) add(pattern string, handler T) {
	if !strings.Contains(pattern, "{") && !strings.Contains(pattern, "*") {
		t.exact[pattern] = handler
		return
	}
	t.trie.insert(pattern, handler)
}

func (t *routeTable[T]) find(channel string) (T, map[string]string, bool) {
	if h, ok := t.exact[channel]; ok {
		return h, nil, true
	}
	return t.trie.match(channel)
}

// Registry 管理全量实时频道的订阅授权策略（未登记的频道一律拒绝，Fail-Closed）。
type Registry struct {
	mu       sync.RWMutex
	channels *routeTable[ChannelAuthorization]
}

// NewRegistry 实例化空的策略注册中心。
func NewRegistry() *Registry {
	return &Registry{
		channels: newRouteTable[ChannelAuthorization](),
	}
}

// Register 注册频道模式与对应的订阅鉴权策略。支持 {param} 与 * 通配。
func (r *Registry) Register(pattern string, auth ChannelAuthorization) {
	pattern = strings.TrimSpace(pattern)
	if r == nil || pattern == "" || auth == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels.add(pattern, auth)
}

// Authorize 针对给定用户和目标频道执行订阅授权判定（遵循 Fail-Closed 安全原则）。
func (r *Registry) Authorize(ctx context.Context, user User, channel string) error {
	if r == nil {
		return oops.
			Code(errs.CodeForbidden).
			In("Registry.Authorize").
			With("channel", channel).
			Errorf("realtime registry is not initialized")
	}

	r.mu.RLock()
	auth, params, found := r.channels.find(channel)
	r.mu.RUnlock()

	if !found {
		return oops.
			Code(errs.CodeForbidden).
			In("Registry.Authorize").
			With("channel", channel).
			Errorf("unauthorized channel subscription: channel %q is not registered", channel)
	}

	return auth(ctx, user, channel, params)
}
