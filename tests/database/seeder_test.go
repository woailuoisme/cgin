package database_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"bgin/internal/database/seeders"
)

type mockCounterSeeder struct {
	name  string
	count int
}

func (m *mockCounterSeeder) Name() string {
	return m.name
}

func (m *mockCounterSeeder) Seed(_ context.Context, _ *bun.DB) error {
	m.count++
	return nil
}

func TestSeeder_Registry(t *testing.T) {
	// DatabaseSeeder, AdminSeeder, UserSeeder 已经在 init() 注册
	list := seeders.List()
	assert.Contains(t, list, "DatabaseSeeder")
	assert.Contains(t, list, "AdminSeeder")
	assert.Contains(t, list, "UserSeeder")

	s, ok := seeders.Get("DatabaseSeeder")
	require.True(t, ok)
	require.NotNil(t, s)

	_, ok = seeders.Get("NonExistentSeeder")
	assert.False(t, ok)
}

func TestSeeder_RunnerAntiDuplicate(t *testing.T) {
	ctx := context.Background()
	runner := seeders.NewRunner(nil)

	s1 := &mockCounterSeeder{name: "MockUserSeeder"}
	s2 := &mockCounterSeeder{name: "MockPostSeeder"}

	// 第一次调用 s1, s2
	err := runner.Call(ctx, s1, s2)
	require.NoError(t, err)
	assert.Equal(t, 1, s1.count)
	assert.Equal(t, 1, s2.count)

	// 第二次调用 s1（模拟菱形依赖触发重复调用）
	err = runner.Call(ctx, s1)
	require.NoError(t, err)
	assert.Equal(t, 1, s1.count, "相同 Seeder 在同一 Runner 生命周期内不应重复执行")
}
