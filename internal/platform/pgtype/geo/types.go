package geo

import (
	"fmt"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/wkt"
)

// Point 表示二维空间地理坐标点，内嵌 orb.Point（[2]float64，索引 0 为经度 Longitude，索引 1 为纬度 Latitude）。
type Point struct {
	orb.Point
	SRID int `json:"srid,omitempty"`
}

// NewPoint 构造指定经纬度的空间点，默认坐标系为 WGS 84 (SRID 4326)。
func NewPoint(lng, lat float64) Point {
	return Point{
		Point: orb.Point{lng, lat},
		SRID:  DefaultSRID,
	}
}

// NewPointWithSRID 构造附带显式空间参考系 SRID 的空间点。
func NewPointWithSRID(lng, lat float64, srid int) Point {
	return Point{
		Point: orb.Point{lng, lat},
		SRID:  NormalizeSRID(srid),
	}
}

// Lng 获取经度 (X 轴坐标)。
func (p Point) Lng() float64 {
	return p.Point[0]
}

// Lat 获取纬度 (Y 轴坐标)。
func (p Point) Lat() float64 {
	return p.Point[1]
}

// SetLng 设置经度坐标。在值对象传递场景推荐使用 WithLng。
func (p *Point) SetLng(lng float64) {
	p.Point[0] = lng
}

// SetLat 设置纬度坐标。在值对象传递场景推荐使用 WithLat。
func (p *Point) SetLat(lat float64) {
	p.Point[1] = lat
}

// WithLng 返回更新经度坐标后的新空间点副本（不可变值对象范式）。
func (p Point) WithLng(lng float64) Point {
	p.Point[0] = lng
	return p
}

// WithLat 返回更新纬度坐标后的新空间点副本（不可变值对象范式）。
func (p Point) WithLat(lat float64) Point {
	p.Point[1] = lat
	return p
}

// IsZero 判定当前点坐标是否全为零且未定义有效空间标识。
func (p Point) IsZero() bool {
	return p.Point[0] == 0 && p.Point[1] == 0 && p.SRID == 0
}

// String 输出该点坐标的 WKT (Well-Known Text) 表达（如 "POINT(121.4737 31.2304)"）。
func (p Point) String() string {
	return wkt.MarshalString(p.Point)
}

// LineString 表示有序的空间折线/轨迹集合，内嵌 orb.LineString。
type LineString struct {
	orb.LineString
	SRID int `json:"srid,omitempty"`
}

// NewLineString 构造空间折线，默认坐标系为 WGS 84。
func NewLineString(pts ...orb.Point) LineString {
	return LineString{
		LineString: orb.LineString(pts),
		SRID:       DefaultSRID,
	}
}

// NewLineStringWithSRID 构造附带显式 SRID 的空间折线。
func NewLineStringWithSRID(pts []orb.Point, srid int) LineString {
	return LineString{
		LineString: orb.LineString(pts),
		SRID:       NormalizeSRID(srid),
	}
}

// String 输出该折线的 WKT 表达。
func (ls LineString) String() string {
	return wkt.MarshalString(ls.LineString)
}

// Polygon 表示由外环和零个或多个内环（空洞）围成的空间多边形，内嵌 orb.Polygon。
type Polygon struct {
	orb.Polygon
	SRID int `json:"srid,omitempty"`
}

// NewPolygon 构造空间多边形，默认坐标系为 WGS 84。
func NewPolygon(rings ...orb.Ring) Polygon {
	return Polygon{
		Polygon: orb.Polygon(rings),
		SRID:    DefaultSRID,
	}
}

// NewPolygonWithSRID 构造附带显式 SRID 的空间多边形。
func NewPolygonWithSRID(rings []orb.Ring, srid int) Polygon {
	return Polygon{
		Polygon: orb.Polygon(rings),
		SRID:    NormalizeSRID(srid),
	}
}

// String 输出多边形的 WKT 表达。
func (poly Polygon) String() string {
	return wkt.MarshalString(poly.Polygon)
}

// MultiPolygon 表示由多个互不重叠的多边形构成的复合几何区划，内嵌 orb.MultiPolygon。
type MultiPolygon struct {
	orb.MultiPolygon
	SRID int `json:"srid,omitempty"`
}

// NewMultiPolygon 构造复合空间多边形，默认坐标系为 WGS 84。
func NewMultiPolygon(polys ...orb.Polygon) MultiPolygon {
	return MultiPolygon{
		MultiPolygon: orb.MultiPolygon(polys),
		SRID:         DefaultSRID,
	}
}

// NewMultiPolygonWithSRID 构造附带显式 SRID 的复合多边形。
func NewMultiPolygonWithSRID(polys []orb.Polygon, srid int) MultiPolygon {
	return MultiPolygon{
		MultiPolygon: orb.MultiPolygon(polys),
		SRID:         NormalizeSRID(srid),
	}
}

// String 输出复合多边形的 WKT 表达。
func (mp MultiPolygon) String() string {
	return wkt.MarshalString(mp.MultiPolygon)
}

// Geometry 表示通用的多态空间几何容器，可承载点、线、面及复合空间对象。
type Geometry struct {
	orb.Geometry
	SRID int `json:"srid,omitempty"`
}

// NewGeometry 构造通用几何容器，默认坐标系为 WGS 84。
func NewGeometry(geom orb.Geometry) Geometry {
	return Geometry{
		Geometry: geom,
		SRID:     DefaultSRID,
	}
}

// NewGeometryWithSRID 构造附带显式 SRID 的通用几何容器。
func NewGeometryWithSRID(geom orb.Geometry, srid int) Geometry {
	return Geometry{
		Geometry: geom,
		SRID:     NormalizeSRID(srid),
	}
}

// String 输出几何对象的 WKT 表达。
func (g Geometry) String() string {
	if g.Geometry == nil {
		return ""
	}
	return wkt.MarshalString(g.Geometry)
}

// FormatWKT 带 SRID 前缀格式化输出 WKT（形如 "SRID=4326;POINT(121.47 31.23)"）。
func FormatWKT(g orb.Geometry, srid int) string {
	if g == nil {
		return ""
	}
	srid = NormalizeSRID(srid)
	return fmt.Sprintf("SRID=%d;%s", srid, wkt.MarshalString(g))
}
