package geo_test

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/ewkb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"bgin/internal/platform/pgtype/geo"
)

// TestPoint_Basic 验证空间点的构造、访问器、修改器及 WKT 格式化。
func TestPoint_Basic(t *testing.T) {
	pt := geo.NewPoint(121.4737, 31.2304)
	assert.InDelta(t, 121.4737, pt.Lng(), 1e-6)
	assert.InDelta(t, 31.2304, pt.Lat(), 1e-6)
	assert.Equal(t, geo.DefaultSRID, pt.SRID)
	assert.False(t, pt.IsZero())

	pt.SetLng(116.4074)
	pt.SetLat(39.9042)
	assert.InDelta(t, 116.4074, pt.Lng(), 1e-6)
	assert.InDelta(t, 39.9042, pt.Lat(), 1e-6)

	// 不可变值对象方法验证 (WithLng, WithLat)
	ptImmutable := pt.WithLng(113.2644).WithLat(23.1291)
	assert.InDelta(t, 113.2644, ptImmutable.Lng(), 1e-6)
	assert.InDelta(t, 23.1291, ptImmutable.Lat(), 1e-6)
	// 原 pt 保持不变
	assert.InDelta(t, 116.4074, pt.Lng(), 1e-6)
	assert.InDelta(t, 39.9042, pt.Lat(), 1e-6)

	// 自定义 SRID 验证
	ptCustom := geo.NewPointWithSRID(120.0, 30.0, geo.SRIDCGCS2000)
	assert.Equal(t, geo.SRIDCGCS2000, ptCustom.SRID)

	// 零值点验证
	zeroPt := geo.Point{}
	assert.True(t, zeroPt.IsZero())

	// WKT 格式化
	wktStr := pt.String()
	assert.True(t, strings.HasPrefix(wktStr, "POINT"))
	assert.Contains(t, wktStr, "116.4074")
	assert.Contains(t, wktStr, "39.9042")

	// 附带 SRID 的 WKT
	sridWKT := geo.FormatWKT(pt.Point, pt.SRID)
	assert.Equal(t, "SRID=4326;POINT(116.4074 39.9042)", sridWKT)
}

// TestPoint_ScannerValuer 验证 Point 满足 sql.Scanner 与 driver.Valuer，支持 EWKB 回环与 Hex 解码。
func TestPoint_ScannerValuer(t *testing.T) {
	origin := geo.NewPoint(121.4737, 31.2304)

	// 1. Value() 产出二进制 EWKB
	val, err := origin.Value()
	require.NoError(t, err)
	rawBytes, ok := val.([]byte)
	require.True(t, ok)
	require.NotEmpty(t, rawBytes)

	// 2. Scan() 原生二进制 EWKB
	var scanned geo.Point
	err = scanned.Scan(rawBytes)
	require.NoError(t, err)
	assert.InDelta(t, origin.Lng(), scanned.Lng(), 1e-6)
	assert.InDelta(t, origin.Lat(), scanned.Lat(), 1e-6)
	assert.Equal(t, geo.DefaultSRID, scanned.SRID)

	// 3. Scan() 十六进制 EWKB 字符串（模拟 PostGIS 文本传输）
	hexStr, err := ewkb.MarshalToHex(origin.Point, origin.SRID)
	require.NoError(t, err)
	var hexScanned geo.Point
	err = hexScanned.Scan(hexStr)
	require.NoError(t, err)
	assert.InDelta(t, origin.Lng(), hexScanned.Lng(), 1e-6)
	assert.InDelta(t, origin.Lat(), hexScanned.Lat(), 1e-6)

	// 4. Scan() PostgreSQL \x 前缀的十六进制
	pgHex := "\\x" + hexStr
	var pgHexScanned geo.Point
	err = pgHexScanned.Scan(pgHex)
	require.NoError(t, err)
	assert.InDelta(t, origin.Lng(), pgHexScanned.Lng(), 1e-6)
	assert.InDelta(t, origin.Lat(), pgHexScanned.Lat(), 1e-6)

	// 5. Scan() WKT 字符串与带 SRID 的 WKT
	var wktScanned geo.Point
	err = wktScanned.Scan("POINT(121.4737 31.2304)")
	require.NoError(t, err)
	assert.InDelta(t, 121.4737, wktScanned.Lng(), 1e-4)

	var sridWKTScanned geo.Point
	err = sridWKTScanned.Scan("SRID=3857;POINT(121.4737 31.2304)")
	require.NoError(t, err)
	assert.Equal(t, 3857, sridWKTScanned.SRID)

	// 6. Scan(nil) 清空
	err = scanned.Scan(nil)
	require.NoError(t, err)
	assert.True(t, scanned.IsZero())

	// 7. 零值 Value() 应返回 nil
	zeroVal, err := geo.Point{}.Value()
	require.NoError(t, err)
	assert.Nil(t, zeroVal)
}

