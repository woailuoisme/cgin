package geo

import (
	"github.com/paulmach/orb"
	orbgeo "github.com/paulmach/orb/geo"
	"github.com/paulmach/orb/planar"
)

// Distance 计算两点在 WGS 84 地球椭球体表面上的大圆距离，单位：米 (Meters)。
func Distance(p1, p2 Point) float64 {
	return orbgeo.Distance(p1.Point, p2.Point)
}

// DistanceHaversine 采用 Haversine 公式计算两点大圆距离，单位：米。
func DistanceHaversine(p1, p2 Point) float64 {
	return orbgeo.DistanceHaversine(p1.Point, p2.Point)
}

// DistanceTo 计算当前点与目标点的地表大圆距离，单位：米。
func (p Point) DistanceTo(other Point) float64 {
	return Distance(p, other)
}

// Length 计算折线轨迹在 WGS 84 球面上的总几何长度，单位：米。
func Length(ls LineString) float64 {
	return orbgeo.Length(ls.LineString)
}

// Length 计算当前折线的地表总长度，单位：米。
func (ls LineString) Length() float64 {
	return Length(ls)
}

// Area 计算多边形在 WGS 84 球面上的投影面积，单位：平方米 (Square Meters)。
func Area(poly Polygon) float64 {
	return orbgeo.Area(poly.Polygon)
}

// Area 计算当前多边形的球面地表面积，单位：平方米。
func (poly Polygon) Area() float64 {
	return Area(poly)
}

// MultiPolygonArea 计算复合多边形的总面积，单位：平方米。
func MultiPolygonArea(mp MultiPolygon) float64 {
	return orbgeo.Area(mp.MultiPolygon)
}

// Area 计算当前复合多边形的总面积，单位：平方米。
func (mp MultiPolygon) Area() float64 {
	return MultiPolygonArea(mp)
}

// Contains 判断点是否在多边形（Polygon 或 MultiPolygon）内部或边界上（电子围栏检测）。
func Contains(geom any, p Point) bool {
	switch g := geom.(type) {
	case Polygon:
		return planar.PolygonContains(g.Polygon, p.Point)
	case *Polygon:
		if g == nil {
			return false
		}
		return planar.PolygonContains(g.Polygon, p.Point)
	case MultiPolygon:
		return planar.MultiPolygonContains(g.MultiPolygon, p.Point)
	case *MultiPolygon:
		if g == nil {
			return false
		}
		return planar.MultiPolygonContains(g.MultiPolygon, p.Point)
	case Geometry:
		return containsGeometry(g.Geometry, p.Point)
	case *Geometry:
		if g == nil {
			return false
		}
		return containsGeometry(g.Geometry, p.Point)
	case orb.Polygon:
		return planar.PolygonContains(g, p.Point)
	case orb.MultiPolygon:
		return planar.MultiPolygonContains(g, p.Point)
	case orb.Geometry:
		return containsGeometry(g, p.Point)
	default:
		return false
	}
}

func containsGeometry(g orb.Geometry, pt orb.Point) bool {
	if g == nil {
		return false
	}
	switch typed := g.(type) {
	case orb.Polygon:
		return planar.PolygonContains(typed, pt)
	case orb.MultiPolygon:
		return planar.MultiPolygonContains(typed, pt)
	default:
		return false
	}
}

// Contains 判断当前多边形是否包含指定空间点。
func (poly Polygon) Contains(p Point) bool {
	return planar.PolygonContains(poly.Polygon, p.Point)
}

// Contains 判断当前复合多边形是否包含指定空间点。
func (mp MultiPolygon) Contains(p Point) bool {
	return planar.MultiPolygonContains(mp.MultiPolygon, p.Point)
}

// Contains 判断当前通用几何对象是否包含指定空间点。
func (g Geometry) Contains(p Point) bool {
	return containsGeometry(g.Geometry, p.Point)
}

// Centroid 计算几何图形的质心点。
func Centroid(geom any) Point {
	var pt orb.Point
	switch g := geom.(type) {
	case Polygon:
		pt, _ = planar.CentroidArea(g.Polygon)
	case *Polygon:
		if g != nil {
			pt, _ = planar.CentroidArea(g.Polygon)
		}
	case MultiPolygon:
		pt, _ = planar.CentroidArea(g.MultiPolygon)
	case *MultiPolygon:
		if g != nil {
			pt, _ = planar.CentroidArea(g.MultiPolygon)
		}
	case Geometry:
		if g.Geometry != nil {
			pt, _ = planar.CentroidArea(g.Geometry)
		}
	case *Geometry:
		if g != nil && g.Geometry != nil {
			pt, _ = planar.CentroidArea(g.Geometry)
		}
	case orb.Geometry:
		if g != nil {
			pt, _ = planar.CentroidArea(g)
		}
	}
	return NewPoint(pt[0], pt[1])
}

// Centroid 计算当前多边形的质心。
func (poly Polygon) Centroid() Point {
	pt, _ := planar.CentroidArea(poly.Polygon)
	return NewPoint(pt[0], pt[1])
}

// Centroid 计算当前复合多边形的质心。
func (mp MultiPolygon) Centroid() Point {
	pt, _ := planar.CentroidArea(mp.MultiPolygon)
	return NewPoint(pt[0], pt[1])
}

// Centroid 计算当前几何容器的质心。
func (g Geometry) Centroid() Point {
	return Centroid(g)
}

// Bound 获取几何对象的最小外接矩形包围盒。
func Bound(geom any) orb.Bound {
	switch g := geom.(type) {
	case Point:
		return g.Bound()
	case LineString:
		return g.Bound()
	case Polygon:
		return g.Bound()
	case MultiPolygon:
		return g.Bound()
	case Geometry:
		if g.Geometry != nil {
			return g.Bound()
		}
	case orb.Geometry:
		if g != nil {
			return g.Bound()
		}
	}
	return orb.Bound{}
}

// NewBoundAroundPoint 计算以指定坐标点为中心、以指定米数为扩展距离的矩形包围盒。
func NewBoundAroundPoint(center Point, distanceMeters float64) orb.Bound {
	return orbgeo.NewBoundAroundPoint(center.Point, distanceMeters)
}

// InBound 判断点是否在矩形包围盒内。
func InBound(b orb.Bound, p Point) bool {
	return b.Contains(p.Point)
}
