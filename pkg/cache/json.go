package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// GetJSON 从 Store 中读取并反序列化 JSON 结构体对象；键不存在时返回 ErrNotFound。
func GetJSON[T any](ctx context.Context, s Store, key string) (T, error) {
	var zero T
	str, err := s.Get(ctx, key)
	if err != nil {
		return zero, err
	}
	var val T
	if err := json.Unmarshal([]byte(str), &val); err != nil {
		return zero, fmt.Errorf("cache: unmarshal json for key %s failed: %w", key, err)
	}
	return val, nil
}

// SetJSON 将任意 Go 结构体对象序列化为 JSON 写入 Store。
func SetJSON[T any](ctx context.Context, s Store, key string, value T, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache: marshal json for key %s failed: %w", key, err)
	}
	return s.Set(ctx, key, string(b), ttl)
}

// RememberJSON 实现泛型对象的 cache-aside 模式，结合 singleflight 防止高并发缓存击穿。
func RememberJSON[T any](
	ctx context.Context,
	s Store,
	key string,
	ttl time.Duration,
	load func(context.Context) (T, error),
) (T, error) {
	var zero T
	val, err := GetJSON[T](ctx, s, key)
	if err == nil {
		return val, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return zero, err
	}

	str, err := s.Remember(ctx, key, ttl, func(fillCtx context.Context) (string, error) {
		loaded, err := load(fillCtx)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(loaded)
		if err != nil {
			return "", fmt.Errorf("cache: marshal json failed in remember: %w", err)
		}
		return string(b), nil
	})
	if err != nil {
		return zero, err
	}

	var result T
	if err := json.Unmarshal([]byte(str), &result); err != nil {
		return zero, fmt.Errorf("cache: unmarshal json failed in remember: %w", err)
	}
	return result, nil
}