// TestPoint_JSON 验证 Point 的 RFC 7946 规范输出与宽容反序列化。
func TestPoint_JSON(t *testing.T) {
	pt := geo.NewPoint(121.4737, 31.2304)

	// 1. Marshal 严格遵循 GeoJSON
	data, err := json.Marshal(pt)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"Point","coordinates":[121.4737,31.2304]}`, string(data))

	// 2. Unmarshal: 标准 GeoJSON
	var p1 geo.Point
	err = json.Unmarshal([]byte(`{"type":"Point","coordinates":[121.4737,31.2304]}`), &p1)
	require.NoError(t, err)
	assert.InDelta(t, 121.4737, p1.Lng(), 1e-6)
	assert.InDelta(t, 31.2304, p1.Lat(), 1e-6)

	// 3. 宽容 Unmarshal: 纯坐标数组 [lng, lat]
	var p2 geo.Point
	err = json.Unmarshal([]byte(`[116.4074, 39.9042]`), &p2)
	require.NoError(t, err)
	assert.InDelta(t, 116.4074, p2.Lng(), 1e-6)
	assert.InDelta(t, 39.9042, p2.Lat(), 1e-6)

	// 4. 宽容 Unmarshal: 经纬度键值对象 {lng, lat}
	var p3 geo.Point
	err = json.Unmarshal([]byte(`{"lng": 113.2644, "lat": 23.1291}`), &p3)
	require.NoError(t, err)
	assert.InDelta(t, 113.2644, p3.Lng(), 1e-6)
	assert.InDelta(t, 23.1291, p3.Lat(), 1e-6)

	// 5. 宽容 Unmarshal: 完整名称经纬度 {longitude, latitude}
	var p4 geo.Point
	err = json.Unmarshal([]byte(`{"longitude": 114.0579, "latitude": 22.5431}`), &p4)
	require.NoError(t, err)
	assert.InDelta(t, 114.0579, p4.Lng(), 1e-6)
	assert.InDelta(t, 22.5431, p4.Lat(), 1e-6)

	// 6. 宽容 Unmarshal: WKT 字符串
	var p5 geo.Point
	err = json.Unmarshal([]byte(`"POINT(120.1551 30.2741)"`), &p5)
	require.NoError(t, err)
	assert.InDelta(t, 120.1551, p5.Lng(), 1e-6)
	assert.InDelta(t, 30.2741, p5.Lat(), 1e-6)

	// 7. 宽容 Unmarshal: 逗号分隔字符串
	var p6 geo.Point
	err = json.Unmarshal([]byte(`"121.5, 31.2"`), &p6)
	require.NoError(t, err)
	assert.InDelta(t, 121.5, p6.Lng(), 1e-6)
	assert.InDelta(t, 31.2, p6.Lat(), 1e-6)

	// 8. null 处理
	var pNull geo.Point
	err = json.Unmarshal([]byte(`null`), &pNull)
	require.NoError(t, err)
	assert.True(t, pNull.IsZero())
}

// TestLineString_Operations 验证折线的创建、编解码、长度与 WKT 输出。
func TestLineString_Operations(t *testing.T) {
	// 构建一条从上海到杭州再到宁波的轨迹
	p1 := orb.Point{121.4737, 31.2304} // 上海
	p2 := orb.Point{120.1551, 30.2741} // 杭州
	p3 := orb.Point{121.5498, 29.8683} // 宁波

	ls := geo.NewLineString(p1, p2, p3)
	assert.Len(t, ls.LineString, 3)
	assert.Equal(t, geo.DefaultSRID, ls.SRID)

	// 计算轨迹总长度（上海-杭州-宁波约 300+ 公里）
	lengthMeters := ls.Length()
	assert.True(t, lengthMeters > 250000 && lengthMeters < 400000, "轨迹长度应在 250km ~ 400km 之间")

	// Value 与 Scan 回环
	val, err := ls.Value()
	require.NoError(t, err)
	var scanned geo.LineString
	err = scanned.Scan(val)
	require.NoError(t, err)
	assert.Len(t, scanned.LineString, 3)

	// GeoJSON 序列化与反序列化
	jsonBytes, err := json.Marshal(ls)
	require.NoError(t, err)
	var fromJSON geo.LineString
	err = json.Unmarshal(jsonBytes, &fromJSON)
	require.NoError(t, err)
	assert.Len(t, fromJSON.LineString, 3)

	// WKT
	assert.True(t, strings.HasPrefix(ls.String(), "LINESTRING"))
}

// TestPolygon_ElectronicFence 验证多边形电子围栏、点包含检测、面积与质心。
func TestPolygon_ElectronicFence(t *testing.T) {
	// 构造一个包含上海外滩与人民广场的闭合多边形区域 (外环顺时针或逆时针闭合)
	ring := orb.Ring{
		{121.45, 31.22},
		{121.50, 31.22},
		{121.50, 31.25},
		{121.45, 31.25},
		{121.45, 31.22}, // 闭合点
	}
	poly := geo.NewPolygon(ring)

	// 1. 点在围栏内
	insidePoint := geo.NewPoint(121.4737, 31.2304) // 人民广场
	assert.True(t, poly.Contains(insidePoint))
	assert.True(t, geo.Contains(poly, insidePoint))

	// 2. 点在围栏外
	outsidePoint := geo.NewPoint(121.60, 31.30) // 浦东高桥区域
	assert.False(t, poly.Contains(outsidePoint))
	assert.False(t, geo.Contains(poly, outsidePoint))

	// 3. 面积计算 (经度跨度 0.05 度约 4.7km，纬度 0.03 度约 3.3km，面积约 15 平方公里 = 15,000,000 m²)
	area := poly.Area()
	assert.True(t, area > 10000000 && area < 20000000, "面积计算结果应约为 15 平方公里")

	// 4. 质心计算
	centroid := poly.Centroid()
	assert.InDelta(t, 121.475, centroid.Lng(), 0.005)
	assert.InDelta(t, 31.235, centroid.Lat(), 0.005)

	// 5. 数据库 Scanner/Valuer 回环
	val, err := poly.Value()
	require.NoError(t, err)
	var scanned geo.Polygon
	err = scanned.Scan(val)
	require.NoError(t, err)
	assert.Len(t, scanned.Polygon, 1)
	assert.Len(t, scanned.Polygon[0], 5)
	assert.True(t, scanned.Contains(insidePoint))

	// 6. JSON GeoJSON 序列化与反序列化
	jsonBytes, err := json.Marshal(poly)
	require.NoError(t, err)
	var unmarshaled geo.Polygon
	err = json.Unmarshal(jsonBytes, &unmarshaled)
	require.NoError(t, err)
	assert.True(t, unmarshaled.Contains(insidePoint))
}

// TestMultiPolygon_Operations 验证复合多边形（如包含多块岛屿或多个隔离配送区）。
func TestMultiPolygon_Operations(t *testing.T) {
	// 区域 A
	ringA := orb.Ring{
		{121.0, 31.0},
		{121.1, 31.0},
		{121.1, 31.1},
		{121.0, 31.1},
		{121.0, 31.0},
	}
	// 区域 B
	ringB := orb.Ring{
		{121.5, 31.5},
		{121.6, 31.5},
		{121.6, 31.6},
		{121.5, 31.6},
		{121.5, 31.5},
	}

	mp := geo.NewMultiPolygon(orb.Polygon{ringA}, orb.Polygon{ringB})
	assert.Len(t, mp.MultiPolygon, 2)

	// 点在 A 区域中
	ptA := geo.NewPoint(121.05, 31.05)
	assert.True(t, mp.Contains(ptA))

	// 点在 B 区域中
	ptB := geo.NewPoint(121.55, 31.55)
	assert.True(t, mp.Contains(ptB))

	// 点在 A、B 区域外
	ptOutside := geo.NewPoint(121.3, 31.3)
	assert.False(t, mp.Contains(ptOutside))

	// 面积大于 0
	assert.Positive(t, mp.Area())

	// Scan/Value 回环
	val, err := mp.Value()
	require.NoError(t, err)
	var scanned geo.MultiPolygon
	err = scanned.Scan(val)
	require.NoError(t, err)
	assert.True(t, scanned.Contains(ptA))
	assert.True(t, scanned.Contains(ptB))
}

// TestGeometry_Polymorphic 验证通用 Geometry 容器的多态兼容性。
func TestGeometry_Polymorphic(t *testing.T) {
	pt := geo.NewPoint(121.47, 31.23)
	geom := geo.NewGeometry(pt.Point)
	assert.Equal(t, geo.DefaultSRID, geom.SRID)

	// 验证 Value 与 Scan
	val, err := geom.Value()
	require.NoError(t, err)
	var scanned geomHolder
	err = scanned.G.Scan(val)
	require.NoError(t, err)
	assert.NotNil(t, scanned.G.Geometry)

	// 验证 GeoJSON 序列化
	data, err := json.Marshal(geom)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"Point"`)

	// 容错反序列化 WKT
	var fromWKT geo.Geometry
	err = json.Unmarshal([]byte(`"POLYGON((121 31, 122 31, 122 32, 121 32, 121 31))"`), &fromWKT)
	require.NoError(t, err)
	assert.NotNil(t, fromWKT.Geometry)
	assert.True(t, fromWKT.Contains(geo.NewPoint(121.5, 31.5)))
}

