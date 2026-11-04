// Package authentication_test 验证 JWT 双令牌的签发、校验与吊销契约（ADR-0001）。
package authentication_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/config"
	"cgin/pkg/authentication"
	"cgin/pkg/errs"
)

func newIssuer(secret, issuer string, accessTTL time.Duration) *authentication.TokenIssuer {
	return authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     secret,
		Issuer:     issuer,
		AccessTTL:  accessTTL,
		RefreshTTL: 24 * time.Hour,
	}, nil)
}

func TestIssueAndParse(t *testing.T) {
	issuer := newIssuer(testSecret, "cgin-test", 15*time.Minute)

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

func TestIssueWithoutRefreshToken(t *testing.T) {
	issuer := newIssuer(testSecret, "cgin-test", 15*time.Minute)

	pair, err := issuer.IssueWithCustom("u_1", "user", 15*time.Minute, 0, nil, false)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.Empty(t, pair.RefreshToken, "关闭刷新令牌时不得签发 refresh token")
	assert.True(t, pair.RefreshExpiresAt.IsZero())

	claims, err := issuer.Parse(pair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "u_1", claims.Subject)

	defaultPair, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)
	assert.NotEmpty(t, defaultPair.AccessToken)
	assert.NotEmpty(t, defaultPair.RefreshToken)
}

func TestParseRejectsCrossTypeUsage(t *testing.T) {
	issuer := newIssuer(testSecret, "cgin-test", 15*time.Minute)
	pair, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)

	_, err = issuer.Parse(pair.RefreshToken, authentication.TypeAccess)
	require.Error(t, err)

	_, err = issuer.Parse(pair.AccessToken, authentication.TypeRefresh)
	require.Error(t, err)
}

func TestParseRejectsForeignSecret(t *testing.T) {
	pair, err := newIssuer(testSecret, "cgin-test", 15*time.Minute).Issue("u_1", "user")
	require.NoError(t, err)

	other := newIssuer("ffffffffffffffffffffffffffffffff", "cgin-test", 15*time.Minute)
	_, err = other.Parse(pair.AccessToken, authentication.TypeAccess)
	assert.Error(t, err)
}

func TestParseRejectsForeignIssuer(t *testing.T) {
	pair, err := newIssuer(testSecret, "cgin-test", 15*time.Minute).Issue("u_1", "user")
	require.NoError(t, err)

	other := newIssuer(testSecret, "someone-else", 15*time.Minute)
	_, err = other.Parse(pair.AccessToken, authentication.TypeAccess)
	assert.Error(t, err)
}

func TestParseRejectsGarbage(t *testing.T) {
	issuer := newIssuer(testSecret, "cgin-test", 15*time.Minute)

	for _, raw := range []string{"", "not-a-jwt", "a.b.c"} {
		_, err := issuer.Parse(raw, authentication.TypeAccess)
		assert.Error(t, err, "令牌 %q 应被拒绝", raw)
	}
}

func TestExpiredTokenUsesDedicatedCode(t *testing.T) {
	issuer := newIssuer(testSecret, "cgin-test", -time.Minute)

	pair, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)

	_, err = issuer.Parse(pair.AccessToken, authentication.TypeAccess)
	require.Error(t, err)

	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenExpired, bizErr.Code())
	assert.Equal(t, 401, errs.Status(bizErr.Code()))
}

func TestRoleClaimIsCarried(t *testing.T) {
	issuer := newIssuer(testSecret, "cgin-test", 15*time.Minute)

	pair, err := issuer.Issue("u_1", "admin")
	require.NoError(t, err)

	claims, err := issuer.Parse(pair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "admin", claims.Role)
}

func TestEachIssueHasUniqueJTI(t *testing.T) {
	issuer := newIssuer(testSecret, "cgin-test", 15*time.Minute)

	first, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)
	second, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)

	assert.NotEqual(t, first.AccessToken, second.AccessToken)
}

