package user

import (
	"strings"
	"time"

	"github.com/aarondl/opt/omitnull"

	"cgin/internal/database/sqlc"
	"cgin/internal/shared/enums"
)

// FieldSet 是客户端与后台管理共用的可更新档案字段集合。
type FieldSet struct {
	Name            omitnull.Val[string]
	Image           omitnull.Val[string]
	Username        omitnull.Val[string]
	DisplayUsername omitnull.Val[string]
	PhoneNumber     omitnull.Val[string]
}

// AdminFieldSet 承载管理端具备完整权限的用户模型局部更新字段集合。
//
// 字段逐项声明而不是嵌入 FieldSet：嵌入会让“哪些字段可被管理端写入”
// 这件事取决于另一个类型的演化。
type AdminFieldSet struct {
	Name            omitnull.Val[string]
	Image           omitnull.Val[string]
	Username        omitnull.Val[string]
	DisplayUsername omitnull.Val[string]
	PhoneNumber     omitnull.Val[string]
	EmailVerified   omitnull.Val[bool]
	Role            omitnull.Val[enums.Role]
	Status          omitnull.Val[enums.Status]
	Banned          omitnull.Val[bool]
	BanReason       omitnull.Val[string]
	BanExpires      omitnull.Val[time.Time]
}

// Patch 把三态字段归一为单条 UPDATE 所需的显式设置/清空意图。
type Patch struct {
	Name             *string
	Image            *string
	ClearImage       bool
	Username         *string
	ClearUsername    bool
	DisplayUsername  *string
	ClearDisplayName bool
	PhoneNumber      *string
	ClearPhone       bool
	EmailVerified    *bool
	Role             *enums.Role
	Status           *enums.Status
	Banned           *bool
	BanReason        *string
	ClearBanReason   bool
	BanExpires       *time.Time
	ClearBanExpires  bool
}

// Empty 报告补丁是否不含任何待写入字段。
func (p Patch) Empty() bool {
	return p.Name == nil && p.Image == nil && !p.ClearImage &&
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

	patch.Image, patch.ClearImage = textField(fields.Image)
	patch.PhoneNumber, patch.ClearPhone = textField(fields.PhoneNumber)
	patch.DisplayUsername, patch.ClearDisplayName = textField(fields.DisplayUsername)

	if !fields.Username.IsUnset() {
		value, clear := textField(fields.Username)
		patch.Username = value
		patch.ClearUsername = clear
		// 用户名与展示名同属一套登录标识：清空用户名时联动清空展示名。
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
		Image:           fields.Image,
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

// reconciled 返回封禁布尔值与账号状态相互协同后的补丁副本。
//
// Patch 是值语义：全部方法共用值接收者，避免同一个类型混用两种接收者。
func (p Patch) reconciled() Patch {
	if p.Status != nil && *p.Status == enums.StatusBanned {
		banned := true
		p.Banned = &banned
	}
	if p.Banned != nil && *p.Banned {
		status := enums.StatusBanned
		p.Status = &status
	}
	return p
}

// Params 把补丁投影为 sqlc 更新参数。
func (p Patch) Params(id string) sqlc.UpdateUserParams {
	params := sqlc.UpdateUserParams{
		ID:               id,
		Name:             p.Name,
		Image:            p.Image,
		ClearImage:       p.ClearImage,
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

// textField 处理可空文本三态：未提供表示不改，null/纯空白表示清空，其余为设置值。
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
