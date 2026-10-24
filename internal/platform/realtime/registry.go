// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与协议定义。
package realtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/samber/oops"
)

// ChannelAuthorization 定义私有/成员频道的订阅授权回调函数签名。
type ChannelAuthorization func(ctx context.Context, user User, channel string, params map[string]string) error

// ChannelPublishPolicy 定义客户端直接向频道发布消息时的策略拦截函数签名。
type ChannelPublishPolicy func(ctx context.Context, user User, channel string, data json.RawMessage, params map[string]string) error

// RPCHandler 定义客户端通过 WebSocket/SSE 发起 RPC 调用的处理函数签名。
type RPCHandler func(ctx context.Context, user User, data json.RawMessage) (any, error)

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
	if isStaticPattern(pattern) {
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

func isStaticPattern(pattern string) bool {
	return !strings.Contains(pattern, "{") && !strings.Contains(pattern, "*")
}

// Registry 管理全量实时频道路由、发布策略与 RPC 远程调用处理映射。
type Registry struct {
	mu          sync.RWMutex
	once        sync.Once
	channels    *routeTable[ChannelAuthorization]
	publishes   *routeTable[ChannelPublishPolicy]
	rpcHandlers map[string]RPCHandler
}

// NewRegistry 实例化空的策略注册中心。
func NewRegistry() *Registry {
	return &Registry{
		channels:    newRouteTable[ChannelAuthorization](),
		publishes:   newRouteTable[ChannelPublishPolicy](),
		rpcHandlers: make(map[string]RPCHandler),
	}
}

// OnceDo 保证注册逻辑单次幂等执行。
func (r *Registry) OnceDo(fn func()) {
	if r == nil || fn == nil {
		return
	}
	r.once.Do(fn)
}

// IsPrivateChannel 检查频道是否属于包含私有/成员/状态频道专属前缀。
func IsPrivateChannel(channel string) bool {
	return strings.HasPrefix(channel, "private:") ||
		strings.HasPrefix(channel, "private-") ||
		strings.HasPrefix(channel, "presence:") ||
		strings.HasPrefix(channel, "presence-") ||
		strings.HasPrefix(channel, "$")
}

// Register 注册频道模式与对应的订阅鉴权策略。
func (r *Registry) Register(pattern string, auth ChannelAuthorization) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || auth == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels.add(pattern, auth)
}

// AllowPublic 声明公开广播频道白名单，任何客户端均可自由订阅（满足 Fail-Closed 下的显式放行要求）。
func (r *Registry) AllowPublic(patterns ...string) {
	if r == nil {
		return
	}
	for _, pattern := range patterns {
		r.Register(pattern, func(_ context.Context, _ User, _ string, _ map[string]string) error {
			return nil
		})
	}
}

// RegisterPublish 注册客户端向频道发布消息的授权策略。
func (r *Registry) RegisterPublish(pattern string, policy ChannelPublishPolicy) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || policy == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishes.add(pattern, policy)
}

// RegisterRPC 注册指定方法名称的 RPC 处理器。
func (r *Registry) RegisterRPC(method string, handler RPCHandler) {
	method = strings.TrimSpace(method)
	if method == "" || handler == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.rpcHandlers[method] = handler
}

// Authorize 针对给定用户和目标频道执行订阅授权判定（遵循 Fail-Closed 安全原则）。
func (r *Registry) Authorize(ctx context.Context, user User, channel string) error {
	if r == nil {
		return oops.
			Code("FORBIDDEN").
			In("Registry.Authorize").
			With("channel", channel).
			Errorf("realtime registry is not initialized")
	}

	r.mu.RLock()
	auth, params, found := r.channels.find(channel)
	r.mu.RUnlock()

	if !found {
		return oops.
			Code("FORBIDDEN").
			In("Registry.Authorize").
			With("channel", channel).
			Errorf("unauthorized channel subscription: channel %q is not registered", channel)
	}

	return auth(ctx, user, channel, params)
}

// AuthorizePublish 针对客户端直接发布消息执行策略判定（遵循 Fail-Closed 原则）。
func (r *Registry) AuthorizePublish(ctx context.Context, user User, channel string, data json.RawMessage) error {
	if r == nil {
		return oops.
			Code("FORBIDDEN").
			In("Registry.AuthorizePublish").
			With("channel", channel).
			Errorf("client publishing is disabled for channel: %s", channel)
	}

	r.mu.RLock()
	policy, params, found := r.publishes.find(channel)
	r.mu.RUnlock()

	if !found {
		return oops.
			Code("FORBIDDEN").
			In("Registry.AuthorizePublish").
			With("channel", channel).
			Errorf("client publishing is disabled for channel: %s", channel)
	}

	return policy(ctx, user, channel, data, params)
}

// HandleRPC 执行客户端请求的 RPC 方法。
func (r *Registry) HandleRPC(ctx context.Context, user User, method string, data json.RawMessage) (any, error) {
	if r == nil {
		return nil, oops.
			Code("BAD_REQUEST").
			In("Registry.HandleRPC").
			Errorf("unknown RPC method: %s", method)
	}

	r.mu.RLock()
	handler, ok := r.rpcHandlers[method]
	r.mu.RUnlock()

	if !ok {
		return nil, oops.
			Code("BAD_REQUEST").
			In("Registry.HandleRPC").
			Errorf("unknown RPC method: %s", method)
	}

	return handler(ctx, user, data)
}
