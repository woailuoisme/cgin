// Package geoip 提供基于 ip2region 的离线 IP 归属地极速检索、无锁热重载与安全降级服务。
package geoip

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
	"github.com/rs/zerolog"

	"cgin/internal/config"
	"cgin/pkg/logger"
)

const (
	// internalIP 表示内网/局域网/环回地址的默认标识。
	internalIP = "Internal IP"
	// unknownLocation 表示无法识别或未加载数据库时的缺省归属地描述。
	unknownLocation = "Unknown"
	// ipv6Default 表示公网 IPv6 地址的缺省归属地描述。
	ipv6Default = "IPv6"
)

// Location 封装结构化的 IP 物理与网络归属地信息。
type Location struct {
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	Province string `json:"province,omitempty"`
	City     string `json:"city,omitempty"`
	ISP      string `json:"isp,omitempty"`
	Raw      string `json:"raw,omitempty"`
}

var (
	internalLocation = Location{
		Country: internalIP,
		City:    internalIP,
		Raw:     "0|0|0|" + internalIP + "|" + internalIP,
	}
	unknownLocationModel = Location{
		Country: unknownLocation,
		Raw:     "0|0|0|" + unknownLocation + "|" + unknownLocation,
	}
	ipv6Location = Location{
		Country: ipv6Default,
		City:    ipv6Default,
		Raw:     "0|0|0|" + ipv6Default + "|" + ipv6Default,
	}
)

func isInternalAddr(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast()
}

func parseIPString(ipStr string) (netip.Addr, error) {
	trimmed := strings.TrimSpace(ipStr)
	if trimmed == "" {
		return netip.Addr{}, errors.New("empty ip address")
	}

	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid ip address: %s", trimmed)
	}

	return addr, nil
}

// Format 输出地域描述文本（国家·省份·城市·运营商），空值与相邻重复项自动省略。
func (l Location) Format() string {
	parts := l.collectParts()
	if len(parts) == 0 {
		return unknownLocation
	}
	return strings.Join(parts, "·")
}

func (l Location) collectParts() []string {
	var parts []string
	for _, raw := range []string{l.Country, l.Province, l.City, l.ISP} {
		val := strings.TrimSpace(raw)
		if val == "" || val == "0" {
			continue
		}
		if len(parts) > 0 && parts[len(parts)-1] == val {
			continue
		}
		parts = append(parts, val)
	}
	return parts
}

// ParseRaw 将 ip2region 原始管道符输出格式（Country|Region|Province|City|ISP）反解为 Location。
func ParseRaw(raw string) Location {
	parts := strings.Split(raw, "|")
	loc := Location{Raw: raw}
	if len(parts) >= 5 {
		loc.Country = sanitizePart(parts[0])
		loc.Region = sanitizePart(parts[1])
		loc.Province = sanitizePart(parts[2])
		loc.City = sanitizePart(parts[3])
		loc.ISP = sanitizePart(parts[4])
	}
	return loc
}

func sanitizePart(s string) string {
	s = strings.TrimSpace(s)
	if s == "0" {
		return ""
	}
	return s
}

// Provider 定义 IP 物理地理归属地检索驱动规范，遵循依赖倒置与开闭原则。
type Provider interface {
	// SearchAddr 检索指定公网 netip.Addr 的归属地数据。
	SearchAddr(ctx context.Context, addr netip.Addr) (*Location, error)
	// Close 释放驱动底层资源。
	Close() error
}

// Service 定义 IP 归属地检索服务的标准契约，支持 Context 贯穿与零堆分配路径。
type Service interface {
	// Lookup 解析指定 IP 字符串并返回结构化归属地数据。
	Lookup(ctx context.Context, ip string) (*Location, error)
	// LookupAddr 解析指定 netip.Addr 并返回结构化归属地数据（零堆分配快速路径）。
	LookupAddr(ctx context.Context, addr netip.Addr) (*Location, error)
	// Summary 获取指定 IP 的人性化地域描述字符串。
	Summary(ctx context.Context, ip string) string
	// SummaryAddr 获取指定 netip.Addr 的人性化地域描述字符串。
	SummaryAddr(ctx context.Context, addr netip.Addr) string
	// Reload 重新加载 IP 归属地数据库（支持热重载）。
	Reload() error
	// Close 释放检索器占用的内存与文件句柄。
	Close()
}

