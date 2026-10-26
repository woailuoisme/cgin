package seeders

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/jackc/pgx/v5"
	"github.com/samber/lo"

	"cgin/internal/config"
	"cgin/internal/database"
	"cgin/internal/database/sqlc"
	"cgin/pkg/hasher"
	"cgin/pkg/logger"
	"cgin/shared/enums"
)

// FixturePassword 是本地开发演示账号统一使用的明文口令。
const FixturePassword = "Password123!"

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

// Seed 执行用户数据播种流程：单条语句批量写入整批用户与凭据。
func (s *UserSeeder) Seed(ctx context.Context, db *database.DB) error {
	log := logger.FromContext(ctx)

	cfg, err := s.resolveConfig()
	if err != nil {
		return err
	}
	// 演示数据携带固定测试凭据，生产环境中直接跳过
	if cfg.IsProduction() {
		log.Warn().Msg("demo user seeding is disabled in production, skipped")
		return nil
	}

	total := max(cmp.Or(s.Count, 20), 1)
	log.Info().Int("count", total).Msg("batch generating test users...")

	rows, err := s.buildRows(ctx, db, total)
	if err != nil {
		return err
	}

	created, err := db.Queries(ctx).CreateUsersBatch(ctx, sqlc.CreateUsersBatchParams{
		Payload: map[string]any{"rows": rows},
	})
	if err != nil {
		return fmt.Errorf("user seeder create users failed: %w", err)
	}

	hash, err := hasher.New().Make(FixturePassword)
	if err != nil {
		return fmt.Errorf("user seeder hash password failed: %w", err)
	}

	ids := lo.Map(created, func(item sqlc.User, _ int) string { return item.ID })
	if err := db.Queries(ctx).CreateAccountsBatch(ctx, sqlc.CreateAccountsBatchParams{
		UserIDList: ids,
		ProviderID: string(enums.ProviderCredential),
		Password:   &hash,
	}); err != nil {
		return fmt.Errorf("user seeder create accounts failed: %w", err)
	}

	if subHook := SubProgressHookFromContext(ctx); subHook != nil {
		subHook(int64(len(created)), int64(len(rows)))
	}

	log.Info().Int("total", len(created)).Msg("batch test user seeding completed")
	return nil
}

// resolveConfig 返回注入的配置，缺省时自行装载。
func (s *UserSeeder) resolveConfig() (*config.Config, error) {
	if s.Cfg != nil {
		return s.Cfg, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("user seeder load config failed: %w", err)
	}
	return cfg, nil
}

// buildRows 组装固定演示账号与批量生成的测试用户行。
func (s *UserSeeder) buildRows(ctx context.Context, db *database.DB, total int) ([]map[string]any, error) {
	exists, err := s.demoUserExists(ctx, db)
	if err != nil {
		return nil, err
	}

	rows := make([]map[string]any, 0, total+1)
	if !exists {
		rows = append(rows, demoUserRow())
	}
	return append(rows, generatedUserRows(total)...), nil
}

// demoUserExists 报告固定演示账号是否已存在。
func (s *UserSeeder) demoUserExists(ctx context.Context, db *database.DB) (bool, error) {
	_, err := db.Queries(ctx).GetUserByEmail(ctx, sqlc.GetUserByEmailParams{Email: "user.demo@example.com"})
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("user seeder check demo user failed: %w", err)
	}
	return false, nil
}

// demoUserRow 返回固定演示账号的行载荷。
func demoUserRow() map[string]any {
	return map[string]any{
		"name":                  "Demo User",
		"email":                 "user.demo@example.com",
		"email_verified":        true,
		"role":                  string(enums.RoleUser),
		"status":                string(enums.StatusActive),
		"phone_number_verified": false,
	}
}

// generatedUserRows 生成覆盖活跃、头像、封禁与未验证状态的拟真用户行。
func generatedUserRows(total int) []map[string]any {
	faker := gofakeit.New(0)
	rows := make([]map[string]any, 0, total)

	for i := range total {
		row := map[string]any{
			"name":                  faker.Name(),
			"email":                 fmt.Sprintf("user%d.%s", i, strings.ToLower(faker.Email())),
			"email_verified":        true,
			"role":                  string(enums.RoleUser),
			"status":                string(enums.StatusActive),
			"username":              fmt.Sprintf("%s%d", strings.ToLower(faker.Username()), i),
			"phone_number_verified": true,
		}
		if i == 0 {
			row["image"] = fmt.Sprintf("https://picsum.photos/seed/%d/200/200", i)
		}
		if i == 1 {
			expires := time.Now().UTC().Add(30 * 24 * time.Hour)
			row["status"] = string(enums.StatusBanned)
			row["banned"] = true
			row["ban_reason"] = "temporarily banned for violating community guidelines"
			row["ban_expires"] = expires.Format(time.RFC3339)
		}
		if i == 2 {
			row["email_verified"] = false
			row["phone_number_verified"] = false
		}
		rows = append(rows, row)
	}
	return rows
}
