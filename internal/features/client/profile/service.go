package profile

import (
	"context"
	"mime/multipart"

	"github.com/aarondl/opt/omitnull"

	"cgin/internal/shared/user"
)

// userProfiles 声明本人档案所需的读写能力，由本包按需定义最小契约。
type userProfiles interface {
	GetProfile(ctx context.Context, userID string) (user.Profile, error)
	UpdateProfile(ctx context.Context, userID string, fields user.FieldSet, scope user.Scope) (user.Profile, error)
}

// avatarStore 声明头像原子替换能力。
//
// link 会在头像事务内被调用，用于把生成的 URL 回写到宿主模型；
// 返回错误即让整笔事务回滚。
type avatarStore interface {
	ChangeAvatar(
		ctx context.Context,
		userID string,
		file *multipart.FileHeader,
		link func(ctx context.Context, url string) error,
	) (string, error)
}

// Deps 声明本人档案服务的外部依赖。
type Deps struct {
	Users   userProfiles
	Avatars avatarStore
}

// Service 承载本人档案用例：读取、局部更新与头像替换。
type Service struct {
	users   userProfiles
	avatars avatarStore
}

// MustNewService 构造档案服务；Users 与 Avatars 均为装配方必须提供的硬依赖。
func MustNewService(deps Deps) *Service {
	if deps.Users == nil {
		panic("profile: service requires a non-nil Users")
	}
	if deps.Avatars == nil {
		panic("profile: service requires a non-nil Avatars")
	}
	return &Service{users: deps.Users, avatars: deps.Avatars}
}

// Get 读取本人档案。
func (s *Service) Get(ctx context.Context, userID string) (user.Profile, error) {
	return s.users.GetProfile(ctx, userID)
}

// Update 局部更新本人档案；作用域固定为客户端自助，受控列由共享内核剔除。
func (s *Service) Update(ctx context.Context, userID string, fields user.FieldSet) (user.Profile, error) {
	return s.users.UpdateProfile(ctx, userID, fields, user.ScopeClient)
}

// UploadAvatar 原子替换本人头像。
//
// 媒体记录、物理文件与 user.image 的写入收敛在一次事务里：任一步失败都会回滚，
// 不会留下指向已被删除文件的档案，也不会留下无人引用的媒体记录。
func (s *Service) UploadAvatar(ctx context.Context, userID string, file *multipart.FileHeader) (user.Profile, error) {
	var updated user.Profile

	_, err := s.avatars.ChangeAvatar(ctx, userID, file, func(txCtx context.Context, url string) error {
		profile, err := s.users.UpdateProfile(txCtx, userID, user.FieldSet{
			Image: omitnull.From(url),
		}, user.ScopeClient)
		if err != nil {
			return err
		}
		updated = profile
		return nil
	})
	if err != nil {
		return user.Profile{}, err
	}

	return updated, nil
}
