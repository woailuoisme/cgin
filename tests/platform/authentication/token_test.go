// Package auth_test 验证 JWT 双令牌的签发与校验契约（ADR-0001）。
package authentication_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/authentication"
	"bgin/internal/platform/errs"
)

// newIssuer 构造不依赖 Redis 的签发器，仅用于签发与校验路径。
func newIssuer(secret, issuer string, accessTTL time.Duration) *authentication.TokenIssuer {
	return authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     secret,
		Issuer:     issuer,
		AccessTTL:  accessTTL,
		RefreshTTL: 24 * time.Hour,
	}, nil)
}

// TestIssueAndParse 校验双令牌形态不同，且访问令牌载荷完整。
func TestIssueAndParse(t *testing.T) {
	issuer := newIssuer(testSecret, "bgin-test", 15*time.Minute)

	pair, err := issuer.Issue("u_1", "admin")
	require.NoError(t, err)
	assert.NotEqual(t, pair.AccessToken, pair.RefreshToken, "两类令牌必须不同")

	claims, err := issuer.Parse(pair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "u_1", claims.Subject)
	assert.Equal(t, "admin", claims.Role)
	assert.Equal(t, authentication.TypeAccess, claims.Type)
	assert.NotEmpty(t, claims.ID, "每枚令牌都要有唯一 jti 才能独立撤销")
	assert.True(t, pair.AccessExpiresAt.Before(pair.RefreshExpiresAt), "访问令牌寿命必须短于刷新令牌")
}

// TestParseRejectsCrossTypeUsage 校验刷新令牌不能当访问令牌用，反之亦然。
func TestParseRejectsCrossTypeUsage(t *testing.T) {
	issuer := newIssuer(testSecret, "bgin-test", 15*time.Minute)
	pair, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)

	_, err = issuer.Parse(pair.RefreshToken, authentication.TypeAccess)
	require.Error(t, err)

	_, err = issuer.Parse(pair.AccessToken, authentication.TypeRefresh)
	require.Error(t, err)
}

// TestParseRejectsForeignSecret 校验换密钥后旧令牌立即失效。
func TestParseRejectsForeignSecret(t *testing.T) {
	pair, err := newIssuer(testSecret, "bgin-test", 15*time.Minute).Issue("u_1", "user")
	require.NoError(t, err)

	other := newIssuer("ffffffffffffffffffffffffffffffff", "bgin-test", 15*time.Minute)
	_, err = other.Parse(pair.AccessToken, authentication.TypeAccess)
	assert.Error(t, err)
}

// TestParseRejectsForeignIssuer 校验签发方不匹配被拒绝。
func TestParseRejectsForeignIssuer(t *testing.T) {
	pair, err := newIssuer(testSecret, "bgin-test", 15*time.Minute).Issue("u_1", "user")
	require.NoError(t, err)

	other := newIssuer(testSecret, "someone-else", 15*time.Minute)
	_, err = other.Parse(pair.AccessToken, authentication.TypeAccess)
	assert.Error(t, err)
}

// TestParseRejectsGarbage 校验畸形令牌被拒绝。
func TestParseRejectsGarbage(t *testing.T) {
	issuer := newIssuer(testSecret, "bgin-test", 15*time.Minute)

	for _, raw := range []string{"", "not-a-jwt", "a.b.c"} {
		_, err := issuer.Parse(raw, authentication.TypeAccess)
		assert.Error(t, err, "令牌 %q 应被拒绝", raw)
	}
}

// TestExpiredTokenUsesDedicatedCode 校验过期令牌返回专门的错误码而非泛化的无效错误。
func TestExpiredTokenUsesDedicatedCode(t *testing.T) {
	issuer := newIssuer(testSecret, "bgin-test", -time.Minute)

	pair, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)

	_, err = issuer.Parse(pair.AccessToken, authentication.TypeAccess)
	require.Error(t, err)

	bizErr, ok := errs.As(err)
	require.True(t, ok, "应返回可映射为 HTTP 响应的业务错误")
	assert.Equal(t, errs.CodeAuthTokenExpired, bizErr.Code())
	assert.Equal(t, 401, errs.Status(bizErr.Code()))
}

// TestRoleClaimIsCarried 校验角色声明写入令牌，供鉴权中间件免查库使用（ADR-0003）。
func TestRoleClaimIsCarried(t *testing.T) {
	issuer := newIssuer(testSecret, "bgin-test", 15*time.Minute)

	pair, err := issuer.Issue("u_1", "admin")
	require.NoError(t, err)

	claims, err := issuer.Parse(pair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "admin", claims.Role)
}

