package geo

import (
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/encoding/ewkb"
	"github.com/paulmach/orb/encoding/wkb"
	"github.com/paulmach/orb/encoding/wkt"
	"github.com/paulmach/orb/geojson"
)

const jsonNull = "null"

// decodeGeometry 通用解析数据库/驱动层返回的空间数据，自适应适配 EWKB、WKB、十六进制字符串与 WKT。
func decodeGeometry(src any) (orb.Geometry, int, error) {
	if src == nil {
		return nil, DefaultSRID, nil
	}

	switch v := src.(type) {
	case []byte:
		return decodeBytes(v)
	case string:
		return decodeString(v)
	default:
		return nil, 0, fmt.Errorf("geo: unsupported scan source type %T", src)
	}
}

func decodeBytes(v []byte) (orb.Geometry, int, error) {
	if len(v) == 0 {
		return nil, DefaultSRID, nil
	}
	// 检查是否为 PostgreSQL 风格的 \x 前缀 Hex 编码
	if len(v) >= 2 && v[0] == '\\' && (v[1] == 'x' || v[1] == 'X') {
		decoded, err := hex.DecodeString(string(v[2:]))
		if err != nil {
			return nil, 0, fmt.Errorf("geo: failed to decode hex prefixed bytes: %w", err)
		}
		return decodeBinaryEWKB(decoded)
	}
	// 检查是否为纯 ASCII 十六进制文本
	if isHexString(v) {
		decoded, err := hex.DecodeString(string(v))
		if err == nil {
			if geom, srid, err := decodeBinaryEWKB(decoded); err == nil {
				return geom, srid, nil
			}
		}
	}
	// 尝试直接作为二进制 EWKB 解析
	if geom, srid, err := decodeBinaryEWKB(v); err == nil {
		return geom, srid, nil
	}
	// 尝试直接作为 WKT 文本解析
	if geom, err := wkt.Unmarshal(string(v)); err == nil {
		return geom, DefaultSRID, nil
	}
	return nil, 0, errors.New("geo: failed to unmarshal bytes as ewkb or wkt")
}

func decodeString(v string) (orb.Geometry, int, error) {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return nil, DefaultSRID, nil
	}
	// 检查是否包含 SRID 前缀（例如 "SRID=4326;POINT(121.47 31.23)"）
	srid := DefaultSRID
	if strings.HasPrefix(trimmed, "SRID=") || strings.HasPrefix(trimmed, "srid=") {
		if semi := strings.Index(trimmed, ";"); semi != -1 {
			sridPart := trimmed[5:semi]
			if parsedSRID, err := strconv.Atoi(sridPart); err == nil {
				srid = parsedSRID
			}
			trimmed = strings.TrimSpace(trimmed[semi+1:])
		}
	}
	// 尝试作为 WKT 文本解析
	if geom, err := wkt.Unmarshal(trimmed); err == nil {
		return geom, srid, nil
	}
	// 去除可能的 \x 前缀
	if strings.HasPrefix(trimmed, "\\x") || strings.HasPrefix(trimmed, "\\X") {
		trimmed = trimmed[2:]
	}
	// 尝试作为 Hex 编码的 EWKB 解析
	if isHexText(trimmed) {
		decoded, err := hex.DecodeString(trimmed)
		if err != nil {
			return nil, 0, fmt.Errorf("geo: invalid hex string: %w", err)
		}
		return decodeBinaryEWKB(decoded)
	}
	return nil, 0, fmt.Errorf("geo: unrecognized geometry string: %q", trimmed)
}

