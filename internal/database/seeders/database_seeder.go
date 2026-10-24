package seeders

import (
	"context"

	"github.com/uptrace/bun"
)

func init() {
	Register(&DatabaseSeeder{})
}

// DatabaseSeeder 是系统总入口播种器，负责按拓扑依赖顺序编排所有子播种器。
type DatabaseSeeder struct{}

// Name 返回总播种器在注册表中的标识。
func (s *DatabaseSeeder) Name() string {
	return "DatabaseSeeder"
}

// Seed 按序调度 AdminSeeder 与 UserSeeder。
func (s *DatabaseSeeder) Seed(ctx context.Context, db *bun.DB) error {
	runner := NewRunner(db)

	return runner.Call(ctx,
		&AdminSeeder{},
		&UserSeeder{},
	)
}
