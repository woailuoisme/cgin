package service

import (
	"context"
	"strings"

	"github.com/aarondl/opt/omitnull"
	"github.com/samber/oops"

	"cgin/internal/user"
	"cgin/pkg/errs"
)

// userProfiles 声明本人档案所需的读写能力，由本包按需定义最小契约。
type userProfiles interface {
	GetProfile(ctx context.Context, userID string) (user.Profile, error)
	UpdateProfile(ctx context.Context, userID string, fields user.FieldSet, scope user.Scope) (user.Profile, error)
}

// mediaStorage 声明底层对象存储存在性与删除契约。
type mediaStorage interface {
	Exists(ctx context.Context, path string) (bool, error)
	Delete(ctx context.Context, path string) error
}

// ProfileDeps 声明本人档案服务的外部依赖。
type ProfileDeps struct {
	Users   userProfiles
	Storage mediaStorage
}

// ProfileService 承载本人档案用例：读取、局部更新与头像替换。
type ProfileService struct {
	users   userProfiles
	storage mediaStorage
}

// MustNewProfileService 构造档案服务；Users 为装配方必须提供的硬依赖。
func MustNewProfileService(deps ProfileDeps) *ProfileService {
	if deps.Users == nil {
		panic("profile: service requires a non-nil Users")
	}
	return &ProfileService{users: deps.Users, storage: deps.Storage}
}

// Get 读取本人档案。
func (s *ProfileService) Get(ctx context.Context, userID string) (user.Profile, error) {
	return s.users.GetProfile(ctx, userID)
}

// Update 局部更新本人档案；作用域固定为客户端自助，受控列由共享内核剔除。
func (s *ProfileService) Update(ctx context.Context, userID string, fields user.FieldSet) (user.Profile, error) {
	if fields.AvatarPath.IsValue() {
		avatarPath := strings.TrimSpace(fields.AvatarPath.MustGet())
		if avatarPath != "" {
			if err := s.verifyAvatar(ctx, avatarPath); err != nil {
				return user.Profile{}, err
			}
		}
	}

	return s.users.UpdateProfile(ctx, userID, fields, user.ScopeClient)
}

// UpdateAvatar 保存直传成功后的新头像，并在旧头像存在时清理旧文件。
func (s *ProfileService) UpdateAvatar(ctx context.Context, userID string, avatarPath string) (user.Profile, error) {
	cleanPath := strings.TrimSpace(avatarPath)
	if cleanPath == "" {
		return user.Profile{}, errs.BadRequest("avatar path cannot be empty")
	}

	if err := s.verifyAvatar(ctx, cleanPath); err != nil {
		return user.Profile{}, err
	}

	// 记录旧头像用于事后清理
	oldProfile, _ := s.users.GetProfile(ctx, userID)

	updated, err := s.users.UpdateProfile(ctx, userID, user.FieldSet{
		AvatarPath: omitnull.From(cleanPath),
	}, user.ScopeClient)
	if err != nil {
		return user.Profile{}, err
	}

	// 同步清理旧头像，避免孤儿文件堆积
	if s.storage != nil && oldProfile.AvatarPath != nil && *oldProfile.AvatarPath != "" && *oldProfile.AvatarPath != cleanPath {
		_ = s.storage.Delete(ctx, *oldProfile.AvatarPath)
	}

	return updated, nil
}

func (s *ProfileService) verifyAvatar(ctx context.Context, avatarPath string) error {
	if s.storage == nil {
		return nil
	}
	exists, err := s.storage.Exists(ctx, avatarPath)
	if err != nil {
		return oops.Code(errs.CodeStorageFailed).In("profile.verifyAvatar").Wrap(err)
	}
	if !exists {
		return errs.BadRequest("uploaded avatar does not exist in storage")
	}
	return nil
}
