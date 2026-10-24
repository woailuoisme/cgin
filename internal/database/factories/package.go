package factories

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"bgin/internal/platform/hasher"
)

// User 快捷构造 UserFactory 实例。
func User(db bun.IDB) *UserFactory {
	return NewUserFactory(db)
}

// Account 快捷构造 AccountFactory 实例。
func Account(db bun.IDB) *AccountFactory {
	return NewAccountFactory(db)
}

// FixturePassword 是全部夹具与播种账号统一使用的明文口令（本地开发免记口令用）。
const FixturePassword = "Password123!"

// newUUID 生成夹具主键。熵源异常属环境级故障：夹具宁可当场失败（播种时即进程退出），
// 也不要落一个零 UUID 让后续断言以"随机撞名"的形式飘红。
func newUUID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		panic(fmt.Errorf("factories: generate uuid v7 failed: %w", err))
	}
	return id
}

// hashPasswordOrPanic 生成夹具口令哈希；失败即 panic，避免夹具带着空哈希静默通过。
func hashPasswordOrPanic(h *hasher.Hasher, raw string) string {
	hash, err := h.Make(raw)
	if err != nil {
		panic(fmt.Errorf("factories: hash fixture password failed: %w", err))
	}
	return hash
}
