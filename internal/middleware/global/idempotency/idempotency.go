package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"cgin/internal/platform/errs"
	"cgin/internal/platform/logger"
)

const (
	// ctxKeyIsFromCache 标记当前响应是否由幂等缓存回放。
	ctxKeyIsFromCache = "cgin:idempotency:isFromCache"
	// ctxKeyWasPutToCache 标记当前响应是否已写入幂等缓存。
	ctxKeyWasPutToCache = "cgin:idempotency:wasPutToCache"
	// redactedKey 日志中替代真实幂等键的占位符。
	redactedKey = "[redacted]"
)

// IsFromCache 报告当前请求是否直接回放了幂等缓存。
func IsFromCache(c *gin.Context) bool {
	value, ok := c.Get(ctxKeyIsFromCache)
	return ok && value != nil
}

// WasPutToCache 报告当前请求的响应是否已写入幂等缓存。
func WasPutToCache(c *gin.Context) bool {
	value, ok := c.Get(ctxKeyWasPutToCache)
	if !ok {
		return false
	}
	if placed, isBool := value.(bool); isBool {
		return placed
	}
	return value != nil
}

// New 构造幂等中间件。
func New(config ...Config) gin.HandlerFunc {
	cfg := configDefault(config...)
	handler := &handler{cfg: cfg, keepHeader: newHeaderFilter(cfg.KeepResponseHeaders)}
	return handler.handle
}

// handler 持有单次构造时快照的配置，避免运行期读取可变的调用方状态。
type handler struct {
	cfg        Config
	keepHeader func(string) bool
}

// handle 执行完整的幂等处理流程。
func (h *handler) handle(c *gin.Context) {
	if h.cfg.Next != nil && h.cfg.Next(c) {
		c.Next()
		return
	}

	key := c.GetHeader(h.cfg.KeyHeader)
	if key == "" {
		c.Next()
		return
	}
	// 只复制一次：校验器可能保留入参，处理器改写请求头也不应影响已计算的键。
	key = strings.Clone(key)

	if err := h.cfg.KeyHeaderValidate(key); err != nil {
		if errors.Is(err, ErrInvalidIdempotencyKey) {
			err = errs.BadRequest(err.Error())
		}
		abortWithError(c, err)
		return
	}

	storageKey := key
	if h.cfg.KeyScope != nil {
		storageKey = scopedKey(h.cfg.KeyScope(c), key)
	}

	// 快速路径：无锁读缓存，命中直接回放。
	hit, err := h.replay(c, storageKey)
	if err != nil {
		h.abortInternal(c, "failed to write cached response at fastpath", err)
		return
	}
	if hit {
		return
	}

	// 相同幂等键串行化，避免并发重复执行。
	if err := h.cfg.Lock.Lock(c.Request.Context(), storageKey); err != nil {
		h.abortInternal(c, "failed to lock", err)
		return
	}
	defer h.unlock(c.Request.Context(), key, storageKey)

	// 锁内二次读：并发竞争的失败方在这里回放先完成者的响应。
	hit, err = h.replay(c, storageKey)
	if err != nil {
		h.abortInternal(c, "failed to write cached response while locked", err)
		return
	}
	if hit {
		return
	}

	capture := &responseBufferWriter{ResponseWriter: c.Writer, body: bytes.NewBuffer(nil)}
	c.Writer = capture

	c.Next()

	// 处理器经 c.Errors 表达失败：失败请求不写入缓存，允许客户端用同一幂等键重试。
	if len(c.Errors) > 0 {
		return
	}

	if h.persist(c, key, storageKey, snapshotResponse(c, capture, h.keepHeader)) {
		c.Set(ctxKeyWasPutToCache, true)
	}
}

