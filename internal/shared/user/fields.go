package user

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/aarondl/opt/omitnull"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/samber/lo"
	"github.com/uptrace/bun"

	"bgin/internal/database/models"
	"bgin/internal/platform/errs"
	"bgin/internal/shared/enums"
)

// uniqueViolation 是 PostgreSQL 的唯一约束冲突错误码。
const uniqueViolation = "23505"

// 唯一索引名，用于把冲突翻译成具体错误码。
const (
	indexUserEmail       = "idx_user_email"
	indexUserUsername    = "idx_user_username"
	indexUserPhoneNumber = "idx_user_phone_number"
)

// NormaliseEmail 统一邮箱大小写与首尾空白，与库中 idx_user_email 的口径一致。
func NormaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormaliseUsername 统一用户名大小写与首尾空白，与库中 idx_user_username 的口径一致。
func NormaliseUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// FieldSet 是客户端与后台管理共用的可更新档案字段集合。
type FieldSet struct {
	Name            omitnull.Val[string]
	Image           omitnull.Val[string]
	Username        omitnull.Val[string]
	DisplayUsername omitnull.Val[string]
	PhoneNumber     omitnull.Val[string]
}

// AdminFieldSet 承载管理端具备完整权限的用户模型局部更新字段集合。
type AdminFieldSet struct {
	FieldSet
	EmailVerified omitnull.Val[bool]
	Role          omitnull.Val[enums.Role]
	Status        omitnull.Val[enums.Status]
	Banned        omitnull.Val[bool]
	BanReason     omitnull.Val[string]
	BanExpires    omitnull.Val[time.Time]
}

// ToPtr 将三态值在拥有具体值时转换为指针，缺省或置空时返回 nil。
func ToPtr[T any](v omitnull.Val[T]) *T {
	if val, ok := v.Get(); ok {
		return &val
	}
	return nil
}

// CollectFields 把档案三态字段翻译成待更新列。db 只用于确认用户名未被他人占用。
func CollectFields(ctx context.Context, db bun.IDB, userID string, patch FieldSet) (map[string]any, error) {
	fields := make(map[string]any, 5)

	if !patch.Name.IsUnset() {
		if patch.Name.IsNull() {
			return nil, FieldCannotBeNull("name")
		}
		fields["name"] = strings.TrimSpace(patch.Name.MustGet())
	}
	if !patch.Image.IsUnset() {
		SetNullable(fields, "image", patch.Image)
	}
	if !patch.PhoneNumber.IsUnset() {
		SetNullable(fields, "phone_number", patch.PhoneNumber)
	}
	if !patch.Username.IsUnset() {
		if err := setUsername(ctx, db, userID, fields, patch.Username); err != nil {
			return nil, err
		}
	}
	if !patch.DisplayUsername.IsUnset() {
		SetNullable(fields, "display_username", patch.DisplayUsername)
	}
	return fields, nil
}

// CollectAdminFields 把管理端三态更新请求转换为待更新字段映射，并同步协同封禁状态。
func CollectAdminFields(ctx context.Context, db bun.IDB, userID string, patch AdminFieldSet) (map[string]any, error) {
	fields, err := CollectFields(ctx, db, userID, patch.FieldSet)
	if err != nil {
		return nil, err
	}

	if patch.EmailVerified.IsValue() {
		fields["email_verified"] = patch.EmailVerified.MustGet()
	}
	if err := SetRequiredString(fields, "role", patch.Role); err != nil {
		return nil, err
	}
	if err := SetRequiredString(fields, "status", patch.Status); err != nil {
		return nil, err
	}
	if patch.Banned.IsValue() {
		fields["banned"] = patch.Banned.MustGet()
	}
	if !patch.BanReason.IsUnset() {
		SetNullable(fields, "ban_reason", patch.BanReason)
	}
	if !patch.BanExpires.IsUnset() {
		fields["ban_expires"] = NullableTime(patch.BanExpires)
	}

	ReconcileBanState(fields)
	return fields, nil
}

// SetRequiredString 处理库层 NOT NULL、因而不接受显式 null 的字符串或枚举列。
func SetRequiredString[T ~string](fields map[string]any, column string, value omitnull.Val[T]) error {
	if value.IsUnset() {
		return nil
	}
	if value.IsNull() {
		return FieldCannotBeNull(column)
	}
	fields[column] = string(value.MustGet())
	return nil
}

// NullableTime 把三态时间字段转换为可写入列的值，并统一归一到 UTC。
func NullableTime(value omitnull.Val[time.Time]) any {
	if t, ok := value.Get(); ok {
		return t.UTC()
	}
	return nil
}

// ReconcileBanState 保证 banned 布尔值与 status 状态相互协同一致。
func ReconcileBanState(fields map[string]any) {
	if status, ok := fields["status"].(string); ok && status == string(enums.StatusBanned) {
		fields["banned"] = true
	}
	if banned, ok := fields["banned"].(bool); ok && banned {
		fields["status"] = string(enums.StatusBanned)
	}
}

// ApplyUpdate 执行更新，并把"记录不存在"翻译为 404、唯一冲突翻译为具体错误码。
func ApplyUpdate(ctx context.Context, db bun.IDB, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}

	if err := models.UpdateUserFields(ctx, db, id, fields); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errs.NotFound("The requested user does not exist")
		}
		if _, ok := errs.As(err); ok {
			return err
		}
		return MapUniqueConflict(err)
	}
	return nil
}

// SetNullable 把三态字段写入列；显式 null 或空白串都落为 NULL。
func SetNullable(fields map[string]any, column string, value omitnull.Val[string]) {
	if value.IsNull() {
		fields[column] = nil
		return
	}

	trimmed := strings.TrimSpace(value.MustGet())
	fields[column] = lo.Ternary[any](trimmed == "", nil, trimmed)
}

// FieldCannotBeNull 用于库层 NOT NULL、因而不能接受显式 null 的字段。
// 这里只声明"违反 not_null 规则"，文案由渲染层按请求语言从 lang/*/validation.toml 取。
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
		// 手机号同样是可写列，撞索引时不该退化成通用 409——调用方需要知道是哪个字段。
		return errs.New(errs.CodeAuthPhoneNumberTaken, "The phone number has already been taken")
	default:
		return errs.Conflict("The user conflicts with an existing record")
	}
}

// setUsername 处理用户名的三态：显式 null 表示解绑，赋新值前需确认未被他人占用。
func setUsername(
	ctx context.Context,
	db bun.IDB,
	userID string,
	fields map[string]any,
	value omitnull.Val[string],
) error {
	if value.IsNull() {
		fields["username"] = nil
		fields["display_username"] = nil
		return nil
	}

	normalised := NormaliseUsername(value.MustGet())
	// 这次查重只为给出友好错误，不构成并发保证：两个用户同时改名可能双双通过这里。
	// 真正的仲裁是唯一索引 idx_user_username，冲突由 MapUniqueConflict 兜底成同一个错误码。
	existing, err := models.FindUserByUsername(ctx, db, normalised)
	switch {
	case err == nil && existing.ID != userID:
		return errs.New(errs.CodeAuthUsernameTaken, "The username has already been taken")
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return errs.Internal(err)
	}

	fields["username"] = normalised
	if _, ok := fields["display_username"]; !ok {
		fields["display_username"] = normalised
	}
	return nil
}
