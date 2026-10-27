// Package database_test 验证数据库层对外暴露的纯审计契约。
package database_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"cgin/internal/database"
)

// TestQueryFingerprintIgnoresFormatting 校验指纹只受语句语义影响，与空白排版无关。
func TestQueryFingerprintIgnoresFormatting(t *testing.T) {
	compact := `SELECT * FROM "user" WHERE id = $1 AND deleted_at IS NULL`
	spacious := "SELECT *\n\tFROM \"user\"\nWHERE id = $1\n\tAND deleted_at IS NULL"

	assert.Equal(t, database.QueryFingerprint(compact), database.QueryFingerprint(spacious))
	assert.Len(t, database.QueryFingerprint(compact), 16)
}

// TestQueryFingerprintSeparatesStatements 校验不同语句不会撞进同一指纹。
func TestQueryFingerprintSeparatesStatements(t *testing.T) {
	a := database.QueryFingerprint(`SELECT * FROM "user" WHERE id = $1`)
	b := database.QueryFingerprint(`SELECT * FROM "user" WHERE email = $1`)

	assert.NotEqual(t, a, b)
}

// TestQueryFingerprintKeepsParametersOut 校验字面量拼接会把值带进指纹，
// 这正是此类语句被 loggableStatement 拒绝输出原文的原因。
func TestQueryFingerprintKeepsParametersOut(t *testing.T) {
	withLiteral := `SELECT * FROM "user" WHERE email = 'alice@example.com'`
	otherLiteral := `SELECT * FROM "user" WHERE email = 'bob@example.com'`

	assert.NotEqual(t, database.QueryFingerprint(withLiteral), database.QueryFingerprint(otherLiteral))
}
