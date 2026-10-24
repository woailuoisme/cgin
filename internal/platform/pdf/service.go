package pdf

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/johnfercher/maroto/v2"
	marotoconfig "github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/consts/orientation"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/core/entity"
	"github.com/johnfercher/maroto/v2/pkg/props"
	"github.com/samber/lo"
	"github.com/samber/oops"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/storage"
)

// Service 统一管理 PDF 文档的排版、渲染与多通道交付。
//
// 交付只依赖标准库的 http.ResponseWriter：任何 Web 框架（Gin/chi/net-http）
// 都可以直接把自己的响应写入器传进来，平台层不感知具体框架。
// 不在此定义消费方接口：调用方按需在自己一侧声明最小接口即可。
type Service struct {
	cfg        config.PDFConfig
	storage    storage.Disk
	log        *zap.Logger
	fonts      []entity.CustomFont
	fontFamily string
}

// NewService 实例化 PDF 服务。
func NewService(cfg config.PDFConfig, disk storage.Disk, log *zap.Logger) *Service {
	fonts, resolvedFamily := ResolveFontConfig(cfg.FontPath, cfg.FontFamily, log)

	return &Service{
		cfg:        cfg,
		storage:    disk,
		log:        log,
		fonts:      fonts,
		fontFamily: resolvedFamily,
	}
}

// RegisterPDF 注册并初始化 PDF 服务实例。
func RegisterPDF(cfg *config.Config, disk storage.Disk, log *zap.Logger) (*Service, error) {
	pdfCfg := lo.Ternary(cfg != nil, lo.FromPtr(cfg).PDF, config.PDFConfig{
		FontFamily: "SourceHanSans",
		PageSize:   "A4",
		Margin:     DefaultMargin,
	})

	return NewService(pdfCfg, disk, log), nil
}

// Render 将 Document 编排并编译生成原始 PDF 二进制字节切片。
func (s *Service) Render(ctx context.Context, doc Document) ([]byte, error) {
	if doc == nil {
		return nil, oops.Errorf("pdf: document cannot be nil")
	}

	m, b, err := s.createMaroto(ctx, doc)
	if err != nil {
		return nil, oops.Wrap(err)
	}

	if err := doc.Build(ctx, b); err != nil {
		return nil, oops.Wrap(err)
	}

	pdfDoc, err := m.Generate()
	if err != nil {
		return nil, oops.Wrap(err)
	}

	return pdfDoc.GetBytes(), nil
}

// RenderToWriter 将渲染完成的 PDF 二进制内容写入目标 io.Writer。
func (s *Service) RenderToWriter(ctx context.Context, doc Document, w io.Writer) error {
	data, err := s.Render(ctx, doc)
	if err != nil {
		return oops.Wrap(err)
	}
	if _, err := w.Write(data); err != nil {
		return oops.Wrap(err)
	}
	return nil
}

// StreamToHTTP 将生成的 PDF 以流式方式直接输出给标准 HTTP 响应写入器。
func (s *Service) StreamToHTTP(ctx context.Context, w http.ResponseWriter, filename string, doc Document, mode DeliveryMode) error {
	data, err := s.Render(ctx, doc)
	if err != nil {
		return oops.Wrap(err)
	}

	filename = cmp.Or(filename, "document.pdf")
	disposition := cmp.Or(string(mode), string(DeliveryInline))

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))

	// ASCII 兜底文件名必须清洗：内嵌引号会提前终止 filename 参数，
	// 反斜杠与控制字符同样会破坏头部结构（CRLF 已由 net/http 写头时中和）。
	asciiFilename := sanitizeHeaderFilename(filename)
	encodedFilename := url.PathEscape(filename)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, disposition, asciiFilename, encodedFilename))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		return oops.Wrap(err)
	}
	return nil
}

// sanitizeHeaderFilename 清洗用于 Content-Disposition 的兜底文件名。
//
// 引号、反斜杠、路径分隔符与控制字符会破坏头部参数结构；非 ASCII 字符按 RFC 6266
// 也不适合出现在 filename 参数里（真正的文件名由 filename*= 以 UTF-8 承载），一并剔除。
// 清洗后为空时回落到默认名。
func sanitizeHeaderFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '/' {
			return -1
		}
		return r
	}, name)

	return cmp.Or(strings.TrimSpace(cleaned), "document.pdf")
}

// SaveToStorage 将生成的 PDF 文档持久化转存至已装配的 storage.Disk 对象存储。
func (s *Service) SaveToStorage(ctx context.Context, storagePath string, doc Document) (string, error) {
	if s.storage == nil {
		return "", oops.Errorf("pdf: storage disk not configured")
	}

	data, err := s.Render(ctx, doc)
	if err != nil {
		return "", oops.Wrap(err)
	}

	if err := s.storage.Put(ctx, storagePath, data); err != nil {
		return "", oops.Wrap(err)
	}

	return storagePath, nil
}

