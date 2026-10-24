// Package media 提供媒体文件上传、验证、处理、命名策略与 Imgproxy 防篡改签名服务。
package media

// Builder 提供流畅的链式 imgproxy 处理选项与签名生成能力。
type Builder struct {
	client    *client
	sourceURI string
	cfg       ProcessConfig
}

// newBuilder 构建链式构建器。
func newBuilder(c *client, sourceURI string) *Builder {
	return &Builder{
		client:    c,
		sourceURI: sourceURI,
		cfg: ProcessConfig{
			Format:  NormalizeFormat(c.config.DefaultFormat),
			Quality: c.config.DefaultQuality,
		},
	}
}

// Resize 设置目标缩放尺寸。
func (b *Builder) Resize(width, height int) *Builder {
	b.cfg.Width = width
	b.cfg.Height = height
	return b
}

// ResizingType 设置缩放类型（fit, fill, auto, force）。
func (b *Builder) ResizingType(t ResizingType) *Builder {
	b.cfg.ResizingType = t
	return b
}

// Fit 快捷设置保持比例适应尺寸模式。
func (b *Builder) Fit(width, height int) *Builder {
	b.cfg.ResizingType = ResizeFit
	b.cfg.Width = width
	b.cfg.Height = height
	return b
}

// Fill 快捷设置填满目标尺寸模式。
func (b *Builder) Fill(width, height int) *Builder {
	b.cfg.ResizingType = ResizeFill
	b.cfg.Width = width
	b.cfg.Height = height
	return b
}

// Crop 快捷设置指定裁剪方位填满模式。
func (b *Builder) Crop(width, height int, gravity GravityType) *Builder {
	b.cfg.ResizingType = ResizeFill
	b.cfg.Width = width
	b.cfg.Height = height
	b.cfg.Gravity = gravity
	return b
}

// Gravity 设置重心方位。
func (b *Builder) Gravity(g GravityType) *Builder {
	b.cfg.Gravity = g
	return b
}

// Enlarge 设置是否允许放大较小原图。
func (b *Builder) Enlarge(enlarge bool) *Builder {
	b.cfg.Enlarge = enlarge
	return b
}

// Format 设置目标输出格式。
func (b *Builder) Format(f Format) *Builder {
	b.cfg.Format = NormalizeFormat(string(f))
	return b
}

// Quality 设置压缩质量。
func (b *Builder) Quality(q int) *Builder {
	if q > 0 && q <= 100 {
		b.cfg.Quality = q
	}
	return b
}

// Dpr 设置目标像素比。
func (b *Builder) Dpr(dpr float64) *Builder {
	if dpr > 0 {
		b.cfg.Dpr = dpr
	}
	return b
}

// Blur 设置高斯模糊。
func (b *Builder) Blur(sigma float64) *Builder {
	if sigma > 0 {
		b.cfg.Blur = sigma
	}
	return b
}

// Sharpen 设置图像锐化。
func (b *Builder) Sharpen(sigma float64) *Builder {
	if sigma > 0 {
		b.cfg.Sharpen = sigma
	}
	return b
}

// Sign 生成最终签名的 imgproxy URL。
func (b *Builder) Sign() (string, error) {
	return b.client.generateURL(b.sourceURI, &b.cfg)
}

// String 实现 fmt.Stringer 接口，便于在模板或日志中直接输出签名 URL。
func (b *Builder) String() string {
	urlStr, _ := b.Sign()
	return urlStr
}
