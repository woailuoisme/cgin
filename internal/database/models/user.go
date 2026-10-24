package models

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"bgin/internal/database"
	"bgin/internal/platform/errs"
	"bgin/internal/shared/enums"
)

// 用户角色枚举别名，取值统一定义于 internal/shared/enums。
const (
	RoleUser  = enums.RoleUser
	RoleAdmin = enums.RoleAdmin
)

// 用户账号状态枚举别名，取值统一定义于 internal/shared/enums。
const (
	StatusActive   = enums.StatusActive
	StatusInactive = enums.StatusInactive
	StatusBanned   = enums.StatusBanned
)

// User 映射系统用户表 ("user")。
type User struct {
	bun.BaseModel `bun:"table:user,alias:u"`
	database.Base

	Name                string       `bun:"name"`
	Email               string       `bun:"email"`
	EmailVerified       bool         `bun:"email_verified"`
	Image               *string      `bun:"image"`
	Role                enums.Role   `bun:"role"`
	Status              enums.Status `bun:"status"`
	Banned              bool         `bun:"banned"`
	TwoFactorEnabled    bool         `bun:"two_factor_enabled"`
	Username            *string      `bun:"username"`
	DisplayUsername     *string      `bun:"display_username"`
	PhoneNumber         *string      `bun:"phone_number"`
	PhoneNumberVerified bool         `bun:"phone_number_verified"`
	IsAnonymous         bool         `bun:"is_anonymous"`
	LastLoginMethod     *string      `bun:"last_login_method"`
	BanReason           *string      `bun:"ban_reason"`
	BanExpires          *time.Time   `bun:"ban_expires"`
	LastLoginAt         *time.Time   `bun:"last_login_at"`
	DeletedAt           *time.Time   `bun:"deleted_at"`
}

var (
	_ database.Model         = (*User)(nil)
	_ database.SoftDeletable = (*User)(nil)
)

// GetDeletedAt 返回用户软删除时间指针（满足 SoftDeletable 契约）。
func (u *User) GetDeletedAt() *time.Time {
	if u == nil {
		return nil
	}
	return u.DeletedAt
}

// SetDeletedAt 设置用户软删除时间（满足 SoftDeletable 契约）。
func (u *User) SetDeletedAt(t *time.Time) {
	if u != nil {
		u.DeletedAt = t
		if t != nil {
			u.UpdatedAt = *t
		}
	}
}

// IsBanned 判定账号是否处于封禁状态，已过期的封禁视为解除。
func (u *User) IsBanned(now time.Time) bool {
	if !u.Banned {
		return false
	}
	return u.BanExpires == nil || u.BanExpires.After(now)
}

// SearchableAs 返回 User 实体对应的搜索索引基础名称。
func (u *User) SearchableAs() string {
	return "users"
}

// SearchableKey 返回 User 实体的全局唯一搜索键。
func (u *User) SearchableKey() string {
	return u.ID
}

// ToSearchableArray 将用户模型映射为全文搜索文档字典。
func (u *User) ToSearchableArray() map[string]any {
	doc := map[string]any{
		"id":             u.ID,
		"name":           u.Name,
		"email":          u.Email,
		"role":           string(u.Role),
		"status":         string(u.Status),
		"email_verified": u.EmailVerified,
		"created_at":     u.CreatedAt.Unix(),
		"updated_at":     u.UpdatedAt.Unix(),
	}
	if u.Username != nil {
		doc["username"] = *u.Username
	}
	if u.DisplayUsername != nil {
		doc["display_username"] = *u.DisplayUsername
	}
	if u.PhoneNumber != nil {
		doc["phone_number"] = *u.PhoneNumber
	}
	return doc
}

// GetModelType 返回多态媒体附件的模型类型标识。
func (u *User) GetModelType() string {
	return "user"
}

// GetModelID 返回多态媒体附件的宿主主键 ID。
func (u *User) GetModelID() string {
	return u.ID
}