type geomHolder struct {
	G geo.Geometry
}

// TestSpatialCalculations_Distance 验证大圆米级距离计算与精度基准。
func TestSpatialCalculations_Distance(t *testing.T) {
	shanghai := geo.NewPoint(121.4737, 31.2304) // 上海 (121.4737, 31.2304)
	beijing := geo.NewPoint(116.4074, 39.9042)  // 北京 (116.4074, 39.9042)

	// 计算上海到北京的大圆距离，官方测绘真实地表距离约为 1,068 公里 ~ 1,070 公里
	dist := geo.Distance(shanghai, beijing)
	distKM := dist / 1000.0

	assert.InDelta(t, 1068.0, distKM, 15.0, "上海至北京距离应在约 1068km 左右 (误差 < 15km)")
	assert.InDelta(t, dist, shanghai.DistanceTo(beijing), 1e-6)

	// 自测：同一点距离必须为 0
	zeroDist := geo.Distance(shanghai, shanghai)
	assert.InDelta(t, 0.0, zeroDist, 1e-6)

	// 包围盒扩展测试
	bound := geo.NewBoundAroundPoint(shanghai, 5000) // 5 公里外接矩形
	assert.True(t, geo.InBound(bound, shanghai))
	assert.False(t, geo.InBound(bound, beijing))
}

