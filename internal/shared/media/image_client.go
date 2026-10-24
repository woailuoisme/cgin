// Package media 提供媒体文件上传、验证、处理、命名策略与 Imgproxy 防篡改签名服务。
package media

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	stdImage "image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/samber/lo"
	"github.com/samber/oops"
	_ "golang.org/x/image/webp"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
)

// Format 指定输出图像格式。
type Format string

const (
	FormatWebP Format = "webp"
	FormatJPEG Format = "jpeg"
	FormatJPG  Format = "jpg"
	FormatPNG  Format = "png"
	FormatAVIF Format = "avif"
	FormatGIF  Format = "gif"
)

// NormalizeFormat 清洗并标准化格式字符串。
func NormalizeFormat(ext string) Format {
	clean := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
	switch clean {
	case "webp":
		return FormatWebP
	case "jpg", "jpeg":
		return FormatJPEG
	case "png":
		return FormatPNG
	case "avif":
		return FormatAVIF
	case "gif":
		return FormatGIF
	default:
		return FormatWebP
	}
}

// ResizingType 定义 imgproxy 缩放策略模式。
type ResizingType string

const (
	ResizeFit   ResizingType = "fit"
	ResizeFill  ResizingType = "fill"
	ResizeAuto  ResizingType = "auto"
	ResizeForce ResizingType = "force"
)

// GravityType 定义裁剪对齐重心方位。
type GravityType string

const (
	GravityCenter     GravityType = "ce"
	GravitySmart      GravityType = "sm"
	GravityFocusPoint GravityType = "fp:0:0"
	GravityNorth      GravityType = "no"
	GravitySouth      GravityType = "so"
	GravityEast       GravityType = "ea"
	GravityWest       GravityType = "we"
)

// ImageMeta 保存图像宽高、格式与文件大小等基础元数据。
type ImageMeta struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Format Format `json:"format"`
	Size   int64  `json:"size"`
}

// ProcessConfig 保存构建 imgproxy 处理选项所需的所有参数。
type ProcessConfig struct {
	ResizingType ResizingType
	Width        int
	Height       int
	Gravity      GravityType
	Enlarge      bool
	Format       Format
	Quality      int
	Dpr          float64
	Blur         float64
	Sharpen      float64
}

// ProcessOption 用于配置图像处理选项的函数式选项。
type ProcessOption func(*ProcessConfig)

// WithResize 设置目标宽度与高度。
func WithResize(width, height int) ProcessOption {
	return func(c *ProcessConfig) {
		c.Width = width
		c.Height = height
	}
}

// WithResizingType 设置缩放策略（fit, fill, auto, force）。
func WithResizingType(t ResizingType) ProcessOption {
	return func(c *ProcessConfig) {
		c.ResizingType = t
	}
}

// WithGravity 设置裁剪对齐方位（如智能居中、北、南等）。
func WithGravity(g GravityType) ProcessOption {
	return func(c *ProcessConfig) {
		c.Gravity = g
	}
}

// WithEnlarge 设置小图是否允许放大。
func WithEnlarge(enlarge bool) ProcessOption {
	return func(c *ProcessConfig) {
		c.Enlarge = enlarge
	}
}

// WithFormat 设置输出目标图像格式。
func WithFormat(f Format) ProcessOption {
	return func(c *ProcessConfig) {
		c.Format = NormalizeFormat(string(f))
	}
}

// WithQuality 设置图像压缩品质百分比 (1-100)。
func WithQuality(q int) ProcessOption {
	return func(c *ProcessConfig) {
		if q > 0 && q <= 100 {
			c.Quality = q
		}
	}
}

// WithDpr 设置设备像素比 (如 Retina 屏幕 2x, 3x)。
func WithDpr(dpr float64) ProcessOption {
	return func(c *ProcessConfig) {
		if dpr > 0 {
			c.Dpr = dpr
		}
	}
}

// WithBlur 设置高斯模糊半径。
func WithBlur(sigma float64) ProcessOption {
	return func(c *ProcessConfig) {
		if sigma > 0 {
			c.Blur = sigma
		}
	}
}

// WithSharpen 设置锐化程度。
func WithSharpen(sigma float64) ProcessOption {
	return func(c *ProcessConfig) {
		if sigma > 0 {
			c.Sharpen = sigma
		}
	}
}

