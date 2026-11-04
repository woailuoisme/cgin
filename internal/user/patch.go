package user

import (
	"cmp"
	"strings"
	"time"

	"github.com/aarondl/opt/omitnull"
	govalidator "github.com/go-playground/validator/v10"

	"cgin/internal/database/sqlc"
	"cgin/internal/enum"
	"cgin/pkg/validator"
)

// sortColumns 是允许排序的字段白名单。
var sortColumns = map[string]bool{
	"createdAt":   true,
	"updatedAt":   true,
	"email":       true,
	"name":        true,
	"lastLoginAt": true,
}

// defaultSort 保证分页遍历的确定性顺序，对应 api.md 6.2。
const defaultSort = "-createdAt"

// likeReplacer 预编译 SQL LIKE 通配符转义替换器。
var likeReplacer = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// ValidationOptions 返回用户模块贡献给校验器的规则。
func ValidationOptions() []validator.Option {
	return []validator.Option{validator.WithRule("usersort", ValidateSort, "oneof")}
}

// FieldSet 是客户端与后台管理共用的可更新档案字段集合。
type FieldSet struct {
	Name            omitnull.Val[string]
	AvatarPath      omitnull.Val[string]
	Username        omitnull.Val[string]
	DisplayUsername omitnull.Val[string]
	PhoneNumber     omitnull.Val[string]
}

// AdminFieldSet 承载管理端具备完整权限的用户模型局部更新字段集合。
type AdminFieldSet struct {
	Name            omitnull.Val[string]
	AvatarPath      omitnull.Val[string]
	Username        omitnull.Val[string]
	DisplayUsername omitnull.Val[string]
	PhoneNumber     omitnull.Val[string]
	EmailVerified   omitnull.Val[bool]
	Role            omitnull.Val[enum.Role]
	Status          omitnull.Val[enum.Status]
	Banned          omitnull.Val[bool]
	BanReason       omitnull.Val[string]
	BanExpires      omitnull.Val[time.Time]
}

// Patch 把三态字段归一为单条 UPDATE 所需的显式设置/清空意图。
type Patch struct {
	Name             *string
	AvatarPath       *string
	ClearAvatarPath  bool
	Username         *string
	ClearUsername    bool
	DisplayUsername  *string
	ClearDisplayName bool
	PhoneNumber      *string
	ClearPhone       bool
	EmailVerified    *bool
	Role             *enum.Role
	Status           *enum.Status
	Banned           *bool
	BanReason        *string
	ClearBanReason   bool
	BanExpires       *time.Time
	ClearBanExpires  bool
}

// Empty 报告补丁是否不含任何待写入字段。
func (p Patch) Empty() bool {
	return p.Name == nil && p.AvatarPath == nil && !p.ClearAvatarPath &&
		p.Username == nil && !p.ClearUsername &&
		p.DisplayUsername == nil && !p.ClearDisplayName &&
		p.PhoneNumber == nil && !p.ClearPhone &&
		p.EmailVerified == nil && p.Role == nil && p.Status == nil &&
		p.Banned == nil && p.BanReason == nil && !p.ClearBanReason &&
		p.BanExpires == nil && !p.ClearBanExpires
}

// PatchFromFieldSet 把客户端档案字段翻译为补丁。
func PatchFromFieldSet(fields FieldSet) (Patch, error) {
	var patch Patch

	if !fields.Name.IsUnset() {
		if fields.Name.IsNull() {
			return Patch{}, FieldCannotBeNull("name")
		}
		name := strings.TrimSpace(fields.Name.MustGet())
		patch.Name = &name
	}

	patch.AvatarPath, patch.ClearAvatarPath = textField(fields.AvatarPath)
	patch.PhoneNumber, patch.ClearPhone = textField(fields.PhoneNumber)
	patch.DisplayUsername, patch.ClearDisplayName = textField(fields.DisplayUsername)

	if !fields.Username.IsUnset() {
		value, clear := textField(fields.Username)
		patch.Username = value
		patch.ClearUsername = clear
		if clear && fields.DisplayUsername.IsUnset() {
			patch.ClearDisplayName = true
		}
	}

	return patch, nil
}

// PatchFromAdminFieldSet 把管理端字段翻译为补丁，并协同封禁状态。
func PatchFromAdminFieldSet(fields AdminFieldSet) (Patch, error) {
	patch, err := PatchFromFieldSet(FieldSet{
		Name:            fields.Name,
		AvatarPath:      fields.AvatarPath,
		Username:        fields.Username,
		DisplayUsername: fields.DisplayUsername,
		PhoneNumber:     fields.PhoneNumber,
	})
	if err != nil {
		return Patch{}, err
	}

	if fields.EmailVerified.IsValue() {
		value := fields.EmailVerified.MustGet()
		patch.EmailVerified = &value
	}
	if !fields.Role.IsUnset() {
		if fields.Role.IsNull() {
			return Patch{}, FieldCannotBeNull("role")
		}
		value := fields.Role.MustGet()
		patch.Role = &value
	}
	if !fields.Status.IsUnset() {
		if fields.Status.IsNull() {
			return Patch{}, FieldCannotBeNull("status")
		}
		value := fields.Status.MustGet()
		patch.Status = &value
	}
	if fields.Banned.IsValue() {
		value := fields.Banned.MustGet()
		patch.Banned = &value
	}
	patch.BanReason, patch.ClearBanReason = textField(fields.BanReason)
	if !fields.BanExpires.IsUnset() {
		if fields.BanExpires.IsNull() {
			patch.ClearBanExpires = true
		} else {
			value := fields.BanExpires.MustGet().UTC()
			patch.BanExpires = &value
		}
	}

	return patch.reconciled(), nil
}