// xdbEngine 封装单份只读 XDB 内存缓冲区与并发安全的检索器对象池。
type xdbEngine struct {
	dbPath  string
	version *xdb.Version
	cBuff   []byte
	pool    sync.Pool
	log     *zerolog.Logger
}

func newEngine(dbPath string, cBuff []byte, log *zerolog.Logger) (*xdbEngine, error) {
	log = cmp.Or(log, logger.Nop())

	header, err := xdb.LoadHeaderFromBuff(cBuff)
	if err != nil {
		return nil, fmt.Errorf("failed to load ip2region header: %w", err)
	}

	version, err := xdb.VersionFromHeader(header)
	if err != nil {
		return nil, fmt.Errorf("failed to get version from ip2region header: %w", err)
	}

	eng := &xdbEngine{
		dbPath:  dbPath,
		version: version,
		cBuff:   cBuff,
		log:     log,
	}

	eng.pool.New = func() any {
		searcher, err := xdb.NewWithBuffer(eng.version, eng.cBuff)
		if err != nil {
			eng.log.Error().Err(err).Msg("failed to create pooled ip2region searcher")
			return nil
		}
		return searcher
	}

	return eng, nil
}

func loadEngine(path string, log *zerolog.Logger) (*xdbEngine, error) {
	cBuff, err := xdb.LoadContentFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load geoip xdb content: %w", err)
	}
	return newEngine(path, cBuff, log)
}

func (e *xdbEngine) search(ipBytes []byte) (string, error) {
	obj := e.pool.Get()
	if obj == nil {
		return "", errors.New("searcher pool returned nil")
	}
	searcher, ok := obj.(*xdb.Searcher)
	if !ok {
		return "", errors.New("invalid searcher type in pool")
	}
	defer e.pool.Put(searcher)

	return searcher.Search(ipBytes)
}

// xdbProvider 实现基于 ip2region 的 Provider 驱动。
type xdbProvider struct {
	engine atomic.Pointer[xdbEngine]
}

var _ Provider = (*xdbProvider)(nil)

func newXDBProvider(eng *xdbEngine) *xdbProvider {
	p := &xdbProvider{}
	p.engine.Store(eng)
	return p
}

func (p *xdbProvider) SearchAddr(_ context.Context, addr netip.Addr) (*Location, error) {
	eng := p.engine.Load()
	if eng == nil {
		return &unknownLocationModel, nil
	}

	ip4 := addr.As4()
	raw, err := eng.search(ip4[:])
	if err != nil {
		return nil, fmt.Errorf("ip2region search failed: %w", err)
	}

	parsed := ParseRaw(raw)
	return &parsed, nil
}

func (p *xdbProvider) Close() error {
	p.engine.Store(nil)
	return nil
}

// noopProvider 是降级空驱动。
type noopProvider struct{}

var _ Provider = (*noopProvider)(nil)

func (n *noopProvider) SearchAddr(_ context.Context, _ netip.Addr) (*Location, error) {
	return &unknownLocationModel, nil
}

func (n *noopProvider) Close() error {
	return nil
}

type providerHolder struct {
	p Provider
}

// defaultService 统一封装 IP 地址分类、缓存及底层 Provider 调度的门面服务。
type defaultService struct {
	cfg      config.GeoIPConfig
	provider atomic.Pointer[providerHolder]
	log      *zerolog.Logger
}

var _ Service = (*defaultService)(nil)

func (s *defaultService) getLogger() *zerolog.Logger {
	return cmp.Or(s.log, logger.Nop())
}

