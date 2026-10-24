package seeders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/database/models"
	"bgin/internal/platform/hasher"
	"bgin/internal/platform/logger"
	"bgin/internal/platform/validator"
)

func init() {
	Register(&AdminSeeder{})
}

// AdminSeeder 负责初始超级管理员账号的幂等播种。
type AdminSeeder struct {
	Cfg *config.Config
}

// Name 返回播种器在注册表中的唯一名称。
func (s *AdminSeeder) Name() string {
	return "AdminSeeder"
}

// Seed 执行管理员播种操作。
func (s *AdminSeeder) Seed(ctx context.Context, db *bun.DB) error {
	cfg := s.Cfg
	if cfg == nil {
		loaded, err := config.Load()
		if err != nil {
			return fmt.Errorf("admin seeder load config failed: %w", err)
		}
		cfg = loaded
	}

	if cfg.Seed.AdminEmail == "" || cfg.Seed.AdminPassword == "" {
		return errors.New("admin seeder: please set SEED_ADMIN_EMAIL and SEED_ADMIN_PASSWORD in environment")
	}
	if err := validator.PasswordStrength("password", cfg.Seed.AdminPassword); err != nil {
		return fmt.Errorf("admin seeder: SEED_ADMIN_PASSWORD does not meet strength requirements: %w", err)
	}

	pwdHasher := hasher.New()
	_, err := models.FindUserByEmail(ctx, db, cfg.Seed.AdminEmail)
	switch {
	case err == nil:
		logger.FromContext(ctx).Warn("admin account already exists, skipping seeding",
			zap.String("email", cfg.Seed.AdminEmail))
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("admin seeder query existing admin failed: %w", err)
	}

	hash, err := pwdHasher.Make(cfg.Seed.AdminPassword)
	if err != nil {
		return fmt.Errorf("admin seeder hash password failed: %w", err)
	}

	admin := &models.User{
		Name:          cfg.Seed.AdminName,
		Email:         strings.ToLower(strings.TrimSpace(cfg.Seed.AdminEmail)),
		EmailVerified: true,
		Role:          models.RoleAdmin,
		Status:        models.StatusActive,
	}

	if err := models.CreateUserWithCredential(ctx, db, admin, hash); err != nil {
		return fmt.Errorf("admin seeder create account failed: %w", err)
	}

	logger.FromContext(ctx).Info("initial admin user seeded successfully",
		zap.String("id", admin.ID),
		zap.String("email", admin.Email))
	return nil
}