// GetAuthIdentifier 返回认证主体唯一标识符（用户 ID）。
func (u *User) GetAuthIdentifier() string {
	if u == nil {
		return ""
	}
	return u.ID
}

// GetAuthRole 返回认证主体的权限角色。
func (u *User) GetAuthRole() string {
	if u == nil {
		return ""
	}
	return string(u.Role)
}

// HasVerifiedEmail 判断用户邮箱是否已通过验证。
func (u *User) HasVerifiedEmail() bool {
	if u == nil {
		return false
	}
	return u.EmailVerified
}

// GetJWTCustomClaims 返回需要嵌入 JWT 载荷的自定义声明字典。
func (u *User) GetJWTCustomClaims() map[string]any {
	if u == nil {
		return nil
	}
	return map[string]any{
		"email_verified": u.EmailVerified,
	}
}

// RouteNotificationFor 根据通知渠道返回接收目标标识（满足 contract.Notifiable 契约）。
func (u *User) RouteNotificationFor(channel string) string {
	if u == nil {
		return ""
	}
	switch channel {
	case "mail":
		return u.Email
	case "sms":
		if u.PhoneNumber != nil {
			return *u.PhoneNumber
		}
	}
	return ""
}

// RouteNotificationForMail 返回接收邮件通知的目标邮箱。
func (u *User) RouteNotificationForMail() string {
	if u == nil {
		return ""
	}
	return u.Email
}

// UserListFilter 是用户列表查询的过滤条件。
type UserListFilter struct {
	Search string
	Status enums.Status
	Role   enums.Role
	Sort   []string
}

// UserSortColumns 是允许排序的字段白名单，未列出的字段一律忽略以杜绝注入。
var UserSortColumns = map[string]string{
	"createdAt":   "u.created_at",
	"updatedAt":   "u.updated_at",
	"email":       "u.email",
	"name":        "u.name",
	"lastLoginAt": "u.last_login_at",
}

// UserWritableColumns 是允许经 UpdateUserFields 写入的列白名单。
var UserWritableColumns = map[string]bool{
	"name":             true,
	"image":            true,
	"username":         true,
	"display_username": true,
	"phone_number":     true,
	// 以下受控列仅管理端可写。
	"role":           false,
	"status":         false,
	"banned":         false,
	"ban_reason":     false,
	"ban_expires":    false,
	"email_verified": false,
}

// UserQuery 返回针对 User 实体的泛型查询构造器。
func UserQuery(db bun.IDB) *database.Query[User] {
	return database.NewQuery[User](db)
}

// UserQueryFrom 优先从上下文提取活跃数据库或事务，无则回退至 fallback。
func UserQueryFrom(ctx context.Context, fallback bun.IDB) *database.Query[User] {
	return database.NewQueryFrom[User](ctx, fallback)
}

// ScopeUserStatus 过滤指定用户状态。
func ScopeUserStatus(status enums.Status) database.ScopeFunc[User] {
	return func(q *database.Query[User]) *database.Query[User] {
		if status != "" {
			return q.Where("?TableAlias.status = ?", status)
		}
		return q
	}
}

// ScopeUserRole 过滤指定用户权限角色。
func ScopeUserRole(role enums.Role) database.ScopeFunc[User] {
	return func(q *database.Query[User]) *database.Query[User] {
		if role != "" {
			return q.Where("?TableAlias.role = ?", role)
		}
		return q
	}
}

// ScopeUserSearch 关键词全文模糊搜索（匹配 name, email, username）。
func ScopeUserSearch(keyword string) database.ScopeFunc[User] {
	return func(q *database.Query[User]) *database.Query[User] {
		trimmed := strings.TrimSpace(keyword)
		if trimmed != "" {
			pattern := "%" + escapeLike(trimmed) + "%"
			return q.Where(
				"(?TableAlias.name ILIKE ? OR ?TableAlias.email ILIKE ? OR ?TableAlias.username ILIKE ?)",
				pattern, pattern, pattern,
			)
		}
		return q
	}
}