// TestPostGISQueryHelpers 验证 Bun ORM SQL 构建表达式与链式查询构造。
func TestPostGISQueryHelpers(t *testing.T) {
	// 创建内存虚拟 Bun DB（基于 PostgreSQL 方言）
	db := bun.NewDB(&sql.DB{}, pgdialect.New())

	shanghai := geo.NewPoint(121.4737, 31.2304)

	// 1. 验证 SQLMakePoint 生成
	sqlPt, argsPt := geo.SQLMakePoint(shanghai)
	assert.Equal(t, "ST_SetSRID(ST_MakePoint(?, ?), ?)", sqlPt)
	assert.Equal(t, []any{121.4737, 31.2304, 4326}, argsPt)

	// 2. 验证 SQLDWithin 范围搜索生成
	sqlDWithin, argsDWithin := geo.SQLDWithin("store.location", shanghai, 5000)
	assert.Equal(t, "ST_DWithin(?::geography, ST_SetSRID(ST_MakePoint(?, ?), ?)::geography, ?)", sqlDWithin)
	assert.Len(t, argsDWithin, 5)

	// 3. 验证 SQLContains 生成
	sqlContains, argsContains := geo.SQLContains("zone.delivery_area", shanghai)
	assert.Equal(t, "ST_Contains(?::geometry, ST_SetSRID(ST_MakePoint(?, ?), ?))", sqlContains)
	assert.Len(t, argsContains, 4)

	// 4. 验证在 Bun SelectQuery 上的流畅链式调用与完整 SQL 拼接
	type Store struct {
		bun.BaseModel `bun:"table:stores"`
		ID            string      `bun:"id,pk"`
		Name          string      `bun:"name"`
		Location      geo.Point   `bun:"location,type:geometry(Point, 4326)"`
		ServiceArea   geo.Polygon `bun:"service_area,type:geometry(Polygon, 4326)"`
	}

	q := db.NewSelect().Model((*Store)(nil))
	q = geo.WhereDWithin(q, "location", shanghai, 3000)
	q = geo.WhereContains(q, "service_area", shanghai)
	q = geo.WhereWithinBox(q, "location", 121.0, 31.0, 122.0, 32.0)
	q = geo.OrderByDistance(q, "location", shanghai, true)
	q = geo.ColumnDistance(q, "location", shanghai, "dist_meters")

	renderedSQL := q.String()
	assert.Contains(t, renderedSQL, "ST_DWithin")
	assert.Contains(t, renderedSQL, "ST_Contains")
	assert.Contains(t, renderedSQL, "ST_MakeEnvelope")
	assert.Contains(t, renderedSQL, "ST_Distance")
	assert.Contains(t, renderedSQL, "ASC")
	assert.Contains(t, renderedSQL, "dist_meters")

	// 验证自解释排序别名辅助函数
	qAsc := db.NewSelect().Model((*Store)(nil))
	qAsc = geo.OrderByDistanceAsc(qAsc, "location", shanghai)
	assert.Contains(t, qAsc.String(), "ASC")

	qDesc := db.NewSelect().Model((*Store)(nil))
	qDesc = geo.OrderByDistanceDesc(qDesc, "location", shanghai)
	assert.Contains(t, qDesc.String(), "DESC")
}

// 确保符合 sql.Scanner 与 driver.Valuer 接口契约的编译期检查
var (
	_ sql.Scanner   = (*geo.Point)(nil)
	_ driver.Valuer = (*geo.Point)(nil)

	_ sql.Scanner   = (*geo.LineString)(nil)
	_ driver.Valuer = (*geo.LineString)(nil)

	_ sql.Scanner   = (*geo.Polygon)(nil)
	_ driver.Valuer = (*geo.Polygon)(nil)

	_ sql.Scanner   = (*geo.MultiPolygon)(nil)
	_ driver.Valuer = (*geo.MultiPolygon)(nil)

	_ sql.Scanner   = (*geo.Geometry)(nil)
	_ driver.Valuer = (*geo.Geometry)(nil)
)

// 消除未使用引用的保护
var _ = math.Abs