func TestRevokeByJTI(t *testing.T) {
	store := newMockBlacklistStore()
	issuer := authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testSecret,
		Issuer:     "cgin-test",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}, store)

	ctx := context.Background()
	pair, err := issuer.Issue("u_1", "user")
	require.NoError(t, err)

	claims, err := issuer.Parse(pair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)

	revoked, err := issuer.IsTokenRevoked(ctx, claims)
	require.NoError(t, err)
	assert.False(t, revoked)

	err = issuer.Revoke(ctx, claims)
	require.NoError(t, err)

	revoked, err = issuer.IsRevoked(ctx, claims.ID)
	require.NoError(t, err)
	assert.True(t, revoked)

	revoked, err = issuer.IsTokenRevoked(ctx, claims)
	require.NoError(t, err)
	assert.True(t, revoked)
}

func TestRevokeUserEpoch(t *testing.T) {
	store := newMockBlacklistStore()
	issuer := authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testSecret,
		Issuer:     "cgin-test",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}, store)

	ctx := context.Background()

	userAPair, err := issuer.Issue("user_a", "user")
	require.NoError(t, err)
	userBPair, err := issuer.Issue("user_b", "user")
	require.NoError(t, err)

	claimsA, err := issuer.Parse(userAPair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)
	claimsB, err := issuer.Parse(userBPair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)

	revokedA, err := issuer.IsTokenRevoked(ctx, claimsA)
	require.NoError(t, err)
	assert.False(t, revokedA)

	revokedB, err := issuer.IsTokenRevoked(ctx, claimsB)
	require.NoError(t, err)
	assert.False(t, revokedB)

	err = issuer.RevokeUser(ctx, "user_a")
	require.NoError(t, err)

	revokedA, err = issuer.IsTokenRevoked(ctx, claimsA)
	require.NoError(t, err)
	assert.True(t, revokedA)

	revokedB, err = issuer.IsTokenRevoked(ctx, claimsB)
	require.NoError(t, err)
	assert.False(t, revokedB)
}

func TestTokenIssuer_CustomClaimsAndPayload(t *testing.T) {
	issuer := newIssuer(testSecret, "cgin-test", 15*time.Minute)

	pair, err := issuer.IssueWithCustom("usr_100", "user", 10*time.Minute, time.Hour, map[string]any{
		"scope": "read:all",
	}, true)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)

	claims, err := issuer.Parse(pair.AccessToken, authentication.TypeAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr_100", claims.Subject)
	assert.Equal(t, "user", claims.Role)
	val, ok := claims.GetClaim("scope")
	assert.True(t, ok)
	assert.Equal(t, "read:all", val)

	payload := claims.Payload()
	assert.Equal(t, "usr_100", payload["sub"])
	assert.Equal(t, "user", payload["role"])
	assert.Equal(t, "read:all", payload["scope"])
}

func TestTokenIssuer_AuthenticateToken(t *testing.T) {
	store := newMockBlacklistStore()
	issuer := authentication.NewTokenIssuer(config.JWTConfig{
		Secret:     testSecret,
		Issuer:     "cgin-test",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}, store)

	ctx := context.Background()
	pair, err := issuer.Issue("usr_auth", "admin")
	require.NoError(t, err)

	authCtx, claims, err := issuer.AuthenticateToken(ctx, pair.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, "usr_auth", claims.Subject)

	id, ok := authentication.FromContext(authCtx)
	assert.True(t, ok)
	assert.Equal(t, "usr_auth", id.UserID)
	assert.Equal(t, "admin", id.Role)

	err = issuer.Revoke(ctx, claims)
	require.NoError(t, err)

	_, _, err = issuer.AuthenticateToken(ctx, pair.AccessToken)
	require.Error(t, err)
	bizErr, ok := errs.As(err)
	require.True(t, ok)
	assert.Equal(t, errs.CodeAuthTokenRevoked, bizErr.Code())
}