func (p Patch) reconciled() Patch {
	if p.Status != nil && *p.Status == enum.StatusBanned {
		banned := true
		p.Banned = &banned
	}
	if p.Banned != nil && *p.Banned {
		status := enum.StatusBanned
		p.Status = &status
	}
	return p
}

// Params 把补丁投影为 sqlc 更新参数。
func (p Patch) Params(id string) sqlc.UpdateUserParams {
	params := sqlc.UpdateUserParams{
		ID:               id,
		Name:             p.Name,
		AvatarPath:       p.AvatarPath,
		ClearAvatarPath:  p.ClearAvatarPath,
		Username:         p.Username,
		ClearUsername:    p.ClearUsername,
		DisplayUsername:  p.DisplayUsername,
		ClearDisplayName: p.ClearDisplayName,
		PhoneNumber:      p.PhoneNumber,
		ClearPhone:       p.ClearPhone,
		EmailVerified:    p.EmailVerified,
		Banned:           p.Banned,
		BanReason:        p.BanReason,
		ClearBanReason:   p.ClearBanReason,
		BanExpires:       p.BanExpires,
		ClearBanExpires:  p.ClearBanExpires,
	}
	if p.Role != nil {
		role := string(*p.Role)
		params.Role = &role
	}
	if p.Status != nil {
		status := string(*p.Status)
		params.Status = &status
	}
	return params
}

func textField(value omitnull.Val[string]) (*string, bool) {
	if value.IsUnset() {
		return nil, false
	}
	if value.IsNull() {
		return nil, true
	}
	trimmed := strings.TrimSpace(value.MustGet())
	if trimmed == "" {
		return nil, true
	}
	return &trimmed, false
}

// ListFilter 是用户列表查询的过滤条件。
type ListFilter struct {
	Search   string
	Status   enum.Status
	Role     enum.Role
	SortKey  string
	SortDesc bool
}

// NewListFilter 归一化过滤条件与排序表达式。
func NewListFilter(search string, status enum.Status, role enum.Role, sort string) ListFilter {
	key, desc := NormaliseSort(sort)
	return ListFilter{
		Search:   strings.TrimSpace(search),
		Status:   status,
		Role:     role,
		SortKey:  key,
		SortDesc: desc,
	}
}

// ListParams 生成分页查询参数。
func (f ListFilter) ListParams(page, pageSize int) sqlc.ListUsersParams {
	return sqlc.ListUsersParams{
		Status:     optionalText(f.Status),
		Role:       optionalText(f.Role),
		Search:     f.searchPattern(),
		SortKey:    f.SortKey,
		SortDesc:   f.SortDesc,
		PageLimit:  int64(pageSize),
		PageOffset: int64(page-1) * int64(pageSize),
	}
}

// AfterParams 生成键集分块参数：以主键游标推进，不依赖偏移量。
func (f ListFilter) AfterParams(afterID string, limit int) sqlc.ListUsersAfterParams {
	return sqlc.ListUsersAfterParams{
		Status:    optionalText(f.Status),
		Role:      optionalText(f.Role),
		Search:    f.searchPattern(),
		AfterID:   afterID,
		PageLimit: int64(limit),
	}
}

func (f ListFilter) searchPattern() *string {
	if f.Search == "" {
		return nil
	}
	pattern := "%" + EscapeLike(f.Search) + "%"
	return &pattern
}

func optionalText[T ~string](value T) *string {
	if value == "" {
		return nil
	}
	text := string(value)
	return &text
}

// ValidateSort 校验用户列表的排序字段，支持以逗号分隔的复合排序以及 - 降序前缀。
func ValidateSort(field govalidator.FieldLevel) bool {
	raw := field.Field().String()
	if raw == "" {
		return true
	}
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return false
		}
		if !sortColumns[strings.TrimPrefix(part, "-")] {
			return false
		}
	}
	return true
}

// NormaliseSort 把排序表达式归一为单一排序键与方向。
func NormaliseSort(raw string) (string, bool) {
	raw = cmp.Or(strings.TrimSpace(raw), defaultSort)
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		descending := strings.HasPrefix(part, "-")
		key := strings.TrimPrefix(part, "-")
		if sortColumns[key] {
			return key, descending
		}
	}
	return "createdAt", true
}

// EscapeLike 转义 SQL LIKE 特殊通配符，避免用户输入被当作模式语法。
func EscapeLike(value string) string {
	return likeReplacer.Replace(value)
}
