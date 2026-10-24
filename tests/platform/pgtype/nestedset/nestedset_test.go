package nestedset_test

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"bgin/internal/platform/pgtype/nestedset"
)

// TestPath_Basic 验证 ltree 路径的基本构造、分段、深度与父子派生。
func TestPath_Basic(t *testing.T) {
	p := nestedset.MustPath("Top", "Science", "Astronomy")
	assert.Equal(t, "Top.Science.Astronomy", p.String())
	assert.Equal(t, 3, p.Depth())
	assert.Equal(t, 3, p.NLevel())
	assert.False(t, p.IsEmpty())
	assert.True(t, p.IsValid())
	assert.Equal(t, []string{"Top", "Science", "Astronomy"}, p.Segments())
	assert.Equal(t, "Top", p.Root())
	assert.Equal(t, "Astronomy", p.Leaf())

	// Parent
	parent := p.Parent()
	assert.Equal(t, "Top.Science", parent.String())
	grandParent := parent.Parent()
	assert.Equal(t, "Top", grandParent.String())
	rootParent := grandParent.Parent()
	assert.True(t, rootParent.IsEmpty())

	// Child
	child, err := p.Child("Astrophysics")
	require.NoError(t, err)
	assert.Equal(t, "Top.Science.Astronomy.Astrophysics", child.String())

	// 空路径行为
	var empty nestedset.Path
	assert.True(t, empty.IsEmpty())
	assert.True(t, empty.IsValid())
	assert.Equal(t, 0, empty.Depth())
	assert.Nil(t, empty.Segments())
	assert.Empty(t, empty.Root())
	assert.Empty(t, empty.Leaf())
	assert.True(t, empty.Parent().IsEmpty())

	childFromEmpty, err := empty.Child("Root")
	require.NoError(t, err)
	assert.Equal(t, "Root", childFromEmpty.String())
}

// TestPath_Validation 验证路径标签合法性规范（^[A-Za-z0-9_]{1,255}$）。
func TestPath_Validation(t *testing.T) {
	// 合法路径
	valid, err := nestedset.Parse("Corp_1.Dept_A.Team_99")
	require.NoError(t, err)
	assert.True(t, valid.IsValid())

	// 非法字符（包含中划线 -、空格、点号连续等）
	_, err = nestedset.Parse("Corp-1.Dept")
	require.Error(t, err)

	_, err = nestedset.Parse("Corp.Dept@123")
	require.Error(t, err)

	// 非法超长标签 (> 255 字符)
	tooLong := strings.Repeat("a", 256)
	_, err = nestedset.NewPath("Top", tooLong)
	require.Error(t, err)

	// MustPath 非法应当 panic
	assert.Panics(t, func() {
		nestedset.MustPath("Invalid-Label")
	})
}

// TestPath_Relationships 验证祖先、后代与层级从属关系判定。
func TestPath_Relationships(t *testing.T) {
	top := nestedset.MustPath("Top")
	sci := nestedset.MustPath("Top", "Science")
	astro := nestedset.MustPath("Top", "Science", "Astronomy")
	art := nestedset.MustPath("Top", "Art")

	// Contains (@> 语义，含自身)
	assert.True(t, sci.Contains(astro))
	assert.True(t, sci.Contains(sci))
	assert.False(t, sci.Contains(top))
	assert.False(t, sci.Contains(art))

	// IsAncestorOf (严格祖先，深度更小)
	assert.True(t, top.IsAncestorOf(sci))
	assert.True(t, sci.IsAncestorOf(astro))
	assert.False(t, sci.IsAncestorOf(sci)) // 自身不是自身的严格祖先
	assert.False(t, astro.IsAncestorOf(sci))

	// IsDescendantOf (严格后代)
	assert.True(t, astro.IsDescendantOf(sci))
	assert.True(t, astro.IsDescendantOf(top))
	assert.False(t, sci.IsDescendantOf(sci))
	assert.False(t, top.IsDescendantOf(sci))
}

