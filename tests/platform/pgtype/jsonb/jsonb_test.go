package jsonb_test

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"bgin/internal/platform/pgtype/jsonb"
)

type userProfile struct {
	Name    string   `json:"name"`
	Age     int      `json:"age"`
	Tags    []string `json:"tags"`
	Address *address `json:"address,omitempty"`
}

type address struct {
	City   string `json:"city"`
	Street string `json:"street"`
}

// TestJSONB_Basic 验证强类型泛型 JSONB 容器的基本操作。
func TestJSONB_Basic(t *testing.T) {
	data := userProfile{Name: "Alex", Age: 30, Tags: []string{"go", "postgresql"}}
	j := jsonb.New(data)

	assert.Equal(t, "Alex", j.Get().Name)
	assert.Equal(t, 30, j.Get().Age)
	assert.False(t, j.IsZero())

	// Set 测试
	j.Set(userProfile{Name: "Bob", Age: 25})
	assert.Equal(t, "Bob", j.Get().Name)
	assert.Equal(t, 25, j.Get().Age)

	// String 测试
	assert.Contains(t, j.String(), `"name":"Bob"`)

	// 零值判断
	var empty jsonb.JSONB[userProfile]
	assert.True(t, empty.IsZero())
}

// TestJSONB_ScannerValuer 验证数据库驱动 Scanner 与 Valuer 契约。
func TestJSONB_ScannerValuer(t *testing.T) {
	// 1. 结构体 Valuer / Scanner 往返
	profile := userProfile{
		Name: "Charlie",
		Age:  28,
		Tags: []string{"cloud"},
		Address: &address{
			City:   "Hangzhou",
			Street: "Xihu Road",
		},
	}
	j := jsonb.New(profile)

	v, err := j.Value()
	require.NoError(t, err)
	assert.NotNil(t, v)

	var scanned jsonb.JSONB[userProfile]
	// byte slice scan
	strVal, ok := v.(string)
	require.True(t, ok)
	require.NoError(t, scanned.Scan([]byte(strVal)))
	assert.Equal(t, "Charlie", scanned.Get().Name)
	assert.Equal(t, "Hangzhou", scanned.Get().Address.City)

	// string scan
	var strScanned jsonb.JSONB[userProfile]
	require.NoError(t, strScanned.Scan(strVal))
	assert.Equal(t, "Charlie", strScanned.Get().Name)

	// 2. SQL NULL 容错
	var nullScanned jsonb.JSONB[userProfile]
	require.NoError(t, nullScanned.Scan(nil))
	assert.True(t, nullScanned.IsZero())

	require.NoError(t, nullScanned.Scan("null"))
	assert.True(t, nullScanned.IsZero())

	require.NoError(t, nullScanned.Scan([]byte("")))
	assert.True(t, nullScanned.IsZero())

	// 3. 切片/指针类型的 zero value 生成 SQL NULL
	var nilSlice jsonb.JSONB[[]string]
	nilVal, err := nilSlice.Value()
	require.NoError(t, err)
	assert.Nil(t, nilVal)

	var nilPtr jsonb.JSONB[*address]
	nilPtrVal, err := nilPtr.Value()
	require.NoError(t, err)
	assert.Nil(t, nilPtrVal)

	// 4. 异常分支：不支持的类型与非法 JSON
	var failScanned jsonb.JSONB[userProfile]
	err = failScanned.Scan(12345)
	require.Error(t, err)
	require.ErrorIs(t, err, jsonb.ErrInvalidJSONB)

	err = failScanned.Scan([]byte("{invalid-json"))
	require.Error(t, err)
	require.ErrorIs(t, err, jsonb.ErrInvalidJSONB)

	// 接口断言
	var _ driver.Valuer = j
	var _ sql.Scanner = &j
}

// TestJSONB_JSONMarshaling 验证标准库 json.Marshal / Unmarshal 的透明适配。
func TestJSONB_JSONMarshaling(t *testing.T) {
	type wrapper struct {
		ID   int                      `json:"id"`
		Meta jsonb.JSONB[userProfile] `json:"meta"`
	}

	w := wrapper{
		ID: 101,
		Meta: jsonb.New(userProfile{
			Name: "David",
			Age:  35,
		}),
	}

	bytes, err := json.Marshal(w)
	require.NoError(t, err)
	assert.Contains(t, string(bytes), `"id":101`)
	assert.Contains(t, string(bytes), `"name":"David"`)

	var unmarshaled wrapper
	err = json.Unmarshal(bytes, &unmarshaled)
	require.NoError(t, err)
	assert.Equal(t, 101, unmarshaled.ID)
	assert.Equal(t, "David", unmarshaled.Meta.Get().Name)

	// 空数据反序列化
	var emptyMeta jsonb.JSONB[userProfile]
	err = json.Unmarshal([]byte("null"), &emptyMeta)
	require.NoError(t, err)
	assert.True(t, emptyMeta.IsZero())
}

