// Package geoip 提供基于 ip2region xdb 全内存缓冲的无锁并发 IP 归属地检索服务。
// 当未开启或离线库缺失时自动进入零分配降级模式（内部/保留 IP 正常识别，公网 IP 返回 Unknown）。
package geoip

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
	"github.com/samber/oops"

	"cgin/internal/config"
	"cgin/pkg/logger"
)

const (
	labelUnknown    = "Unknown"
	labelInternalIP = "Internal IP"
	labelIPv6       = "IPv6"
	rawFieldCount   = 5
)

var (
	locUnknown  = &Location{Country: labelUnknown, City: labelUnknown, Raw: "0|0|0|0|0"}
	locInternal = &Location{Country: labelInternalIP, City: labelInternalIP, Raw: "0|0|0|内网IP|内网IP"}
	locIPv6     = &Location{Country: labelIPv6, City: labelIPv6, Raw: "0|0|0|IPv6|IPv6"}
)

// Location 表示结构化的 IP 归属地信息。
type Location struct {
	Country  string `json:"country"`
	Region   string `json:"region,omitempty"`
	Province string `json:"province"`
	City     string `json:"city"`
	ISP      string `json:"isp"`
	Raw      string `json:"raw,omitempty"`
}

// Format 返回可读的归属地摘要（如 "中国·上海·上海市·电信"），自动剔除空段与相邻重复项。
func (l *Location) Format() string {
	if l == nil {
		return labelUnknown
	}
	switch l.Country {
	case labelInternalIP, labelUnknown, labelIPv6:
		return l.Country
	}

	var b strings.Builder
	var last string
	for _, p := range [4]string{l.Country, l.Province, l.City, l.ISP} {
		if p == "" || p == "0" || p == last {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("·")
		}
		b.WriteString(p)
		last = p
	}
	if b.Len() == 0 {
		return labelUnknown
	}
	return b.String()
}

// Service 定义 IP 地理归属地解析契约。
type Service interface {
	Lookup(ctx context.Context, ipStr string) (*Location, error)
	LookupAddr(ctx context.Context, addr netip.Addr) (*Location, error)
	Summary(ctx context.Context, ipStr string) string
	SummaryAddr(ctx context.Context, addr netip.Addr) string
	Reload() error
	Close()
}

type xdbEngine struct {
	pool sync.Pool
}

func loadXDBEngine(dbPath string) (*xdbEngine, error) {
	cBuff, err := xdb.LoadContentFromFile(dbPath)
	if err != nil {
		return nil, oops.In("geoip").With("db_path", dbPath).Wrapf(err, "failed to load xdb buffer")
	}
	probe, err := xdb.NewWithBuffer(xdb.IPv4, cBuff)
	if err != nil {
		return nil, oops.In("geoip").With("db_path", dbPath).Wrapf(err, "failed to verify xdb buffer")
	}
	probe.Close()

	eng := &xdbEngine{}
	eng.pool.New = func() any {
		s, err := xdb.NewWithBuffer(xdb.IPv4, cBuff)
		if err != nil {
			return nil
		}
		return s
	}
	return eng, nil
}

func (e *xdbEngine) searchIPv4(addr netip.Addr) (*Location, error) {
	v := e.pool.Get()
	if v == nil {
		return locUnknown, nil
	}
	searcher, ok := v.(*xdb.Searcher)
	if !ok || searcher == nil {
		return locUnknown, nil
	}
	defer e.pool.Put(searcher)

	ip4 := addr.As4()
	raw, err := searcher.Search(ip4[:])
	if err != nil {
		return nil, oops.In("geoip").With("ip", addr.String()).Wrapf(err, "xdb search failed")
	}
	return ParseRaw(raw), nil
}

type service struct {
	dbPath string
	log    *slog.Logger
	engine atomic.Pointer[xdbEngine]
}