// decodeBinaryEWKB 解析原生二进制 EWKB/WKB。
func decodeBinaryEWKB(data []byte) (orb.Geometry, int, error) {
	geom, srid, err := ewkb.Unmarshal(data)
	if err == nil {
		return geom, NormalizeSRID(srid), nil
	}
	// 降级尝试标准 WKB
	geom, err = wkb.Unmarshal(data)
	if err == nil {
		return geom, DefaultSRID, nil
	}
	return nil, 0, err
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isHexString(b []byte) bool {
	if len(b) < 2 || len(b)%2 != 0 {
		return false
	}
	for _, c := range b {
		if !isHexByte(c) {
			return false
		}
	}
	return true
}

func isHexText(s string) bool {
	if len(s) < 2 || len(s)%2 != 0 {
		return false
	}
	for i := range len(s) {
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}

// encodeGeometry 将几何对象编码为数据库驱动可直接消费的二进制 EWKB。
func encodeGeometry(g orb.Geometry, srid int) (driver.Value, error) {
	if g == nil {
		//nolint:nilnil // driver.Valuer returns (nil, nil) to represent SQL NULL
		return nil, nil
	}
	srid = NormalizeSRID(srid)
	bytes, err := ewkb.Marshal(g, srid)
	if err != nil {
		return nil, fmt.Errorf("geo: failed to marshal ewkb: %w", err)
	}
	return bytes, nil
}

// Point Codec 实现

// Scan 实现 sql.Scanner 接口，从数据库读取空间点。
func (p *Point) Scan(src any) error {
	geom, srid, err := decodeGeometry(src)
	if err != nil {
		return err
	}
	if geom == nil {
		*p = Point{}
		return nil
	}
	pt, ok := geom.(orb.Point)
	if !ok {
		return fmt.Errorf("geo: scanned geometry type is %T, expected orb.Point", geom)
	}
	p.Point = pt
	p.SRID = srid
	return nil
}

// Value 实现 driver.Valuer 接口，将空间点输出为 EWKB 二进制供数据库存储。
func (p Point) Value() (driver.Value, error) {
	if p.IsZero() {
		//nolint:nilnil // driver.Valuer returns (nil, nil) to represent SQL NULL
		return nil, nil
	}
	return encodeGeometry(p.Point, p.SRID)
}

// MarshalJSON 实现 json.Marshaler 接口，序列化为规范 RFC 7946 GeoJSON 格式。
// 零值输出 null，与 Value() 落库为 SQL NULL 的语义保持对称，
// 避免把 (0,0) 这种非法经纬度当成有效坐标暴露给调用方。
func (p Point) MarshalJSON() ([]byte, error) {
	if p.IsZero() {
		return []byte(jsonNull), nil
	}

	type geoJSONPoint struct {
		Type        string     `json:"type"`
		Coordinates [2]float64 `json:"coordinates"`
	}
	return json.Marshal(geoJSONPoint{
		Type:        "Point",
		Coordinates: [2]float64{p.Point[0], p.Point[1]},
	})
}

// UnmarshalJSON 实现 json.Unmarshaler 接口，宽容解析 GeoJSON、[lng, lat] 数组或包含经纬度的对象。
func (p *Point) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == jsonNull {
		*p = Point{}
		return nil
	}

	// 1. 宽容解析：纯数组形式 [lng, lat]
	if strings.HasPrefix(trimmed, "[") {
		return p.unmarshalArray(data)
	}

	// 2. 宽容解析：字符串（WKT 格式或 "lng,lat" 逗号分隔格式）
	if strings.HasPrefix(trimmed, "\"") {
		return p.unmarshalString(data)
	}

	// 3. 规范 GeoJSON 或经纬度键值对象 {"lng":..., "lat":...}
	return p.unmarshalObject(data)
}

func (p *Point) unmarshalArray(data []byte) error {
	var coords [2]float64
	if err := json.Unmarshal(data, &coords); err != nil {
		return fmt.Errorf("geo: invalid coordinates array: %w", err)
	}
	p.Point = orb.Point{coords[0], coords[1]}
	p.SRID = DefaultSRID
	return nil
}

func (p *Point) unmarshalString(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	str = strings.TrimSpace(str)
	if strings.HasPrefix(strings.ToUpper(str), "POINT") {
		pt, err := wkt.UnmarshalPoint(str)
		if err != nil {
			return fmt.Errorf("geo: invalid wkt point: %w", err)
		}
		p.Point = pt
		p.SRID = DefaultSRID
		return nil
	}
	// 逗号分隔 lng,lat
	parts := strings.Split(str, ",")
	if len(parts) == 2 {
		lng, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		lat, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err1 == nil && err2 == nil {
			p.Point = orb.Point{lng, lat}
			p.SRID = DefaultSRID
			return nil
		}
	}
	return fmt.Errorf("geo: invalid point string: %q", str)
}

func (p *Point) unmarshalObject(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// 尝试作为 GeoJSON 解析
	if geomType, ok := raw["type"].(string); ok && strings.EqualFold(geomType, "Point") {
		geom, err := geojson.UnmarshalGeometry(data)
		if err == nil {
			if pt, ok := geom.Geometry().(orb.Point); ok {
				p.Point = pt
				p.SRID = DefaultSRID
				return nil
			}
		}
	}

	// 尝试作为 {"lng":..., "lat":...} 或 {"longitude":..., "latitude":...}
	lng, hasLng := extractCoord(raw, "lng", "longitude", "x")
	lat, hasLat := extractCoord(raw, "lat", "latitude", "y")
	if hasLng && hasLat {
		p.Point = orb.Point{lng, lat}
		p.SRID = DefaultSRID
		return nil
	}

	return errors.New("geo: cannot unmarshal JSON into Point (expected GeoJSON, [lng, lat], or {lng, lat})")
}

func extractCoord(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		if val, ok := m[k]; ok {
			switch num := val.(type) {
			case float64:
				return num, true
			case int:
				return float64(num), true
			case string:
				if f, err := strconv.ParseFloat(num, 64); err == nil {
					return f, true
				}
			}
		}
	}
	return 0, false
}