// Client 定义现代化 imgproxy 客户端与图像元数据处理契约。
type Client interface {
	URL(sourceURI string, opts ...ProcessOption) (string, error)
	Build(sourceURI string) *Builder
	Thumbnail(sourceURI string, width, height int) (string, error)
	Avatar(sourceURI string, size int) (string, error)
	Inspect(input []byte) (*ImageMeta, error)
	ResolveSourceURI(path string) string
	CheckHealth(ctx context.Context) error
	HealthCheck() error
	Ping(ctx context.Context) error
	Driver() string
	Enabled() bool
	Close() error
	Shutdown() error
}

// client 结构体实现 Client 接口。
type client struct {
	baseURL    string
	key        []byte
	salt       []byte
	insecure   bool
	config     config.ImgproxyConfig
	storageCfg config.StorageConfig
	httpClient *resty.Client
}

// NewImageClient 创建并初始化基于 imgproxy 的图像服务客户端。
func NewImageClient(cfg config.ImgproxyConfig) Client {
	return NewWithStorage(cfg, config.StorageConfig{})
}

// NewWithStorage 创建带存储配置的 imgproxy 图像服务客户端，用于按存储配置推导资源 URI。
func NewWithStorage(imgCfg config.ImgproxyConfig, storageCfg config.StorageConfig) Client {
	mergedCfg := mergeImgproxyConfig(imgCfg)
	baseURL, keyHex, saltHex := mergedCfg.ResolveCredentials()
	keyBytes, _ := hex.DecodeString(keyHex)
	saltBytes, _ := hex.DecodeString(saltHex)

	if len(keyBytes) == 0 && len(keyHex) > 0 {
		keyBytes = []byte(keyHex)
	}
	if len(saltBytes) == 0 && len(saltHex) > 0 {
		saltBytes = []byte(saltHex)
	}

	trimmedURL := cmp.Or(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "http://localhost:8088")

	restyClient := resty.New().
		SetTimeout(2*time.Second).
		SetHeader("User-Agent", "bgin-imgproxy-client")

	return &client{
		baseURL:    trimmedURL,
		key:        keyBytes,
		salt:       saltBytes,
		insecure:   mergedCfg.Insecure || len(keyBytes) == 0 || len(saltBytes) == 0,
		config:     mergedCfg,
		storageCfg: storageCfg,
		httpClient: restyClient,
	}
}

// mergeImgproxyConfig 为未显式配置的 imgproxy 参数填充合理的默认值。
func mergeImgproxyConfig(cfg config.ImgproxyConfig) config.ImgproxyConfig {
	cfg.BaseURL = cmp.Or(cfg.BaseURL, "http://localhost:8088")
	cfg.DefaultFormat = cmp.Or(cfg.DefaultFormat, "webp")
	cfg.DefaultQuality = cmp.Or(max(cfg.DefaultQuality, 0), 85)
	return cfg
}

