// Package geo 提供基于 github.com/paulmach/orb 的 PostGIS 地理空间扩展底座、
// 强类型实体映射、EWKB/GeoJSON 驱动编解码、米级内存空间算法与 Bun 查询辅助器。
package geo

const (
	// DefaultSRID 默认空间参考系标识符（WGS 84，经纬度坐标系，全球 GPS/GIS 事实标准）。
	DefaultSRID = 4326

	// SRIDWGS84 WGS 84 经纬度地理坐标系 (EPSG:4326)。
	SRIDWGS84 = 4326

	// SRIDWebMercator Web 墨卡托投影坐标系 (EPSG:3857)，常用于电子地图瓦片渲染。
	SRIDWebMercator = 3857

	// SRIDCGCS2000 2000 国家大地坐标系 (EPSG:4490)，中国空间测绘标准。
	SRIDCGCS2000 = 4490
)

// NormalizeSRID 规范化 SRID 标识，若传入值 <= 0 则回退至默认的 WGS 84 (4326)。
func NormalizeSRID(srid int) int {
	if srid <= 0 {
		return DefaultSRID
	}
	return srid
}
