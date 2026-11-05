package admin

import (
	"github.com/samber/do/v2"

	"cgin/internal/database/sqlc"
	adminauth "cgin/internal/feature/admin/auth"
	authv1 "cgin/internal/feature/admin/auth/v1"
	adminuser "cgin/internal/feature/admin/user"
	userv1 "cgin/internal/feature/admin/user/v1"
	"cgin/internal/user"
	platformauth "cgin/pkg/authentication"
	"cgin/pkg/media"
)

// Package 声明 admin 业务特性的 DI 依赖定义。
var Package = do.Package(
	// 1. 管理端认证服务与 HTTP Handler
	do.Lazy(func(i do.Injector) (*adminauth.AuthService, error) {
		guard := do.MustInvokeNamed[*platformauth.Guard[*sqlc.User]](i, "admin_guard")
		userService := do.MustInvoke[*user.Service](i)
		return adminauth.MustNewAuthService(adminauth.AuthDeps{
			Guard:     guard,
			Passwords: userService,
		}), nil
	}),

	do.Lazy(func(i do.Injector) (*authv1.Handler, error) {
		svc := do.MustInvoke[*adminauth.AuthService](i)
		return authv1.NewHandler(svc), nil
	}),

	// 2. 管理端用户服务与 HTTP Handler
	do.Lazy(func(i do.Injector) (*adminuser.UserService, error) {
		userService := do.MustInvoke[*user.Service](i)
		signer := do.MustInvoke[media.Signer](i)
		return adminuser.MustNewUserService(adminuser.UserDeps{
			UserService: userService,
			Signer:      signer,
		}), nil
	}),

	do.Lazy(func(i do.Injector) (*userv1.Handler, error) {
		svc := do.MustInvoke[*adminuser.UserService](i)
		return userv1.NewHandler(svc), nil
	}),

	// 3. 聚合 Handlers 供路由层统一装配
	do.Lazy(func(i do.Injector) (Handlers, error) {
		return Handlers{
			Auth: do.MustInvoke[*authv1.Handler](i),
			User: do.MustInvoke[*userv1.Handler](i),
		}, nil
	}),
)