// TestEachIssueHasUniqueJTI 校验每次签发的 jti 都不同，否则撤销会误伤其他令牌。
func TestEachIssueHasUniqueJTI(t *testing.T) {
	issuer := newIssuer(testSecret, "bgin-test", 15*time.Minute)

	first, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)
	second, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)

	assert.NotEqual(t, first.AccessToken, second.AccessToken)
}

// TestRevokeByJTI 校验根据 JTI 撤销单枚访问令牌。
func TestRevokeByJTI(t *testing.T) {
	store := newMockBlacklistStore()
	issuer := authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testSecret,
		Issuer:     "bgin-test",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}, store)

	ctx := context.Background()
	pair, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)

	claims, err := issuer.Parse(pair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)

	// 初始状态未被撤销
	revoked, err := issuer.IsTokenRevoked(ctx, claims)
	require.NoError(t, err)
	assert.False(t, revoked)

	// 撤销指定 JTI
	err = issuer.Revoke(ctx, claims)
	require.NoError(t, err)

	// 校验单项已撤销
	revoked, err = issuer.IsRevoked(ctx, claims.ID)
	require.NoError(t, err)
	assert.True(t, revoked)

	// 综合校验已撤销
	revoked, err = issuer.IsTokenRevoked(ctx, claims)
	require.NoError(t, err)
	assert.True(t, revoked)
}

// TestRevokeUserEpoch 校验用户全局注销纪元对存量令牌的批量失效与新令牌不受影响。
func TestRevokeUserEpoch(t *testing.T) {
	store := newMockBlacklistStore()
	issuer := authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testSecret,
		Issuer:     "bgin-test",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}, store)

	ctx := context.Background()

	// 签发两个用户的存量令牌
	userAPair, err := issuer.Issue("user_a", "user")
	require.NoError(t, err)
	userBPair, err := issuer.Issue("user_b", "user")
	require.NoError(t, err)

	claimsA, err := issuer.Parse(userAPair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)
	claimsB, err := issuer.Parse(userBPair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)

	// 初始状态均有效
	revokedA, err := issuer.IsTokenRevoked(ctx, claimsA)
	require.NoError(t, err)
	assert.False(t, revokedA)

	revokedB, err := issuer.IsTokenRevoked(ctx, claimsB)
	require.NoError(t, err)
	assert.False(t, revokedB)

	// 触发 user_a 的注销纪元
	err = issuer.RevokeUser(ctx, "user_a")
	require.NoError(t, err)

	// user_a 的存量令牌失效
	revokedA, err = issuer.IsTokenRevoked(ctx, claimsA)
	require.NoError(t, err)
	assert.True(t, revokedA)

	// user_b 的存量令牌不受任何影响
	revokedB, err = issuer.IsTokenRevoked(ctx, claimsB)
	require.NoError(t, err)
	assert.False(t, revokedB)
}

// TestJWTCodec_StatelessOperations 验证纯无状态 JWTCodec 独立编解码能力。
func TestJWTCodec_StatelessOperations(t *testing.T) {
	codec := authentication.NewJWTCodec([]byte(testSecret), "bgin-test", 15*time.Minute, 24*time.Hour)

	pair, err := codec.IssuePair("usr_100", "user", 10*time.Minute, time.Hour, map[string]any{
		"scope": "read:all",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)

	claims, err := codec.Parse(pair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr_100", claims.Subject)
	assert.Equal(t, "user", claims.Role)
	val, ok := claims.GetClaim("scope")
	assert.True(t, ok)
	assert.Equal(t, "read:all", val)

	// 载荷扁平化测试
	payload := claims.Payload()
	assert.Equal(t, "usr_100", payload["sub"])
	assert.Equal(t, "user", payload["role"])
	assert.Equal(t, "read:all", payload["scope"])
}

// TestTokenIssuer_AuthenticateToken 验证 TokenIssuer.AuthenticateToken 在有无黑名单下的解析与注入行为。
func TestTokenIssuer_AuthenticateToken(t *testing.T) {
	store := newMockBlacklistStore()
	issuer := authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testSecret,
		Issuer:     "bgin-test",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}, store)

	ctx := context.Background()
	pair, err := issuer.Issue("usr_auth", "admin")
	require.NoError(t, err)

	// 1. 正常令牌：认证通过并注入上下文
	authCtx, claims, err := issuer.AuthenticateToken(ctx, pair.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, "usr_auth", claims.Subject)

	id, ok := authentication.FromContext(authCtx)
	assert.True(t, ok)
	assert.Equal(t, "usr_auth", id.UserID)
	assert.Equal(t, "admin", id.Role)

	// 2. 撤销后令牌：认证失败（返回 CodeAuthTokenRevoked）
	err = issuer.Revoke(ctx, claims)
	require.NoError(t, err)

	_, _, err = issuer.AuthenticateToken(ctx, pair.AccessToken)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenRevoked, bizErr.Code())
}