// Lookup 解析指定 IP 字符串并返回结构化归属地数据。
func (s *defaultService) Lookup(ctx context.Context, ipStr string) (*Location, error) {
	addr, err := parseIPString(ipStr)
	if err != nil {
		return nil, err
	}
	return s.LookupAddr(ctx, addr)
}

// LookupAddr 解析指定 netip.Addr 并返回结构化归属地数据（零堆分配快速路径）。
func (s *defaultService) LookupAddr(ctx context.Context, addr netip.Addr) (*Location, error) {
	unmapped := addr.Unmap()
	if !unmapped.IsValid() {
		return nil, errors.New("invalid ip address")
	}
	if isInternalAddr(unmapped) {
		return &internalLocation, nil
	}
	if unmapped.Is6() {
		return &ipv6Location, nil
	}

	holder := s.provider.Load()
	if holder == nil || holder.p == nil {
		return &unknownLocationModel, nil
	}

	return holder.p.SearchAddr(ctx, unmapped)
}

// Summary 获取指定 IP 的人性化地域描述字符串。
func (s *defaultService) Summary(ctx context.Context, ipStr string) string {
	loc, err := s.Lookup(ctx, ipStr)
	if err != nil || loc == nil {
		return unknownLocation
	}
	return loc.Format()
}

// SummaryAddr 获取指定 netip.Addr 的人性化地域描述字符串。
func (s *defaultService) SummaryAddr(ctx context.Context, addr netip.Addr) string {
	loc, err := s.LookupAddr(ctx, addr)
	if err != nil || loc == nil {
		return unknownLocation
	}
	return loc.Format()
}

// Reload 无锁原子替换底层检索器（零停机热重载）。
func (s *defaultService) Reload() error {
	if s.cfg.DBPath == "" {
		return nil
	}
	if _, err := os.Stat(s.cfg.DBPath); err != nil {
		return fmt.Errorf("geoip database file stat failed: %w", err)
	}

	eng, err := loadEngine(s.cfg.DBPath, s.getLogger())
	if err != nil {
		return err
	}

	s.provider.Store(&providerHolder{p: newXDBProvider(eng)})
	return nil
}

// Close 释放检索服务底层引用。
func (s *defaultService) Close() {
	holder := s.provider.Swap(nil)
	if holder != nil && holder.p != nil {
		_ = holder.p.Close()
	}
}

// Shutdown 满足 do.ShutdownerWithError 优雅停机契约。
func (s *defaultService) Shutdown() error {
	s.Close()
	return nil
}

// NewNoop 创建安全的降级空实现。
func NewNoop() Service {
	return NewWithProvider(&noopProvider{})
}

// NewWithProvider 允许外部显式注入自定义 Provider 构建服务。
func NewWithProvider(p Provider) Service {
	svc := &defaultService{log: logger.Nop()}
	svc.provider.Store(&providerHolder{p: p})
	return svc
}

// NewService 根据配置构建 GeoIP 服务实例。若数据库文件缺失则自动启用安全降级。
func NewService(cfg config.GeoIPConfig, log *zerolog.Logger) (Service, error) {
	l := cmp.Or(log, logger.Nop())

	if !cfg.Enabled {
		return NewNoop(), nil
	}

	if _, err := os.Stat(cfg.DBPath); os.IsNotExist(err) {
		l.Warn().Str("path", cfg.DBPath).Msg("geoip database file does not exist, degrading to noop service")
		return NewNoop(), nil
	}

	eng, err := loadEngine(cfg.DBPath, l)
	if err != nil {
		l.Warn().
			Err(err).
			Str("path", cfg.DBPath).
			Msg("failed to load geoip xdb engine, degrading to noop service")
		return NewNoop(), nil
	}

	svc := &defaultService{cfg: cfg, log: l}
	svc.provider.Store(&providerHolder{p: newXDBProvider(eng)})
	return svc, nil
}
