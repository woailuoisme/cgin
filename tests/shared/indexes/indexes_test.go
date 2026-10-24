// Package indexes_test 验证全文检索索引器与数据同步契约。
package indexes_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/search"
	"bgin/internal/shared/indexes"
)

func TestIndexesRegisterAll(t *testing.T) {
	indexes.RegisterAll()

	desc, ok := search.GetModel("users")
	require.True(t, ok)
	assert.Equal(t, "user", desc.Name)
	assert.Equal(t, "users", desc.IndexName)
	assert.NotNil(t, desc.Importer)
}

func TestImportUsersForSearchNilDB(t *testing.T) {
	ctx := t.Context()
	count, err := indexes.ImportUsersForSearch(ctx, nil, 10, func(_ []map[string]any) error {
		return nil
	})
	require.Error(t, err)
	assert.Zero(t, count)
}
