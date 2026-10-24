package models

import (
	"context"
	"time"

	"github.com/uptrace/bun"

	"bgin/internal/database"
)

// Verification 对应 "verification" 表：一次性凭据记录。
// 邮箱验证码与密码重置令牌共用此表，靠 identifier 前缀区分用途。
type Verification struct {
	bun.BaseModel `bun:"table:verification,alias:v"`
	database.Base

	Identifier string    `bun:"identifier"`
	Value      string    `bun:"value"`
	ExpiresAt  time.Time `bun:"expires_at"`
}

var _ database.Model = (*Verification)(nil)

// VerificationQuery 返回针对 Verification 实体的泛型查询构造器。
func VerificationQuery(db bun.IDB) *database.Query[Verification] {
	return database.NewQuery[Verification](db)
}

// VerificationQueryFrom 优先从上下文提取活跃数据库或事务，无则回退至 fallback。
func VerificationQueryFrom(ctx context.Context, fallback bun.IDB) *database.Query[Verification] {
	return database.NewQueryFrom[Verification](ctx, fallback)
}

// SaveVerification 覆盖式写入一次性凭据：同一 identifier 只保留最新一条。
// 若上层已在活跃事务中，则复用现有事务；否则通过事务保证清理与插入原子执行。
func SaveVerification(ctx context.Context, db bun.IDB, identifier, value string, expiresAt time.Time) error {
	run := func(idb bun.IDB) error {
		if _, err := idb.NewDelete().
			Model((*Verification)(nil)).
			Where("identifier = ?", identifier).
			Exec(ctx); err != nil {
			return err
		}

		record := &Verification{Identifier: identifier, Value: value, ExpiresAt: expiresAt}
		return record.Insert(ctx, idb)
	}

	if tx, ok := db.(bun.Tx); ok {
		return run(tx)
	}
	if bdb := database.UnwrapBunDB(db); bdb != nil {
		return bdb.RunInTx(ctx, nil, func(_ context.Context, tx bun.Tx) error {
			return run(tx)
		})
	}
	return run(db)
}

// FindVerification 取出某 identifier 的唯一一条凭据；不存在时返回 sql.ErrNoRows（优先从上下文感知活跃事务）。
func FindVerification(ctx context.Context, db bun.IDB, identifier string) (*Verification, error) {
	return VerificationQueryFrom(ctx, db).
		Where("?TableAlias.identifier = ?", identifier).
		First(ctx)
}

// DeleteVerification 按主键删除已核销或已过期的凭据。
func DeleteVerification(ctx context.Context, db bun.IDB, id string) error {
	v := &Verification{}
	v.ID = id
	return database.ForceDelete(ctx, db, v)
}

// ConsumeVerification 原子核销一枚凭据，通过单条 compare-and-delete 语句保证并发安全（自动透传上下文活跃事务）。
func ConsumeVerification(ctx context.Context, db bun.IDB, identifier, value string) (bool, error) {
	result, err := database.ResolveDB(ctx, db).NewDelete().
		Model((*Verification)(nil)).
		Where("identifier = ?", identifier).
		Where("value = ?", value).
		Exec(ctx)
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// Save 保存凭据记录（自动决策插入或更新）。
func (v *Verification) Save(ctx context.Context, db bun.IDB) error {
	return database.Save(ctx, db, v)
}

// Insert 将凭据记录插入数据库。
func (v *Verification) Insert(ctx context.Context, db bun.IDB) error {
	return database.Insert(ctx, db, v)
}

// Delete 删除凭据记录。
func (v *Verification) Delete(ctx context.Context, db bun.IDB) error {
	return database.ForceDelete(ctx, db, v)
}
