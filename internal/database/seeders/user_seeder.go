package seeders

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/database/factories"
	"bgin/internal/database/models"
	"bgin/internal/platform/logger"
)

func init() {
	Register(&UserSeeder{})
}

// UserSeeder 负责研发与测试环境的用户数据批量播种。
type UserSeeder struct {
	Count int // 自定义播种数量，默认 20 条
	// Cfg 为可选的配置注入，缺省时自行装载；仅用于生产环境守卫。
	Cfg *config.Config
}

// Name 返回播种器在注册表中的唯一名称。
func (s *UserSeeder) Name() string {
	return "UserSeeder"
}

// Seed 执行用户数据播种流程。
func (s *UserSeeder) Seed(ctx context.Context, db *bun.DB) error {
	log := logger.FromContext(ctx)

	cfg := s.Cfg
	if cfg == nil {
		loaded, err := config.Load()
		if err != nil {
			return fmt.Errorf("user seeder load config failed: %w", err)
		}
		cfg = loaded
	}

	// 演示数据携带固定测试凭据，生产环境中直接跳过
	if cfg.IsProduction() {
		log.Warn("demo user seeding is disabled in production, skipped")
		return nil
	}

	total := cmp.Or(s.Count, 20)

	log.Info("batch generating test users via UserFactory...", zap.Int("count", total))

	// 1. 幂等插入固定测试普通用户（若不存在）
	userExists, err := db.NewSelect().
		Model((*models.User)(nil)).
		Where("email = ?", "user.demo@example.com").
		Exists(ctx)
	if err != nil {
		return fmt.Errorf("user seeder check demo user failed: %w", err)
	}

	if !userExists {
		_, err := factories.User(db).
			User().
			State(func(u *models.User) {
				u.Name = "Demo User"
				u.Email = "user.demo@example.com"
			}).
			WithAccount(factories.FixturePassword).
			Create(ctx)
		if err != nil {
			return fmt.Errorf("user seeder create demo user failed: %w", err)
		}
		log.Info("demo user account seeded", zap.String("email", "user.demo@example.com"))
	}

	// 2. 批量生成拟真用户群，覆盖活跃、封禁、未验证等多种状态
	subHook := SubProgressHookFromContext(ctx)
	if subHook != nil {
		subHook(0, int64(total))
	}

	userFactory := factories.User(db).
		WithAccount(factories.FixturePassword).
		ChunkSize(500)
	if subHook != nil {
		userFactory = userFactory.OnProgress(func(current, _ int) {
			subHook(int64(current), int64(total))
		})
	}

	// 生成活跃普通用户群
	activeCount := total - 4
	if activeCount > 0 {
		_, err := userFactory.
			User().
			CreateMany(ctx, activeCount)
		if err != nil {
			return fmt.Errorf("user seeder create active users failed: %w", err)
		}
	}

	// 生成带头像用户
	_, err = userFactory.
		WithAvatar().
		CreateMany(ctx, 2)
	if err != nil {
		return fmt.Errorf("user seeder create avatar users failed: %w", err)
	}

	// 生成 1 个已封禁用户
	banExpire := time.Now().UTC().Add(30 * 24 * time.Hour)
	_, err = userFactory.
		Banned("temporarily banned for violating community guidelines", &banExpire).
		Create(ctx)
	if err != nil {
		return fmt.Errorf("user seeder create banned user failed: %w", err)
	}

	// 生成 1 个未验证邮箱用户
	_, err = userFactory.
		Unverified().
		Create(ctx)
	if err != nil {
		return fmt.Errorf("user seeder create unverified user failed: %w", err)
	}

	if subHook != nil {
		subHook(int64(total), int64(total))
	}

	log.Info("batch test user seeding completed", zap.Int("total", total))
	return nil
}