// LineString Codec 实现

// Scan 实现 sql.Scanner。
func (ls *LineString) Scan(src any) error {
	geom, srid, err := decodeGeometry(src)
	if err != nil {
		return err
	}
	if geom == nil {
		*ls = LineString{}
		return nil
	}
	line, ok := geom.(orb.LineString)
	if !ok {
		return fmt.Errorf("geo: scanned geometry type is %T, expected orb.LineString", geom)
	}
	ls.LineString = line
	ls.SRID = srid
	return nil
}

// Value 实现 driver.Valuer。
func (ls LineString) Value() (driver.Value, error) {
	if len(ls.LineString) == 0 {
		//nolint:nilnil // driver.Valuer returns (nil, nil) to represent SQL NULL
		return nil, nil
	}
	return encodeGeometry(ls.LineString, ls.SRID)
}

// MarshalJSON 序列化为 GeoJSON LineString。
func (ls LineString) MarshalJSON() ([]byte, error) {
	return json.Marshal(geojson.NewGeometry(ls.LineString))
}

// UnmarshalJSON 宽容反序列化 GeoJSON 或坐标点数组。
func (ls *LineString) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == jsonNull {
		*ls = LineString{}
		return nil
	}

	// 纯坐标点数组 [[lng, lat], ...]
	if strings.HasPrefix(trimmed, "[") {
		var pts []orb.Point
		if err := json.Unmarshal(data, &pts); err == nil {
			ls.LineString = orb.LineString(pts)
			ls.SRID = DefaultSRID
			return nil
		}
	}

	// 字符串 (WKT)
	if strings.HasPrefix(trimmed, "\"") {
		var str string
		if err := json.Unmarshal(data, &str); err == nil {
			line, err := wkt.UnmarshalLineString(str)
			if err == nil {
				ls.LineString = line
				ls.SRID = DefaultSRID
				return nil
			}
		}
	}

	geom, err := geojson.UnmarshalGeometry(data)
	if err != nil {
		return fmt.Errorf("geo: failed to unmarshal LineString JSON: %w", err)
	}
	line, ok := geom.Geometry().(orb.LineString)
	if !ok {
		return fmt.Errorf("geo: geojson geometry is %T, expected LineString", geom.Geometry())
	}
	ls.LineString = line
	ls.SRID = DefaultSRID
	return nil
}

// Polygon Codec 实现

// Scan 实现 sql.Scanner。
func (poly *Polygon) Scan(src any) error {
	geom, srid, err := decodeGeometry(src)
	if err != nil {
		return err
	}
	if geom == nil {
		*poly = Polygon{}
		return nil
	}
	p, ok := geom.(orb.Polygon)
	if !ok {
		return fmt.Errorf("geo: scanned geometry type is %T, expected orb.Polygon", geom)
	}
	poly.Polygon = p
	poly.SRID = srid
	return nil
}

// Value 实现 driver.Valuer。
func (poly Polygon) Value() (driver.Value, error) {
	if len(poly.Polygon) == 0 {
		//nolint:nilnil // driver.Valuer returns (nil, nil) to represent SQL NULL
		return nil, nil
	}
	return encodeGeometry(poly.Polygon, poly.SRID)
}

// MarshalJSON 序列化为 GeoJSON Polygon。
func (poly Polygon) MarshalJSON() ([]byte, error) {
	return json.Marshal(geojson.NewGeometry(poly.Polygon))
}

