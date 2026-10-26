package seeders

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/pkg/hasher"
	"cgin/pkg/logger"
	"cgin/pkg/validator"
	"cgin/shared/enums"
)

// AdminSeeder 负责初始超级管理员账号的幂等播种。
type AdminSeeder struct {
	Cfg *config.Config
}

// Name 返回播种器在注册表中的唯一名称。
func (s *AdminSeeder) Name() string {
	return "AdminSeeder"
}

// Seed 执行管理员播种操作。
func (s *AdminSeeder) Seed(ctx context.Context, db *database.DB) error {
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

	email := strings.ToLower(strings.TrimSpace(cfg.Seed.AdminEmail))
	if _, err := db.Queries(ctx).GetUserByEmail(ctx, sqlc.GetUserByEmailParams{Email: email}); err == nil {
		logger.FromContext(ctx).Warn().Str("email", email).Msg("admin account already exists, skipping seeding")
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("admin seeder query existing admin failed: %w", err)
	}

	hash, err := hasher.New().Make(cfg.Seed.AdminPassword)
	if err != nil {
		return fmt.Errorf("admin seeder hash password failed: %w", err)
	}

	var admin sqlc.User
	err = db.RunInTx(ctx, func(ctx context.Context, q *sqlc.Queries) error {
		row, err := q.CreateUser(ctx, sqlc.CreateUserParams{
			Name:          cfg.Seed.AdminName,
			Email:         email,
			EmailVerified: true,
			Role:          string(enums.RoleAdmin),
			Status:        string(enums.StatusActive),
		})
		if err != nil {
			return err
		}
		if _, err := q.CreateAccount(ctx, sqlc.CreateAccountParams{
			UserID:     row.ID,
			AccountID:  row.ID,
			ProviderID: string(enums.ProviderCredential),
			Password:   &hash,
		}); err != nil {
			return err
		}
		admin = row
		return nil
	})
	if err != nil {
		return fmt.Errorf("admin seeder create account failed: %w", err)
	}

	logger.FromContext(ctx).Info().
		Str("id", admin.ID).
		Str("email", admin.Email).
		Msg("initial admin user seeded successfully")
	return nil
}