// TestPath_Subpath_LCA 验证子路径截取与最低公共祖先计算。
func TestPath_Subpath_LCA(t *testing.T) {
	p := nestedset.MustPath("A", "B", "C", "D", "E")

	// Subpath
	assert.Equal(t, "B.C", p.Subpath(1, 2).String())
	assert.Equal(t, "C.D.E", p.Subpath(2, 0).String())
	assert.Equal(t, "E", p.Subpath(-1, 0).String())
	assert.Equal(t, "D.E", p.Subpath(-2, 2).String())

	// LCA (Lowest Common Ancestor)
	p1 := nestedset.MustPath("Top", "Science", "Astronomy")
	p2 := nestedset.MustPath("Top", "Science", "Physics")
	p3 := nestedset.MustPath("Top", "Science", "Chemistry", "Organic")
	p4 := nestedset.MustPath("Top", "Art", "Painting")

	assert.Equal(t, "Top.Science", nestedset.LCA(p1, p2, p3).String())
	assert.Equal(t, "Top", nestedset.LCA(p1, p4).String())

	pOther := nestedset.MustPath("Other", "Dept")
	assert.True(t, nestedset.LCA(p1, pOther).IsEmpty())
	assert.True(t, nestedset.LCA().IsEmpty())
	assert.Equal(t, p1, nestedset.LCA(p1))
}

// TestPath_ScannerValuerJSON 验证数据库 Driver 与 JSON 序列化。
func TestPath_ScannerValuerJSON(t *testing.T) {
	p := nestedset.MustPath("Org", "Dev")

	// Value
	val, err := p.Value()
	require.NoError(t, err)
	assert.Equal(t, "Org.Dev", val)

	var empty nestedset.Path
	emptyVal, err := empty.Value()
	require.NoError(t, err)
	assert.Nil(t, emptyVal)

	// Scan string
	var scanned nestedset.Path
	err = scanned.Scan("Org.Dev")
	require.NoError(t, err)
	assert.Equal(t, "Org.Dev", scanned.String())

	// Scan []byte
	err = scanned.Scan([]byte("Org.QA"))
	require.NoError(t, err)
	assert.Equal(t, "Org.QA", scanned.String())

	// Scan nil
	err = scanned.Scan(nil)
	require.NoError(t, err)
	assert.True(t, scanned.IsEmpty())

	// JSON Marshal
	bytes, err := json.Marshal(p)
	require.NoError(t, err)
	assert.Equal(t, `"Org.Dev"`, string(bytes))

	// JSON Unmarshal 字符串
	var unmarshaled nestedset.Path
	err = json.Unmarshal([]byte(`"Org.Dev"`), &unmarshaled)
	require.NoError(t, err)
	assert.Equal(t, "Org.Dev", unmarshaled.String())

	// JSON Unmarshal 字符串数组
	var unmarshaledArr nestedset.Path
	err = json.Unmarshal([]byte(`["Org", "Dev"]`), &unmarshaledArr)
	require.NoError(t, err)
	assert.Equal(t, "Org.Dev", unmarshaledArr.String())
}

// TestPath_BunQuery 验证针对 Bun ORM SelectQuery/UpdateQuery 渲染生成的 ltree SQL 表达式。
func TestPath_BunQuery(t *testing.T) {
	db := bun.NewDB(&sql.DB{}, pgdialect.New())

	type Department struct {
		ID   int64          `bun:"id,pk"`
		Path nestedset.Path `bun:"path"`
	}

	target := nestedset.MustPath("Top", "Tech")

	// 1. WhereDescendantOf (<@)
	q1 := db.NewSelect().Model((*Department)(nil))
	nestedset.WhereDescendantOf(q1, "path", target)
	sqlStr1 := q1.String()
	assert.Contains(t, sqlStr1, `"path"::ltree <@ 'Top.Tech'::ltree`)

	// 2. WhereAncestorOf (@>)
	q2 := db.NewSelect().Model((*Department)(nil))
	nestedset.WhereAncestorOf(q2, "path", target)
	sqlStr2 := q2.String()
	assert.Contains(t, sqlStr2, `"path"::ltree @> 'Top.Tech'::ltree`)

	// 3. WhereDirectChildren
	q3 := db.NewSelect().Model((*Department)(nil))
	nestedset.WhereDirectChildren(q3, "path", target)
	sqlStr3 := q3.String()
	assert.Contains(t, sqlStr3, `"path"::ltree <@ 'Top.Tech'::ltree AND nlevel("path"::ltree) = nlevel('Top.Tech'::ltree) + 1`)

	// 4. WhereMatchLQuery (~)
	q4 := db.NewSelect().Model((*Department)(nil))
	nestedset.WhereMatchLQuery(q4, "path", "*.Tech.*")
	sqlStr4 := q4.String()
	assert.Contains(t, sqlStr4, `"path"::ltree ~ '*.Tech.*'::lquery`)

	// 5. OrderByPath & OrderByDepth
	q5 := db.NewSelect().Model((*Department)(nil))
	nestedset.OrderByPath(q5, "path", true)
	nestedset.OrderByDepth(q5, "path", false)
	sqlStr5 := q5.String()
	assert.Contains(t, sqlStr5, `ORDER BY "path"::ltree ASC, nlevel("path"::ltree) DESC`)

	// 6. ApplyMoveSubtree 原子子树迁移 UPDATE 渲染
	oldPrefix := nestedset.MustPath("Top", "OldDept")
	newPrefix := nestedset.MustPath("Top", "NewDept")
	q6 := db.NewUpdate().Model((*Department)(nil))
	nestedset.ApplyMoveSubtree(q6, "path", oldPrefix, newPrefix)
	sqlStr6 := q6.String()
	assert.Contains(t, sqlStr6, `SET "path" = 'Top.NewDept'::ltree || subpath("path", nlevel('Top.OldDept'::ltree))`)
	assert.Contains(t, sqlStr6, `WHERE ("path"::ltree <@ 'Top.OldDept'::ltree)`)
}

