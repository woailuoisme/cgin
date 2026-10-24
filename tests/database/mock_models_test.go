package database_test

import (
	"context"
	"errors"

	"github.com/uptrace/bun"

	"bgin/internal/database"
)

type mockTrackableItem struct {
	bun.BaseModel `bun:"table:mock_trackable,alias:m"`
	database.Base
	database.ChangeTracker
	Title string `bun:"title"`
}

func (m *mockTrackableItem) SyncOriginal() {
	m.SyncOriginalFrom(m)
}

func (m *mockTrackableItem) GetDirty() []string {
	return m.GetDirtyWith(m)
}

func (m *mockTrackableItem) IsDirty(columns ...string) bool {
	return m.IsDirtyWith(m, columns...)
}

type mockAfterFailItem struct {
	bun.BaseModel `bun:"table:mock_after_fail,alias:m"`
	database.Base
}

func (m *mockAfterFailItem) AfterCreate(ctx context.Context) error {
	return errors.New("after create hook error")
}

type mockItemNoID struct {
	bun.BaseModel `bun:"table:no_id_items,alias:n"`
	Name          string `bun:"name"`
}

type mockAccountItem struct {
	bun.BaseModel `bun:"table:mock_accounts,alias:a"`
	database.Base
	UserID   string `bun:"user_id"`
	Provider string `bun:"provider"`
}

type mockUserWithAccounts struct {
	bun.BaseModel `bun:"table:mock_users,alias:u"`
	database.Base
	Accounts []*mockAccountItem `bun:"rel:has-many,join:id=user_id"`
}

type mockBeforeFailItem struct {
	bun.BaseModel `bun:"table:mock_before_fail,alias:m"`
	database.Base
}

func (m *mockBeforeFailItem) BeforeCreate(ctx context.Context) error {
	return errors.New("before create hook error")
}

type mockAfterSuccessItem struct {
	bun.BaseModel `bun:"table:mock_after_success,alias:m"`
	database.Base
	database.ChangeTracker
	AfterCalled bool `bun:"after_called"`
}

func (m *mockAfterSuccessItem) AfterCreate(ctx context.Context) error {
	m.AfterCalled = true
	return nil
}

func (m *mockAfterSuccessItem) SyncOriginal() {
	m.SyncOriginalFrom(m)
}

func (m *mockAfterSuccessItem) GetDirty() []string {
	return m.GetDirtyWith(m)
}

func (m *mockAfterSuccessItem) IsDirty(columns ...string) bool {
	return m.IsDirtyWith(m, columns...)
}

type mockErrorResult struct {
	err error
}

func (m mockErrorResult) LastInsertId() (int64, error) { return 0, nil }
func (m mockErrorResult) RowsAffected() (int64, error) { return 0, m.err }

type mockHookItem struct {
	bun.BaseModel `bun:"table:mock_hooks,alias:mh"`
	database.Base
	FailBeforeUpdate bool `bun:"-"`
	FailAfterUpdate  bool `bun:"-"`
	FailBeforeDelete bool `bun:"-"`
	FailAfterDelete  bool `bun:"-"`
	FailAfterCreate  bool `bun:"-"`
}

func (m *mockHookItem) BeforeUpdate(ctx context.Context) error {
	if m.FailBeforeUpdate {
		return errors.New("hook: fail before update")
	}
	return nil
}

func (m *mockHookItem) AfterUpdate(ctx context.Context) error {
	if m.FailAfterUpdate {
		return errors.New("hook: fail after update")
	}
	return nil
}

func (m *mockHookItem) BeforeDelete(ctx context.Context) error {
	if m.FailBeforeDelete {
		return errors.New("hook: fail before delete")
	}
	return nil
}

func (m *mockHookItem) AfterDelete(ctx context.Context) error {
	if m.FailAfterDelete {
		return errors.New("hook: fail after delete")
	}
	return nil
}

func (m *mockHookItem) AfterCreate(ctx context.Context) error {
	if m.FailAfterCreate {
		return errors.New("hook: fail after create")
	}
	return nil
}

type mockHardDeleteItem struct {
	bun.BaseModel `bun:"table:mock_hard_del,alias:hd"`
	database.Base
}