func (s *Service) createMaroto(ctx context.Context, doc Document) (core.Maroto, *Builder, error) {
	cfgBuilder := marotoconfig.NewBuilder()
	s.applyFonts(cfgBuilder)
	applyLayout(cfgBuilder, doc)
	applyMetadata(cfgBuilder, doc)
	s.applyPageNumber(cfgBuilder, doc)

	marotoCfg := cfgBuilder.Build()
	if err := s.applyHeaderFooter(ctx, marotoCfg, doc); err != nil {
		return nil, nil, err
	}

	m := maroto.New(marotoCfg)
	builder := NewBuilder(m, s.fontFamily)
	return m, builder, nil
}

func (s *Service) applyFonts(cfgBuilder marotoconfig.Builder) {
	if len(s.fonts) > 0 {
		cfgBuilder.WithCustomFonts(s.fonts)
	}
	cfgBuilder.WithDefaultFont(&props.Font{
		Family: s.fontFamily,
		Size:   FontSizeBody,
		Style:  fontstyle.Normal,
		Color:  &ColorTextPrimary,
	})
}

func applyLayout(cfgBuilder marotoconfig.Builder, doc Document) {
	layout, ok := doc.(WithLayout)
	if !ok {
		cfgBuilder.WithPageSize(DefaultPageSize)
		cfgBuilder.WithOrientation(orientation.Vertical)
		cfgBuilder.WithTopMargin(DefaultMargin)
		cfgBuilder.WithRightMargin(DefaultMargin)
		cfgBuilder.WithBottomMargin(DefaultMargin)
		cfgBuilder.WithLeftMargin(DefaultMargin)
		return
	}

	pageSize := cmp.Or(layout.PageSize(), DefaultPageSize)
	pageOrientation := cmp.Or(layout.Orientation(), orientation.Vertical)

	t, r, b, l := layout.Margins()
	cfgBuilder.WithPageSize(pageSize)
	cfgBuilder.WithOrientation(pageOrientation)
	cfgBuilder.WithTopMargin(resolveMargin(t))
	cfgBuilder.WithRightMargin(resolveMargin(r))
	cfgBuilder.WithBottomMargin(resolveMargin(b))
	cfgBuilder.WithLeftMargin(resolveMargin(l))
}

func resolveMargin(m float64) float64 {
	return lo.Ternary(m > 0, m, DefaultMargin)
}

func applyMetadata(cfgBuilder marotoconfig.Builder, doc Document) {
	cfgBuilder.WithCreationDate(time.Now())

	meta, ok := doc.(WithMetadata)
	if !ok {
		return
	}

	if t := meta.Title(); t != "" {
		cfgBuilder.WithTitle(t, true)
	}
	if a := meta.Author(); a != "" {
		cfgBuilder.WithAuthor(a, true)
	}
	if sub := meta.Subject(); sub != "" {
		cfgBuilder.WithSubject(sub, true)
	}
	if kw := meta.Keywords(); kw != "" {
		cfgBuilder.WithKeywords(kw, true)
	}
}

func (s *Service) applyPageNumber(cfgBuilder marotoconfig.Builder, doc Document) {
	useDefaultFooter := true
	if footerDoc, ok := doc.(WithDefaultPageFooter); ok {
		useDefaultFooter = footerDoc.UseDefaultFooter()
	}
	if _, customFooter := doc.(WithPageFooter); customFooter {
		useDefaultFooter = false
	}

	if useDefaultFooter {
		cfgBuilder.WithPageNumber(props.PageNumber{
			Pattern: "{current} / {total}",
			Place:   props.RightBottom,
			Family:  s.fontFamily,
			Style:   fontstyle.Normal,
			Size:    FontSizeSmall,
			Color:   &ColorTextMuted,
		})
	}
}

func (s *Service) applyHeaderFooter(ctx context.Context, marotoCfg *entity.Config, doc Document) error {
	if headerDoc, ok := doc.(WithPageHeader); ok {
		headerMaroto := maroto.New(marotoCfg)
		headerBuilder := NewBuilder(headerMaroto, s.fontFamily)
		if err := headerDoc.Header(ctx, headerBuilder); err != nil {
			return oops.Wrap(err)
		}
	}

	if footerDoc, ok := doc.(WithPageFooter); ok {
		footerMaroto := maroto.New(marotoCfg)
		footerBuilder := NewBuilder(footerMaroto, s.fontFamily)
		if err := footerDoc.Footer(ctx, footerBuilder); err != nil {
			return oops.Wrap(err)
		}
	}
	return nil
}