// ScopeUserSort 针对用户字段白名单安全排序。
func ScopeUserSort(sorts []string) database.ScopeFunc[User] {
	return func(q *database.Query[User]) *database.Query[User] {
		if len(sorts) == 0 {
			return q.Order("?TableAlias.created_at DESC")
		}
		for _, order := range sorts {
			q = q.Order(order)
		}
		return q
	}
}

// ScopeUserFilter 组合应用用户状态、角色、检索过滤。
func ScopeUserFilter(filter UserListFilter) database.ScopeFunc[User] {
	return func(q *database.Query[User]) *database.Query[User] {
		return q.
			Scope(ScopeUserStatus(filter.Status)).
			Scope(ScopeUserRole(filter.Role)).
			Scope(ScopeUserSearch(filter.Search))
	}
}

// ActiveUsersQuery 返回已排除软删除记录的查询，所有业务查询都必须经过它。
func ActiveUsersQuery(db bun.IDB) *bun.SelectQuery {
	return UserQuery(db).BuildSelect()
}

// FindUserByID 按主键查找用户（优先从上下文感知活跃事务）。
func FindUserByID(ctx context.Context, db bun.IDB, id string) (*User, error) {
	return UserQueryFrom(ctx, db).Find(ctx, id)
}

// FindUserByUsername 按用户名大小写不敏感查找用户（优先从上下文感知活跃事务）。
func FindUserByUsername(ctx context.Context, db bun.IDB, username string) (*User, error) {
	clean := strings.ToLower(strings.TrimSpace(username))
	return UserQueryFrom(ctx, db).
		Where("?TableAlias.username IS NOT NULL").
		Where("LOWER(?TableAlias.username) = ?", clean).
		First(ctx)
}

// FindUserByEmail 按邮箱大小写不敏感查找用户（优先从上下文感知活跃事务）。
func FindUserByEmail(ctx context.Context, db bun.IDB, email string) (*User, error) {
	clean := strings.ToLower(strings.TrimSpace(email))
	return UserQueryFrom(ctx, db).
		Where("LOWER(?TableAlias.email) = ?", clean).
		First(ctx)
}

// ListUsers 按条件分页查询用户（优先从上下文感知活跃事务）。
func ListUsers(ctx context.Context, db bun.IDB, filter UserListFilter, offset, limit int) ([]User, error) {
	return UserQueryFrom(ctx, db).
		Scope(ScopeUserFilter(filter)).
		Scope(ScopeUserSort(filter.Sort)).
		Offset(offset).
		Limit(limit).
		Get(ctx)
}

// CountUsers 统计满足条件的用户数（优先从上下文感知活跃事务）。
func CountUsers(ctx context.Context, db bun.IDB, filter UserListFilter) (int, error) {
	return UserQueryFrom(ctx, db).Scope(ScopeUserFilter(filter)).Count(ctx)
}