// UnmarshalJSON 宽容反序列化 GeoJSON 或多层坐标环数组。
func (poly *Polygon) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == jsonNull {
		*poly = Polygon{}
		return nil
	}

	// 纯坐标环数组 [[[lng, lat], ...]]
	if strings.HasPrefix(trimmed, "[") {
		var rings []orb.Ring
		if err := json.Unmarshal(data, &rings); err == nil {
			poly.Polygon = orb.Polygon(rings)
			poly.SRID = DefaultSRID
			return nil
		}
	}

	// 字符串 (WKT)
	if strings.HasPrefix(trimmed, "\"") {
		var str string
		if err := json.Unmarshal(data, &str); err == nil {
			p, err := wkt.UnmarshalPolygon(str)
			if err == nil {
				poly.Polygon = p
				poly.SRID = DefaultSRID
				return nil
			}
		}
	}

	geom, err := geojson.UnmarshalGeometry(data)
	if err != nil {
		return fmt.Errorf("geo: failed to unmarshal Polygon JSON: %w", err)
	}
	p, ok := geom.Geometry().(orb.Polygon)
	if !ok {
		return fmt.Errorf("geo: geojson geometry is %T, expected Polygon", geom.Geometry())
	}
	poly.Polygon = p
	poly.SRID = DefaultSRID
	return nil
}

// MultiPolygon Codec 实现

// Scan 实现 sql.Scanner。
func (mp *MultiPolygon) Scan(src any) error {
	geom, srid, err := decodeGeometry(src)
	if err != nil {
		return err
	}
	if geom == nil {
		*mp = MultiPolygon{}
		return nil
	}
	m, ok := geom.(orb.MultiPolygon)
	if !ok {
		return fmt.Errorf("geo: scanned geometry type is %T, expected orb.MultiPolygon", geom)
	}
	mp.MultiPolygon = m
	mp.SRID = srid
	return nil
}

// Value 实现 driver.Valuer。
func (mp MultiPolygon) Value() (driver.Value, error) {
	if len(mp.MultiPolygon) == 0 {
		//nolint:nilnil // driver.Valuer returns (nil, nil) to represent SQL NULL
		return nil, nil
	}
	return encodeGeometry(mp.MultiPolygon, mp.SRID)
}

// MarshalJSON 序列化为 GeoJSON MultiPolygon。
func (mp MultiPolygon) MarshalJSON() ([]byte, error) {
	return json.Marshal(geojson.NewGeometry(mp.MultiPolygon))
}

// UnmarshalJSON 宽容反序列化 GeoJSON。
func (mp *MultiPolygon) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == jsonNull {
		*mp = MultiPolygon{}
		return nil
	}

	// 字符串 (WKT)
	if strings.HasPrefix(trimmed, "\"") {
		var str string
		if err := json.Unmarshal(data, &str); err == nil {
			m, err := wkt.UnmarshalMultiPolygon(str)
			if err == nil {
				mp.MultiPolygon = m
				mp.SRID = DefaultSRID
				return nil
			}
		}
	}

	geom, err := geojson.UnmarshalGeometry(data)
	if err != nil {
		return fmt.Errorf("geo: failed to unmarshal MultiPolygon JSON: %w", err)
	}
	m, ok := geom.Geometry().(orb.MultiPolygon)
	if !ok {
		return fmt.Errorf("geo: geojson geometry is %T, expected MultiPolygon", geom.Geometry())
	}
	mp.MultiPolygon = m
	mp.SRID = DefaultSRID
	return nil
}

// Geometry 多态容器 Codec 实现

// Scan 实现 sql.Scanner。
func (g *Geometry) Scan(src any) error {
	geom, srid, err := decodeGeometry(src)
	if err != nil {
		return err
	}
	g.Geometry = geom
	g.SRID = srid
	return nil
}

// Value 实现 driver.Valuer。
func (g Geometry) Value() (driver.Value, error) {
	if g.Geometry == nil {
		//nolint:nilnil // driver.Valuer returns (nil, nil) to represent SQL NULL
		return nil, nil
	}
	return encodeGeometry(g.Geometry, g.SRID)
}

// MarshalJSON 序列化为标准 GeoJSON。
func (g Geometry) MarshalJSON() ([]byte, error) {
	if g.Geometry == nil {
		return []byte(jsonNull), nil
	}
	return json.Marshal(geojson.NewGeometry(g.Geometry))
}

// UnmarshalJSON 宽容反序列化 GeoJSON 或 WKT 字符串。
func (g *Geometry) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == jsonNull {
		g.Geometry = nil
		g.SRID = DefaultSRID
		return nil
	}

	// WKT 字符串
	if strings.HasPrefix(trimmed, "\"") {
		var str string
		if err := json.Unmarshal(data, &str); err == nil {
			geom, err := wkt.Unmarshal(str)
			if err == nil {
				g.Geometry = geom
				g.SRID = DefaultSRID
				return nil
			}
		}
	}

	geom, err := geojson.UnmarshalGeometry(data)
	if err != nil {
		return fmt.Errorf("geo: failed to unmarshal Geometry JSON: %w", err)
	}
	g.Geometry = geom.Geometry()
	g.SRID = DefaultSRID
	return nil
}
