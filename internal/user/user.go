package user

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
	"cgin/pkg/authentication"
	"cgin/pkg/errs"
)

// 唯一索引名，用于把冲突翻译成具体业务错误码。
const (
	indexUserEmail       = "idx_user_email"
	indexUserUsername    = "idx_user_username"
	indexUserPhoneNumber = "idx_user_phone_number"

	// uniqueViolation 是 PostgreSQL 的唯一约束冲突错误码。
	uniqueViolation = "23505"
)

// NormaliseEmail 统一邮箱大小写与首尾空白，与库中 idx_user_email 的口径一致。
func NormaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormaliseUsername 统一用户名大小写与首尾空白，与库中 idx_user_username 的口径一致。
func NormaliseUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// IsBanned 判定账号是否处于封禁状态，已过期的封禁视为解除。
func IsBanned(u *sqlc.User, now time.Time) bool {
	if u == nil || !u.Banned {
		return false
	}
	return u.BanExpires == nil || u.BanExpires.After(now)
}

// GuardState 校验用户的通用封禁与活跃状态。
func GuardState(u *sqlc.User) error {
	if u == nil {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "The token is invalid")
	}
	if IsBanned(u, time.Now().UTC()) || u.Status == enum.StatusBanned {
		return errs.New(errs.CodeAuthAccountBanned, "The account has been banned")
	}
	if u.Status != enum.StatusActive {
		return errs.Forbidden("The account is not active")
	}
	return nil
}

// Identity 把用户行投影为认证平台需要的身份值。
func Identity(u *sqlc.User) authentication.Identity {
	if u == nil {
		return authentication.Identity{}
	}
	return authentication.Identity{
		UserID: u.ID,
		Role:   string(u.Role),
		CustomClaims: map[string]any{
			"email_verified": u.EmailVerified,
		},
	}
}

// FieldCannotBeNull 用于库层 NOT NULL、因而不能接受显式 null 的字段。
// 这里只声明"违反 not_null 规则"，文案由渲染层按请求语言从 message/*/validation.toml 取。
func FieldCannotBeNull(field string) error {
	return errs.ValidationKeys(map[string][]errs.MessageKey{
		field: {{Key: "not_null"}},
	})
}

// MapUniqueConflict 把唯一索引冲突翻译成具体的业务错误码。
func MapUniqueConflict(err error) error {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pgErr.Code != uniqueViolation {
		return errs.Internal(err)
	}

	switch pgErr.ConstraintName {
	case indexUserEmail:
		return errs.New(errs.CodeAuthEmailAlreadyExists, "An account with this email address already exists")
	case indexUserUsername:
		return errs.New(errs.CodeAuthUsernameTaken, "The username has already been taken")
	case indexUserPhoneNumber:
		return errs.New(errs.CodeAuthPhoneNumberTaken, "The phone number has already been taken")
	default:
		return errs.Conflict("The user conflicts with an existing record")
	}
}

// EnsureUsernameAvailable 预判用户名是否已被他人占用。
// 这次查重只为给出友好错误，不构成并发保证：最终仲裁仍是唯一索引 + MapUniqueConflict。
func EnsureUsernameAvailable(ctx context.Context, db *database.DB, userID, username string) error {
	existing, err := db.Queries(ctx).GetUserByUsername(ctx, sqlc.GetUserByUsernameParams{Username: username})
	switch {
	case err == nil && existing.ID != userID:
		return errs.New(errs.CodeAuthUsernameTaken, "The username has already been taken")
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return errs.Internal(err)
	}
	return nil
}
