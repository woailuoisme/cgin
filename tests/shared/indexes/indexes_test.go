// Package indexes_test 验证全文检索索引器装配与导入契约。
package indexes_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/shared/indexes"
)

func TestUserIndexerMetadata(t *testing.T) {
	catalog := indexes.Catalog(nil)

	desc, ok := catalog.Lookup("users")
	require.True(t, ok)
	assert.Equal(t, "user", desc.Name)
	assert.Equal(t, "users", desc.IndexName)
	assert.NotNil(t, desc.Importer)
	assert.NotNil(t, desc.Counter)

	assert.Equal(t, []string{"user"}, indexes.Names())
	assert.Len(t, catalog.All(), 1)
}

func TestUserImporterRejectsNilDB(t *testing.T) {
	catalog := indexes.Catalog(nil)
	desc, ok := catalog.Lookup("user")
	require.True(t, ok)

	count, err := desc.Importer(t.Context(), 10, func(_ []map[string]any) error {
		return nil
	})
	require.Error(t, err)
	assert.Zero(t, count)
}