// TestJSONB_BunQueryDSL 验证针对 Bun ORM 生成的 PostgreSQL jsonb 算子与 SQL 表达。
func TestJSONB_BunQueryDSL(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	type Item struct {
		bun.BaseModel `bun:"table:items"`
		ID            int                         `bun:"id,pk"`
		Config        jsonb.JSONB[map[string]any] `bun:"config,type:jsonb"`
	}

	// 1. WhereContains (@>)
	q1 := db.NewSelect().Model((*Item)(nil))
	q1 = jsonb.WhereContains(q1, "config", map[string]any{"env": "prod"})
	assert.Contains(t, q1.String(), `WHERE ("config"::jsonb @> '{"env":"prod"}'::jsonb)`)

	// 2. WhereContained (<@)
	q2 := db.NewSelect().Model((*Item)(nil))
	q2 = jsonb.WhereContained(q2, "config", `{"a": 1, "b": 2}`)
	assert.Contains(t, q2.String(), `WHERE ("config"::jsonb <@ '{"a": 1, "b": 2}'::jsonb)`)

	// 3. WhereHasKey (jsonb_exists)
	q3 := db.NewSelect().Model((*Item)(nil))
	q3 = jsonb.WhereHasKey(q3, "config", "retry_count")
	assert.Contains(t, q3.String(), `WHERE (jsonb_exists("config"::jsonb, 'retry_count'))`)

	// 4. WhereHasAnyKeys (jsonb_exists_any)
	q4 := db.NewSelect().Model((*Item)(nil))
	q4 = jsonb.WhereHasAnyKeys(q4, "config", "debug", "trace")
	assert.Contains(t, q4.String(), `WHERE (jsonb_exists_any("config"::jsonb, '{"debug","trace"}'))`)

	// 5. WhereHasAllKeys (jsonb_exists_all)
	q5 := db.NewSelect().Model((*Item)(nil))
	q5 = jsonb.WhereHasAllKeys(q5, "config", "host", "port")
	assert.Contains(t, q5.String(), `WHERE (jsonb_exists_all("config"::jsonb, '{"host","port"}'))`)

	// 6. WhereExtractTextEq (->>)
	q6 := db.NewSelect().Model((*Item)(nil))
	q6 = jsonb.WhereExtractTextEq(q6, "config", "status", "active")
	assert.Contains(t, q6.String(), `WHERE (("config" ->> 'status') = 'active')`)

	// 7. WherePathTextEq (#>>)
	q7 := db.NewSelect().Model((*Item)(nil))
	q7 = jsonb.WherePathTextEq(q7, "config", []string{"database", "primary", "host"}, "10.0.0.1")
	assert.Contains(t, q7.String(), `WHERE (("config" #>> '{"database","primary","host"}') = '10.0.0.1')`)

	// 8. ColumnExtractText & ColumnPathText
	q8 := db.NewSelect().Model((*Item)(nil))
	q8 = jsonb.ColumnExtractText(q8, "config", "version", "cfg_ver")
	q8 = jsonb.ColumnPathText(q8, "config", []string{"server", "port"}, "srv_port")
	sqlStr := q8.String()
	assert.Contains(t, sqlStr, `("config" ->> 'version') AS "cfg_ver"`)
	assert.Contains(t, sqlStr, `("config" #>> '{"server","port"}') AS "srv_port"`)

	// 9. UpdateQuery: ApplyMerge (||)
	u1 := db.NewUpdate().Model((*Item)(nil)).Where("id = ?", 1)
	u1 = jsonb.ApplyMerge(u1, "config", map[string]string{"theme": "dark"})
	assert.Contains(t, u1.String(), `SET "config" = "config"::jsonb || '{"theme":"dark"}'::jsonb`)

	// 10. UpdateQuery: ApplyRemoveKey (-)
	u2 := db.NewUpdate().Model((*Item)(nil)).Where("id = ?", 1)
	u2 = jsonb.ApplyRemoveKey(u2, "config", "legacy_token")
	assert.Contains(t, u2.String(), `SET "config" = "config"::jsonb - 'legacy_token'`)

	// 11. UpdateQuery: ApplyRemovePath (#-)
	u3 := db.NewUpdate().Model((*Item)(nil)).Where("id = ?", 1)
	u3 = jsonb.ApplyRemovePath(u3, "config", []string{"audit", "old_logs"})
	assert.Contains(t, u3.String(), `SET "config" = "config"::jsonb #- '{"audit","old_logs"}'`)

	// 12. 边界防卫检查：nil query 或空参数
	assert.Nil(t, jsonb.WhereContains(nil, "col", "{}"))
	assert.Nil(t, jsonb.WhereContained(nil, "col", "{}"))
	assert.Nil(t, jsonb.WhereHasKey(nil, "col", "k"))
	assert.Nil(t, jsonb.WhereExtractTextEq(nil, "col", "f", "v"))
	assert.Nil(t, jsonb.ColumnExtractText(nil, "col", "f", "alias"))
	assert.Nil(t, jsonb.ApplyMerge(nil, "col", "{}"))
	assert.Nil(t, jsonb.ApplyRemoveKey(nil, "col", "k"))

	qEmpty := db.NewSelect().Model((*Item)(nil))
	assert.Equal(t, qEmpty, jsonb.WhereHasAnyKeys(qEmpty, "config"))
	assert.Equal(t, qEmpty, jsonb.WhereHasAllKeys(qEmpty, "config"))
	assert.Equal(t, qEmpty, jsonb.WherePathTextEq(qEmpty, "config", nil, "v"))
	assert.Equal(t, qEmpty, jsonb.ColumnPathText(qEmpty, "config", nil, "alias"))

	uEmpty := db.NewUpdate().Model((*Item)(nil))
	assert.Equal(t, uEmpty, jsonb.ApplyRemovePath(uEmpty, "config", nil))
}