// replay 读取并回放缓存的响应；返回是否命中。
func (h *handler) replay(c *gin.Context, storageKey string) (bool, error) {
	value, err := h.cfg.Storage.Get(c.Request.Context(), storageKey)
	if err != nil {
		return false, fmt.Errorf("failed to read response: %w", err)
	}
	if value == nil {
		return false, nil
	}

	var cached response
	if err := cached.unmarshal(value); err != nil {
		return false, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	c.Status(cached.StatusCode)
	for header, values := range cached.Headers {
		// 入库时可能由旧版本或另一套配置写入，读取侧必须再过滤一次。
		if !h.keepHeader(header) {
			continue
		}
		for _, value := range values {
			c.Writer.Header().Add(header, value)
		}
	}

	if len(cached.Body) != 0 {
		if _, err := c.Writer.Write(cached.Body); err != nil {
			return true, err
		}
	} else {
		c.Writer.WriteHeaderNow()
	}

	c.Set(ctxKeyIsFromCache, true)
	c.Abort()
	return true, nil
}

// persist 序列化并写入缓存；失败时记录日志并返回 false。
func (h *handler) persist(c *gin.Context, key, storageKey string, snapshot *response) bool {
	payload, err := snapshot.marshal()
	if err != nil {
		logger.FromContext(c.Request.Context()).Error("failed to marshal idempotency response",
			zap.String("key", h.maskKey(key)),
			zap.Error(err),
		)
		return false
	}

	if err := h.cfg.Storage.Set(c.Request.Context(), storageKey, payload, h.cfg.Lifetime); err != nil {
		// 响应已写给客户端，无法再改写状态码；记录错误并放弃缓存。
		logger.FromContext(c.Request.Context()).Error("failed to save idempotency response",
			zap.String("key", h.maskKey(key)),
			zap.Error(err),
		)
		return false
	}
	return true
}

// unlock 释放互斥锁；解锁失败只记录日志，不影响已完成的响应。
func (h *handler) unlock(ctx context.Context, key, storageKey string) {
	if err := h.cfg.Lock.Unlock(ctx, storageKey); err != nil {
		logger.FromContext(ctx).Error("failed to unlock idempotency key",
			zap.String("key", h.maskKey(key)),
			zap.Error(err),
		)
	}
}

// abortInternal 将内部故障转换为统一 500 并中止请求。
func (h *handler) abortInternal(c *gin.Context, stage string, err error) {
	abortWithError(c, errs.Internal(fmt.Errorf("%s: %w", stage, err)))
}

// maskKey 按配置决定日志中是否脱敏幂等键。
func (h *handler) maskKey(key string) string {
	if h.cfg.DisableValueRedaction {
		return key
	}
	return redactedKey
}

// snapshotResponse 捕获状态码、可回放响应头与响应体。
func snapshotResponse(c *gin.Context, capture *responseBufferWriter, keepHeader func(string) bool) *response {
	snapshot := &response{
		StatusCode: c.Writer.Status(),
		Body:       bytes.Clone(capture.body.Bytes()),
		Headers:    make(map[string][]string, len(c.Writer.Header())),
	}
	for header, values := range c.Writer.Header() {
		if !keepHeader(header) {
			continue
		}
		snapshot.Headers[header] = slices.Clone(values)
	}
	return snapshot
}

// newHeaderFilter 构造响应头回放过滤器：nil 表示保留全部但排除 Cookie，
// 非 nil 表示仅保留列出的响应头。
func newHeaderFilter(configured []string) func(string) bool {
	keepResponseHeaders := slices.Clone(configured)
	if keepResponseHeaders == nil {
		return func(header string) bool { return !isCookieHeader(header) }
	}
	return func(header string) bool {
		return slices.ContainsFunc(keepResponseHeaders, func(keep string) bool {
			return strings.EqualFold(header, keep)
		})
	}
}

// responseBufferWriter 将处理器写入的响应体同时写入缓存副本。
type responseBufferWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

// Write 透传响应并捕获正文。
func (w *responseBufferWriter) Write(data []byte) (int, error) {
	w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

// WriteString 透传响应并捕获正文。
func (w *responseBufferWriter) WriteString(value string) (int, error) {
	w.body.WriteString(value)
	return w.ResponseWriter.WriteString(value)
}

// abortWithError 记录错误并中止后续处理器，交由全局 ErrorHandler 渲染。
func abortWithError(c *gin.Context, err error) {
	_ = c.Error(err)
	c.Abort()
}

// isCookieHeader 判断响应头是否为设置 Cookie。
func isCookieHeader(name string) bool {
	return strings.EqualFold(name, "Set-Cookie") || strings.EqualFold(name, "Set-Cookie2")
}

// scopedKey 返回作用域与幂等键的定长摘要。
// 长度前缀保证拼接无歧义，定长输出适配有键长限制的存储。
func scopedKey(scope, key string) string {
	buffer := make([]byte, 0, 128)
	buffer = strconv.AppendInt(buffer, int64(len(scope)), 10)
	buffer = append(buffer, ':')
	buffer = append(buffer, scope...)
	buffer = append(buffer, key...)

	sum := sha256.Sum256(buffer)
	return hex.EncodeToString(sum[:])
}
