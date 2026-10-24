// Package hasher_test 验证 argon2id 口令哈希的外部行为。
package hasher_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/hasher"
)

// TestMakeAndCheck 校验口令往返成立，且同一口令两次哈希结果不同（盐值随机）。
func TestMakeAndCheck(t *testing.T) {
	h := hasher.New()

	first, err := h.Make("secret12345")
	require.NoError(t, err)
	second, err := h.Make("secret12345")
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "盐值必须随机")
	assert.True(t, strings.HasPrefix(first, "$argon2id$"), "应使用 PHC 编码串: %s", first)

	ok, err := h.Check(first, "secret12345")
	require.NoError(t, err)
	assert.True(t, ok)

	wrong, err := h.Check(first, "secret12346")
	require.NoError(t, err)
	assert.False(t, wrong)
}

// TestCheckRejectsMalformedHash 校验损坏的编码串返回错误而非静默的 false。
func TestCheckRejectsMalformedHash(t *testing.T) {
	h := hasher.New()

	for _, encoded := range []string{
		"",
		"plain",
		"$argon2id$v=19$m=1$bad",
		"$bcrypt$v=19$m=1,t=1,p=1$YWJj$ZGVm",
	} {
		_, err := h.Check(encoded, "secret12345")
		assert.Error(t, err, "编码串 %q 应被拒绝", encoded)
	}
}

// TestCheckRejectsIncompatibleVersion 校验 argon2 版本不匹配被显式拒绝。
func TestCheckRejectsIncompatibleVersion(t *testing.T) {
	h := hasher.New()

	_, err := h.Check("$argon2id$v=9$m=65536,t=3,p=4$YWJj$ZGVm", "secret12345")
	assert.ErrorIs(t, err, hasher.ErrIncompatibleVersion)
}

// TestDefaultParams 校验成本参数与 CONTEXT.md 记录的约定一致。
func TestDefaultParams(t *testing.T) {
	params := hasher.DefaultParams()

	assert.Equal(t, uint32(64*1024), params.Memory, "64MiB")
	assert.Equal(t, uint32(3), params.Iterations)
	assert.Equal(t, uint8(4), params.Parallelism)
	assert.Equal(t, uint32(16), params.SaltLength)
	assert.Equal(t, uint32(32), params.KeyLength)
}

// TestUnicodePassword 校验非 ASCII 口令可正常往返。
func TestUnicodePassword(t *testing.T) {
	h := hasher.New()

	encoded, err := h.Make("口令密码1234")
	require.NoError(t, err)

	ok, err := h.Check(encoded, "口令密码1234")
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestQuickParamsAndNeedsRehash 验证轻量单测成本与参数落后升级检测。
func TestQuickParamsAndNeedsRehash(t *testing.T) {
	quick := hasher.NewWithParams(hasher.QuickParams())
	prod := hasher.New()

	encodedQuick, err := quick.Make("password123")
	require.NoError(t, err)

	// quick 实例对自身生成的参数认为不需要 rehash
	assert.False(t, quick.NeedsRehash(encodedQuick))

	// prod 实例对 quick 生成的低成本参数应当要求升级
	assert.True(t, prod.NeedsRehash(encodedQuick))

	// 损坏哈希或空哈希返回 true
	assert.True(t, prod.NeedsRehash("invalid-hash"))
}

// TestPasswordTooLong 验证超长密码拒绝，防御恶意 DoS 攻击。
func TestPasswordTooLong(t *testing.T) {
	h := hasher.NewWithParams(hasher.QuickParams())
	tooLong := strings.Repeat("a", hasher.MaxPasswordLength+1)

	_, err := h.Make(tooLong)
	assert.ErrorIs(t, err, hasher.ErrPasswordTooLong)
}

// TestBcryptAndManagerMigration 验证多驱动兼容与旧 Bcrypt 自动升级标记。
func TestBcryptAndManagerMigration(t *testing.T) {
	bcryptHasher := hasher.NewBcrypt(4) // 最小成本供单测快速执行
	argonHasher := hasher.NewWithParams(hasher.QuickParams())
	mgr := hasher.NewManager(argonHasher)

	// 1. 模拟存量系统的 Bcrypt 哈希
	bcryptEncoded, err := bcryptHasher.Make("mypassword")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(bcryptEncoded, "$2a$"))

	// 2. Manager 能够成功验证旧 Bcrypt 哈希
	matched, err := mgr.Check(bcryptEncoded, "mypassword")
	require.NoError(t, err)
	assert.True(t, matched)

	// 3. Manager 明确指出旧 Bcrypt 哈希需要升级
	assert.True(t, mgr.NeedsRehash(bcryptEncoded))

	// 4. 用户登录后重新生成新哈希（Argon2id）
	newArgonEncoded, err := mgr.Make("mypassword")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(newArgonEncoded, "$argon2id$"))

	// 5. 新 Argon2id 哈希不再需要升级
	assert.False(t, mgr.NeedsRehash(newArgonEncoded))
}
