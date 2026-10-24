// 本文件锁定键的构造契约：命名空间只有一处定义，并且三处既有调用点的键形状不能漂移。
package cache_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"bgin/internal/platform/cache"
)

// TestKey 覆盖命名空间拼接的几种输入。
func TestKey(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{"不给片段时只返回命名空间", nil, "bgin"},
		{"单片段", []string{"session"}, "bgin:session"},
		{"多片段按序拼接", []string{"jwt", "revoked", "abc"}, "bgin:jwt:revoked:abc"},
		{"空片段保留分隔符", []string{"a", "", "b"}, "bgin:a::b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cache.Key(tt.parts...))
		})
	}
}

// TestKeyKeepsExistingShapes 钉住迁移前后的键完全一致。
//
// 这三处是应用里全部的真实用法：键名一旦漂移，在途的令牌撤销名单与限流计数会突然"失联"
// ——撤销记录查不到、限流配额从零开始，且都不会报错。
func TestKeyKeepsExistingShapes(t *testing.T) {
	assert.Equal(t, "bgin:jwt:revoked:jti-1",
		cache.Key("jwt", "revoked", "jti-1"))

	assert.Equal(t, "bgin:auth:otp-attempts:user@example.com",
		cache.Key("auth", "otp-attempts", "user@example.com"))

	assert.Equal(t, "bgin:ratelimit:sensitive:ip:127.0.0.1:12345",
		cache.Key("ratelimit", "sensitive", "ip:127.0.0.1", "12345"))
}