// URL 根据函数式选项构建已签名的 imgproxy 处理 URL。
func (s *client) URL(sourceURI string, opts ...ProcessOption) (string, error) {
	if strings.TrimSpace(sourceURI) == "" {
		return "", errs.BadRequest("source uri cannot be empty")
	}

	resolvedURI := s.ResolveSourceURI(sourceURI)
	cfg := &ProcessConfig{
		Format:  NormalizeFormat(s.config.DefaultFormat),
		Quality: s.config.DefaultQuality,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return s.generateURL(resolvedURI, cfg)
}

// Build 返回链式 URL 构建器实例。
func (s *client) Build(sourceURI string) *Builder {
	resolvedURI := s.ResolveSourceURI(sourceURI)
	return newBuilder(s, resolvedURI)
}

// Thumbnail 快捷构建等比自适应缩略图 URL。
func (s *client) Thumbnail(sourceURI string, width, height int) (string, error) {
	return s.URL(sourceURI,
		WithResizingType(ResizeFill),
		WithResize(width, height),
	)
}

// Avatar 快捷构建头像裁切（自动识别人脸重心）URL。
func (s *client) Avatar(sourceURI string, size int) (string, error) {
	return s.URL(sourceURI,
		WithResizingType(ResizeFill),
		WithResize(size, size),
		WithGravity(GravityFocusPoint),
	)
}

// Inspect 纯 Go 标准库轻量解析图像宽高与格式（零 CGO）。
func (s *client) Inspect(input []byte) (*ImageMeta, error) {
	if len(input) == 0 {
		return nil, errs.BadRequest("image input bytes cannot be empty")
	}

	cfg, formatStr, err := stdImage.DecodeConfig(bytes.NewReader(input))
	if err != nil {
		return nil, oops.Code(errs.CodeBadRequest).In("imgproxy.Inspect").Wrap(err)
	}

	return &ImageMeta{
		Width:  cfg.Width,
		Height: cfg.Height,
		Format: NormalizeFormat(formatStr),
		Size:   int64(len(input)),
	}, nil
}

// ResolveSourceURI 根据底层存储驱动自适应补全 URI（如 local:// 或 s3://）。
func (s *client) ResolveSourceURI(sourcePath string) string {
	cleanPath := strings.TrimSpace(sourcePath)
	if cleanPath == "" {
		return ""
	}

	if strings.Contains(cleanPath, "://") {
		return cleanPath
	}

	trimmed := strings.TrimPrefix(cleanPath, "/")
	driver := strings.ToLower(strings.TrimSpace(s.storageCfg.Driver))

	if driver == config.StorageDriverS3 {
		return lo.Ternary(s.storageCfg.S3.Bucket != "", fmt.Sprintf("s3://%s/%s", s.storageCfg.S3.Bucket, trimmed), "s3://"+trimmed)
	}

	return "local://" + trimmed
}

// CheckHealth 检查 imgproxy 服务的连通性与健康状态。
func (s *client) CheckHealth(ctx context.Context) error {
	if s.baseURL == "" {
		return errors.New("imgproxy base url is not configured")
	}

	healthURL := s.baseURL + "/health"
	resp, err := s.httpClient.R().SetContext(ctx).Get(healthURL)
	if err != nil {
		return fmt.Errorf("imgproxy unreachable: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("imgproxy /health returned HTTP %d", resp.StatusCode())
	}

	if !strings.Contains(resp.String(), "imgproxy is running") {
		return fmt.Errorf("imgproxy unexpected health response: %s", resp.String())
	}

	return nil
}

// HealthCheck 提供无 context 入参的健康检查适配入口。
func (s *client) HealthCheck() error {
	return s.CheckHealth(context.Background())
}

// Ping 执行 imgproxy 连通性探测（满足 ready.Pinger 契约）。
func (s *client) Ping(ctx context.Context) error {
	return s.CheckHealth(ctx)
}

// Driver 返回当前图像服务驱动名称。
func (s *client) Driver() string {
	return "imgproxy"
}

// Enabled 报告当前图像服务是否启用。
func (s *client) Enabled() bool {
	return s.config.IsEnabled()
}

// Close 优雅释放资源。
func (s *client) Close() error {
	return nil
}

// Shutdown 优雅停机实现。
func (s *client) Shutdown() error {
	return s.Close()
}

// generateURL 内部组装处理参数并生成最终签名或非签名 URL。
func (s *client) generateURL(sourceURI string, cfg *ProcessConfig) (string, error) {
	options := buildOptions(cfg)
	encodedSource := base64.RawURLEncoding.EncodeToString([]byte(sourceURI))

	ext := lo.Ternary(cfg.Format != "", "."+string(cfg.Format), "")

	options = append(options, encodedSource+ext)
	pathWithoutSignature := "/" + path.Join(options...)

	if s.insecure {
		return fmt.Sprintf("%s/insecure%s", s.baseURL, pathWithoutSignature), nil
	}

	mac := hmac.New(sha256.New, s.key)
	mac.Write(s.salt)
	mac.Write([]byte(pathWithoutSignature))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("%s/%s%s", s.baseURL, signature, pathWithoutSignature), nil
}

// buildOptions 将处理参数转化为 imgproxy 选项序列。
func buildOptions(cfg *ProcessConfig) []string {
	var opts []string

	if cfg.Width > 0 || cfg.Height > 0 || cfg.ResizingType != "" {
		rt := cmp.Or(cfg.ResizingType, ResizeFit)
		enlarge := lo.Ternary(cfg.Enlarge, 1, 0)
		opts = append(opts, fmt.Sprintf("rs:%s:%d:%d:%d", rt, cfg.Width, cfg.Height, enlarge))
	}

	if cfg.Gravity != "" {
		opts = append(opts, fmt.Sprintf("g:%s", cfg.Gravity))
	}

	if cfg.Quality > 0 && cfg.Quality <= 100 {
		opts = append(opts, fmt.Sprintf("q:%d", cfg.Quality))
	}

	if cfg.Dpr > 0 {
		opts = append(opts, "dpr:"+strconv.FormatFloat(cfg.Dpr, 'f', -1, 64))
	}

	if cfg.Blur > 0 {
		opts = append(opts, fmt.Sprintf("bl:%.2f", cfg.Blur))
	}

	if cfg.Sharpen > 0 {
		opts = append(opts, fmt.Sprintf("sh:%.2f", cfg.Sharpen))
	}

	return opts
}