// CountActiveUsers 统计未删除用户数，供健康检查与 db:seed 使用（优先从上下文感知活跃事务）。
func CountActiveUsers(ctx context.Context, db bun.IDB) (int, error) {
	return UserQueryFrom(ctx, db).Count(ctx)
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// UpdateUserFields 按列名更新用户。列名必须落在 UserWritableColumns 白名单内，否则直接报错。
func UpdateUserFields(ctx context.Context, db bun.IDB, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}

	cols := make([]string, 0, len(fields))
	for column := range fields {
		if _, ok := UserWritableColumns[column]; !ok {
			return errs.BadRequest("Disallowed user column for update: " + column)
		}
		cols = append(cols, column)
	}
	slices.Sort(cols)

	query := database.ResolveDB(ctx, db).NewUpdate().
		Model((*User)(nil)).
		Where("id = ?", id).
		Where("deleted_at IS NULL")
	for _, column := range cols {
		query = query.Set(column+" = ?", fields[column])
	}
	query = query.Set("updated_at = ?", time.Now().UTC())

	result, err := query.Exec(ctx)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Save 保存用户记录（自动决策插入或更新）。
func (u *User) Save(ctx context.Context, db bun.IDB) error {
	return database.Save(ctx, db, u)
}

// Insert 将用户记录插入数据库。
func (u *User) Insert(ctx context.Context, db bun.IDB) error {
	return database.Insert(ctx, db, u)
}

// Update 更新指定列或全量更新。
func (u *User) Update(ctx context.Context, db bun.IDB, columns ...string) error {
	return database.Update(ctx, db, u, columns...)
}

// Delete 删除用户记录（执行软删除）。
func (u *User) Delete(ctx context.Context, db bun.IDB) error {
	return database.Delete(ctx, db, u)
}

// SoftDelete 执行软删除（更新 deleted_at 和 updated_at）。
func (u *User) SoftDelete(ctx context.Context, db bun.IDB) error {
	return database.SoftDelete(ctx, db, u)
}

// MarkEmailVerified 置位邮箱验证状态。
func (u *User) MarkEmailVerified(ctx context.Context, db bun.IDB) error {
	now := time.Now().UTC()
	_, err := database.ResolveDB(ctx, db).NewUpdate().
		Model((*User)(nil)).
		Set("email_verified = ?", true).
		Set("updated_at = ?", now).
		Where("id = ?", u.ID).
		Exec(ctx)
	if err == nil {
		u.EmailVerified = true
		u.UpdatedAt = now
	}
	return err
}

// RecordLogin 按用户 ID 刷新最近登录时间与登录方式。
func RecordLogin(ctx context.Context, db bun.IDB, userID, method string) error {
	now := time.Now().UTC()
	_, err := database.ResolveDB(ctx, db).NewUpdate().
		Model((*User)(nil)).
		Set("last_login_at = ?", now).
		Set("last_login_method = ?", method).
		Where("id = ?", userID).
		Exec(ctx)
	return err
}

// RecordLogin 刷新最近登录时间与登录方式。
func (u *User) RecordLogin(ctx context.Context, db bun.IDB, method string) error {
	now := time.Now().UTC()
	_, err := database.ResolveDB(ctx, db).NewUpdate().
		Model((*User)(nil)).
		Set("last_login_at = ?", now).
		Set("last_login_method = ?", method).
		Where("id = ?", u.ID).
		Exec(ctx)
	if err == nil {
		u.LastLoginAt = &now
		u.LastLoginMethod = &method
	}
	return err
}

// Ban 封禁用户。
func (u *User) Ban(ctx context.Context, db bun.IDB, reason string, expiresAt *time.Time) error {
	now := time.Now().UTC()
	q := database.ResolveDB(ctx, db).NewUpdate().
		Model((*User)(nil)).
		Set("banned = ?", true).
		Set("status = ?", StatusBanned).
		Set("ban_reason = ?", reason).
		Set("ban_expires = ?", expiresAt).
		Set("updated_at = ?", now).
		Where("id = ?", u.ID)
	_, err := q.Exec(ctx)
	if err == nil {
		u.Banned = true
		u.Status = StatusBanned
		u.BanReason = &reason
		u.BanExpires = expiresAt
		u.UpdatedAt = now
	}
	return err
}

// Unban 解封用户。
func (u *User) Unban(ctx context.Context, db bun.IDB) error {
	now := time.Now().UTC()
	_, err := database.ResolveDB(ctx, db).NewUpdate().
		Model((*User)(nil)).
		Set("banned = ?", false).
		Set("status = ?", StatusActive).
		Set("ban_reason = NULL").
		Set("ban_expires = NULL").
		Set("updated_at = ?", now).
		Where("id = ?", u.ID).
		Exec(ctx)
	if err == nil {
		u.Banned = false
		u.Status = StatusActive
		u.BanReason = nil
		u.BanExpires = nil
		u.UpdatedAt = now
	}
	return err
}
