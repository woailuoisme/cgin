package bootstrap

import (
	"github.com/rs/zerolog"
	"github.com/samber/do/v2"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/internal/middleware"
	"cgin/internal/middleware/global"
	"cgin/internal/user"
	platformauth "cgin/pkg/authentication"
	"cgin/pkg/authorization"
	"cgin/pkg/cache"
	"cgin/pkg/hasher"
	"cgin/pkg/i18n"
	"cgin/pkg/validator"
)

// AuthGuards 聚合客户端与管理端双重认证守卫。
type AuthGuards struct {
	Client *platformauth.Guard[*sqlc.User]
	Admin  *platformauth.Guard[*sqlc.User]
}

// SecurityPackage 声明认证令牌、用户仓储、权限守卫与安全中间件。
var SecurityPackage = do.Package(
	// 1. JWT 双令牌签发器
	do.Lazy(func(i do.Injector) (*platformauth.TokenIssuer, error) {
		cfg := do.MustInvoke[*config.Config](i)
		c := do.MustInvoke[cache.Store](i)
		return platformauth.NewTokenIssuer(cfg.JWT, c), nil
	}),

	// 2. 用户认证凭据提供者
	do.Lazy(func(i do.Injector) (*user.Provider, error) {
		db := do.MustInvoke[*database.DB](i)
		h := do.MustInvoke[hasher.PasswordHasher](i)
		return user.MustNewProvider(db, h), nil
	}),

	// 3. 用户领域内核服务
	do.Lazy(func(i do.Injector) (*user.Service, error) {
		db := do.MustInvoke[*database.DB](i)
		h := do.MustInvoke[hasher.PasswordHasher](i)
		return user.MustNewService(db, h), nil
	}),

	// 4. 双通道认证守卫（客户端与管理端）
	do.Lazy(func(i do.Injector) (AuthGuards, error) {
		cfg := do.MustInvoke[*config.Config](i)
		tokens := do.MustInvoke[*platformauth.TokenIssuer](i)
		userProvider := do.MustInvoke[*user.Provider](i)

		clientGuard := platformauth.NewGuard(
			tokens,
			userProvider,
			user.ClientPolicy{RequireEmailVerification: cfg.Auth.RequireEmailVerification},
			userProvider.Identity,
		)
		adminGuard := platformauth.NewGuard(tokens, userProvider, user.AdminPolicy{}, userProvider.Identity)

		return AuthGuards{
			Client: clientGuard,
			Admin:  adminGuard,
		}, nil
	}),

	// 5. 认证管理器
	do.Lazy(func(i do.Injector) (*platformauth.Manager, error) {
		guards := do.MustInvoke[AuthGuards](i)
		return platformauth.NewManager().
			Register("client", guards.Client).
			Register("admin", guards.Admin).
			SetDefault("client"), nil
	}),

	// 6. 全局传输层中间件
	do.Lazy(func(i do.Injector) (*global.Middleware, error) {
		cfg := do.MustInvoke[*config.Config](i)
		c := do.MustInvoke[cache.Store](i)
		log := do.MustInvoke[*zerolog.Logger](i)
		translator := do.MustInvoke[i18n.Translator](i)
		val := do.MustInvoke[*validator.Validator](i)

		return global.New(global.Deps{
			Cache:          c,
			Logger:         log,
			CORS:           cfg.CORS,
			Production:     cfg.IsProduction(),
			Translator:     translator,
			Validator:      val,
			RequestTimeout: cfg.App.Server.RequestTimeout,
			UploadMaxBody:  cfg.Media.MaxFileSize,
		}), nil
	}),

	// 7. 路由级传输层中间件
	do.Lazy(func(i do.Injector) (*middleware.Middleware, error) {
		cfg := do.MustInvoke[*config.Config](i)
		authManager := do.MustInvoke[*platformauth.Manager](i)
		c := do.MustInvoke[cache.Store](i)
		enforcer := do.MustInvoke[*authorization.Enforcer](i)
		log := do.MustInvoke[*zerolog.Logger](i)

		return middleware.New(middleware.Deps{
			AuthManager: authManager,
			Cache:       c,
			Authz:       enforcer,
			Logger:      log,
			Limiter:     cfg.Limiter,
			SigningKey:  cfg.JWT.Secret,
		}), nil
	}),
)