// TestTree_BuildAndFlatten 验证泛型平铺列表转嵌套树与前序扁平化。
func TestTree_BuildAndFlatten(t *testing.T) {
	type Category struct {
		ID   int
		Name string
		Path nestedset.Path
	}

	items := []Category{
		{ID: 1, Name: "Elec", Path: nestedset.MustPath("1")},
		{ID: 2, Name: "Phone", Path: nestedset.MustPath("1", "2")},
		{ID: 3, Name: "iPhone", Path: nestedset.MustPath("1", "2", "3")},
		{ID: 4, Name: "Laptop", Path: nestedset.MustPath("1", "4")},
		{ID: 5, Name: "Book", Path: nestedset.MustPath("5")},
		{ID: 6, Name: "Novel", Path: nestedset.MustPath("5", "6")},
	}

	// 1. 构建多叉树
	tree := nestedset.BuildTree(items, func(c Category) nestedset.Path {
		return c.Path
	})

	require.Len(t, tree, 2) // 根节点 Elec(1) 与 Book(5)

	// 验证 Elec 分支
	elecNode := tree[0]
	assert.Equal(t, 1, elecNode.Data.ID)
	require.Len(t, elecNode.Children, 2) // Phone(2) 与 Laptop(4)

	phoneNode := elecNode.Children[0]
	assert.Equal(t, 2, phoneNode.Data.ID)
	require.Len(t, phoneNode.Children, 1) // iPhone(3)
	assert.Equal(t, 3, phoneNode.Children[0].Data.ID)

	laptopNode := elecNode.Children[1]
	assert.Equal(t, 4, laptopNode.Data.ID)
	assert.Empty(t, laptopNode.Children)

	// 验证 Book 分支
	bookNode := tree[1]
	assert.Equal(t, 5, bookNode.Data.ID)
	require.Len(t, bookNode.Children, 1) // Novel(6)
	assert.Equal(t, 6, bookNode.Children[0].Data.ID)

	// 2. 前序遍历扁平化回环
	flattened := nestedset.FlattenTree(tree)
	require.Len(t, flattened, 6)
	expectedOrder := []int{1, 2, 3, 4, 5, 6}
	for i, item := range flattened {
		assert.Equal(t, expectedOrder[i], item.ID)
	}

	// 3. 空数据集与局部子树健全性
	assert.Nil(t, nestedset.BuildTree([]Category{}, nil))
	assert.Nil(t, nestedset.FlattenTree[Category](nil))

	// 局部子树（父级 1 缺失）：Phone(1.2) 和 iPhone(1.2.3)，Phone 应自动提升为局部根
	subItems := []Category{
		{ID: 2, Name: "Phone", Path: nestedset.MustPath("1", "2")},
		{ID: 3, Name: "iPhone", Path: nestedset.MustPath("1", "2", "3")},
	}
	subTree := nestedset.BuildTree(subItems, func(c Category) nestedset.Path {
		return c.Path
	})
	require.Len(t, subTree, 1)
	assert.Equal(t, 2, subTree[0].Data.ID)
	require.Len(t, subTree[0].Children, 1)
	assert.Equal(t, 3, subTree[0].Children[0].Data.ID)
}

// 确保符合 sql.Scanner, driver.Valuer, json.Marshaler, json.Unmarshaler 契约的编译期检查
var (
	_ sql.Scanner      = (*nestedset.Path)(nil)
	_ driver.Valuer    = (*nestedset.Path)(nil)
	_ json.Marshaler   = (*nestedset.Path)(nil)
	_ json.Unmarshaler = (*nestedset.Path)(nil)
)
