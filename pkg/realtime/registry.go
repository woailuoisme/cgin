// Package realtime 提供基于 Centrifugo 的高可用实时通信基础设施驱动与频道路由策略注册。
package realtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/samber/oops"

	"cgin/pkg/errs"
)

var (
	// ErrChannelNotRegistered 表示目标频道未注册订阅鉴权策略。
	ErrChannelNotRegistered = errors.New("realtime: channel is not registered")
	// ErrRegistryNotInitialized 表示频道注册表未初始化。
	ErrRegistryNotInitialized = errors.New("realtime: registry is not initialized")
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
			Wrap(ErrRegistryNotInitialized)
	}

	r.mu.RLock()
	auth, params, found := r.channels.find(channel)
	r.mu.RUnlock()

	if !found {
		return oops.
			Code(errs.CodeForbidden).
			In("Registry.Authorize").
			With("channel", channel).
			Wrap(fmt.Errorf("%w: channel %q", ErrChannelNotRegistered, channel))
	}

	return auth(ctx, user, channel, params)
}

// trieNode 表示分段路由前缀树的节点。
type trieNode[T any] struct {
	literal       string
	paramName     string
	children      map[string]*trieNode[T]
	paramChild    *trieNode[T]
	wildcardChild *trieNode[T]
	handler       T
	hasHandler    bool
}

func newTrieNode[T any](literal string) *trieNode[T] {
	return &trieNode[T]{
		literal:  literal,
		children: make(map[string]*trieNode[T]),
	}
}

// trie 为分段前缀树，提供 O(Segments) 确定性路由与零正则回溯开销。
type trie[T any] struct {
	root *trieNode[T]
}

func newTrie[T any]() *trie[T] {
	return &trie[T]{
		root: newTrieNode[T](""),
	}
}

func isDelim(s string) bool {
	return len(s) == 1 && (s[0] == ':' || s[0] == '.' || s[0] == '/' || s[0] == '#')
}

// splitTokens 将模式或频道名按分隔符拆解为 Token 序列（保留分隔符作为独立 Token）。
func splitTokens(s string) []string {
	var tokens []string
	start := 0
	for i := range len(s) {
		ch := s[i]
		if ch == ':' || ch == '.' || ch == '/' || ch == '#' {
			if i > start {
				tokens = append(tokens, s[start:i])
			}
			tokens = append(tokens, string(ch))
			start = i + 1
		}
	}
	if start < len(s) {
		tokens = append(tokens, s[start:])
	}
	return tokens
}

// insert 向前缀树插入模式及对应的处理器。
func (t *trie[T]) insert(pattern string, handler T) {
	tokens := splitTokens(pattern)
	curr := t.root
	for _, tok := range tokens {
		switch {
		case tok == "*":
			if curr.wildcardChild == nil {
				curr.wildcardChild = newTrieNode[T]("*")
			}
			curr = curr.wildcardChild
		case strings.HasPrefix(tok, "{") && strings.HasSuffix(tok, "}"):
			paramName := tok[1 : len(tok)-1]
			if curr.paramChild == nil {
				curr.paramChild = newTrieNode[T](tok)
				curr.paramChild.paramName = paramName
			}
			curr = curr.paramChild
		default:
			child, exists := curr.children[tok]
			if !exists {
				child = newTrieNode[T](tok)
				curr.children[tok] = child
			}
			curr = child
		}
		if tok == "*" {
			break // * 为终态通配符，贪婪匹配后续所有分段
		}
	}
	curr.handler = handler
	curr.hasHandler = true
}

// match 在前缀树中匹配目标频道，返回处理器与捕获的参数字典。
func (t *trie[T]) match(channel string) (T, map[string]string, bool) {
	tokens := splitTokens(channel)
	params := make(map[string]string)
	handler, found := matchNode(t.root, tokens, 0, params)
	if !found {
		var zero T
		return zero, nil, false
	}
	if len(params) == 0 {
		return handler, nil, true
	}
	return handler, params, true
}

// matchNode 递归匹配 token 链；params 是就地累积的出参（路径参数在递归中逐步补齐），
// 因此不改成返回值——每层递归各建一份 map 会在路由热路径上多出无谓分配。
func matchNode[T any](curr *trieNode[T], tokens []string, index int, params map[string]string) (T, bool) {
	if curr == nil {
		var zero T
		return zero, false
	}

	// 1. 若当前节点为通配符节点，直接匹配剩余所有 Token
	if curr.literal == "*" {
		if curr.hasHandler {
			return curr.handler, true
		}
	}

	// 2. Token 已全部消耗完毕
	if index == len(tokens) {
		if curr.hasHandler {
			return curr.handler, true
		}
		// 若当前节点挂载通配符子节点，可匹配空剩余项
		if curr.wildcardChild != nil && curr.wildcardChild.hasHandler {
			return curr.wildcardChild.handler, true
		}
		var zero T
		return zero, false
	}

	tok := tokens[index]

	// 3. 优先级 1: 精确字面匹配
	if child, ok := curr.children[tok]; ok {
		if h, found := matchNode(child, tokens, index+1, params); found {
			return h, true
		}
	}

	// 4. 优先级 2: 动态参数段匹配（仅匹配非分隔符文本段）
	if curr.paramChild != nil && !isDelim(tok) {
		if h, found := matchNode(curr.paramChild, tokens, index+1, params); found {
			if params != nil {
				params[curr.paramChild.paramName] = tok
			}
			return h, true
		}
	}

	// 5. 优先级 3: 通配符子节点兜底
	if curr.wildcardChild != nil {
		if curr.wildcardChild.hasHandler {
			return curr.wildcardChild.handler, true
		}
	}

	var zero T
	return zero, false
}