// NewService 根据配置初始化 GeoIP 服务；当未启用或数据库文件缺失时自动降级为无锁空实现。
func NewService(cfg config.GeoIPConfig, log *slog.Logger) (Service, error) {
	log = cmp.Or(log, logger.Nop())
	s := &service{
		dbPath: strings.TrimSpace(cfg.DBPath),
		log:    log,
	}
	if !cfg.Enabled || s.dbPath == "" {
		log.Debug("geoip disabled, running in noop mode", slog.Bool("enabled", cfg.Enabled))
		return s, nil
	}

	if _, err := os.Stat(s.dbPath); err != nil {
		log.Warn("geoip xdb file not found, running in degraded noop mode",
			slog.Any("error", err),
			slog.String("db_path", s.dbPath),
		)
		return s, nil
	}

	eng, err := loadXDBEngine(s.dbPath)
	if err != nil {
		log.Warn("failed to load geoip xdb, running in degraded noop mode",
			slog.Any("error", err),
			slog.String("db_path", s.dbPath),
		)
		return s, nil
	}
	s.engine.Store(eng)
	log.Info("geoip xdb buffer loaded into memory", slog.String("db_path", s.dbPath))
	return s, nil
}

// NewNoop 创建零 I/O 的降级 GeoIP 服务实例。
func NewNoop() Service {
	return &service{log: logger.Nop()}
}

func (s *service) Lookup(ctx context.Context, ipStr string) (*Location, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ipStr))
	if err != nil {
		return nil, oops.In("geoip").With("ip", ipStr).Wrapf(err, "invalid ip address")
	}
	return s.LookupAddr(ctx, addr)
}

func (s *service) LookupAddr(ctx context.Context, addr netip.Addr) (*Location, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("geoip lookup canceled: %w", err)
	}
	addr = addr.Unmap()
	if !addr.IsValid() {
		return nil, oops.In("geoip").Errorf("invalid netip.Addr")
	}
	if isInternalAddr(addr) {
		return locInternal, nil
	}
	if addr.Is6() {
		return locIPv6, nil
	}
	eng := s.engine.Load()
	if eng == nil {
		return locUnknown, nil
	}
	return eng.searchIPv4(addr)
}

func (s *service) Summary(ctx context.Context, ipStr string) string {
	loc, err := s.Lookup(ctx, ipStr)
	if err != nil || loc == nil {
		return labelUnknown
	}
	return loc.Format()
}

func (s *service) SummaryAddr(ctx context.Context, addr netip.Addr) string {
	loc, err := s.LookupAddr(ctx, addr)
	if err != nil || loc == nil {
		return labelUnknown
	}
	return loc.Format()
}

// Reload 重新从磁盘加载 xdb 缓冲并原子替换当前内存引擎。
func (s *service) Reload() error {
	if s.dbPath == "" {
		return nil
	}
	eng, err := loadXDBEngine(s.dbPath)
	if err != nil {
		return err
	}
	s.engine.Store(eng)
	s.log.Info("geoip xdb buffer hot-reloaded", slog.String("db_path", s.dbPath))
	return nil
}

// Close 释放内存引擎引用。
func (s *service) Close() {
	s.engine.Store(nil)
}

// Shutdown 适配 DI 容器生命周期关闭钩子。
func (s *service) Shutdown() error {
	s.Close()
	return nil
}

// ParseRaw 解析 ip2region "国家|区域|省份|城市|ISP" 原始字符串。
func ParseRaw(raw string) *Location {
	var parts [rawFieldCount]string
	rem := raw
	for i := range rawFieldCount - 1 {
		idx := strings.IndexByte(rem, '|')
		if idx < 0 {
			parts[i] = cleanPart(rem)
			rem = ""
			break
		}
		parts[i] = cleanPart(rem[:idx])
		rem = rem[idx+1:]
	}
	parts[rawFieldCount-1] = cleanPart(rem)

	loc := &Location{
		Country:  parts[0],
		Region:   parts[1],
		Province: parts[2],
		City:     parts[3],
		ISP:      parts[4],
		Raw:      raw,
	}
	if loc.Country == "" && loc.Province == "" && loc.City == "" {
		loc.Country = labelUnknown
		loc.City = labelUnknown
	}
	return loc
}

func cleanPart(s string) string {
	s = strings.TrimSpace(s)
	if s == "0" {
		return ""
	}
	return s
}

func isInternalAddr(addr netip.Addr) bool {
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}
