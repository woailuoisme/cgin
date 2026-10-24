package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
	"github.com/spf13/cobra"

	"bgin/internal/console/core"
	"bgin/internal/platform/resilience"
)

// 预设官方源与国内高可用加速镜像源列表。
var defaultGeoIPDownloadURLs = []string{
	"https://fastly.jsdelivr.net/gh/lionsoul2014/ip2region@master/data/ip2region_v4.xdb",
	"https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v4.xdb",
	"https://ghproxy.net/https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v4.xdb",
}

// 缺省存储路径。
const defaultGeoIPDBPath = "storage/app/geoip/ip2region_v4.xdb"

// GeoIPUpdateOptions 封装 geoip:update 命令的参数与依赖容器 (SRP & DIP)。
type GeoIPUpdateOptions struct {
	Path       string
	URL        string
	Force      bool
	HTTPClient *http.Client
	Factory    Factory
}

// NewGeoIPUpdateOptions 构造 GeoIPUpdateOptions 实例。
func NewGeoIPUpdateOptions(f Factory) *GeoIPUpdateOptions {
	return &GeoIPUpdateOptions{
		Factory: f,
	}
}

// Complete 从配置与默认值解析并补全路径。
func (o *GeoIPUpdateOptions) Complete(_ *cobra.Command, _ []string) error {
	if o.Path != "" {
		return nil
	}
	if cfg, err := o.Factory.Config(); err == nil && cfg != nil && cfg.GeoIP.DBPath != "" {
		o.Path = cfg.GeoIP.DBPath
		return nil
	}
	o.Path = defaultGeoIPDBPath
	return nil
}

// Validate 校验更新参数合法性。
func (o *GeoIPUpdateOptions) Validate() error {
	if strings.TrimSpace(o.Path) == "" {
		return &ValidationError{Field: "path", Message: "database path cannot be empty"}
	}
	return nil
}

// Run 调度下载最新 ip2region_v4.xdb 数据库文件并原子覆盖。
func (o *GeoIPUpdateOptions) Run(ctx context.Context) error {
	streams := o.Factory.IOStreams()

	// 检查目标文件是否已存在
	if _, err := os.Stat(o.Path); err == nil && !o.Force {
		confirmed, err := o.Factory.Prompter().Confirm(
			fmt.Sprintf("GeoIP database already exists at [%s]. Overwrite it?", o.Path),
			"A new version will replace the current ip2region_v4.xdb file.",
			"Yes, update",
		)
		if err != nil {
			return err
		}
		if !confirmed {
			core.WarnTo(streams.Out, "GeoIP database update cancelled.")
			return core.ErrCanceled
		}
	}

	urls := defaultGeoIPDownloadURLs
	if strings.TrimSpace(o.URL) != "" {
		urls = []string{strings.TrimSpace(o.URL)}
	}

	targetDir := filepath.Dir(o.Path)
	if err := os.MkdirAll(targetDir, 0o755); err != nil { //nolint:gosec // 需保证目录对应用进程与运维可读写
		return fmt.Errorf("failed to create directory [%s]: %w", targetDir, err)
	}

	tmpFile := filepath.Join(targetDir, fmt.Sprintf(".%s.tmp.%d", filepath.Base(o.Path), time.Now().UnixNano()))
	defer func() {
		_ = os.Remove(tmpFile)
	}()

	client := o.HTTPClient
	if client == nil {
		client = resilience.NewResilientHTTPClient(resilience.ResilientTransportConfig{Timeout: 90 * time.Second})
	}

	var downloadErr error
	for i, u := range urls {
		core.TitleTo(streams.Out, fmt.Sprintf("Downloading GeoIP database [Source %d/%d: %s]...", i+1, len(urls), u))

		downloadErr = o.downloadFile(ctx, client, u, tmpFile)
		if downloadErr == nil {
			break
		}
		core.WarnTo(streams.Out, "Download failed from [%s]: %v. Trying next mirror...", u, downloadErr)
	}

	if downloadErr != nil {
		return fmt.Errorf("failed to download GeoIP database from all available sources: %w", downloadErr)
	}

	// 验证下载文件的有效性 (校验文件大小及 XDB Header)
	core.InfoTo(streams.Out, "Verifying database integrity...")
	if err := verifyXDBFile(tmpFile); err != nil {
		return fmt.Errorf("downloaded database file verification failed: %w", err)
	}

	// 原子替换文件
	if err := os.Rename(tmpFile, o.Path); err != nil {
		return fmt.Errorf("failed to replace database file at [%s]: %w", o.Path, err)
	}

	core.SuccessTo(streams.Out, "GeoIP database updated successfully at [%s].", o.Path)
	return nil
}

func (o *GeoIPUpdateOptions) downloadFile(ctx context.Context, client *http.Client, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "bgin-cli/geoip-updater")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP server returned non-200 status: %s", resp.Status)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	streams := o.Factory.IOStreams()
	total := resp.ContentLength
	if total <= 0 {
		// ip2region_v4.xdb 官方当前大小基准约 11MB；若服务端未返回 Content-Length（如分块传输），以此基准驱动百分比与进度条
		total = 11 * 1024 * 1024
	}
	bar := core.NewByteProgressBarTo(streams.Out, streams.CanPrompt(), total, "Downloading")
	defer bar.Finish()

	buf := make([]byte, 32*1024)
	var totalRead int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				return wErr
			}
			totalRead += int64(n)
			bar.Set(totalRead)
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			return rErr
		}
	}

	return nil
}

// verifyXDBFile 校验下载的 xdb 文件大小与文件头有效性。
func verifyXDBFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() < 512*1024 { // 有效的 ip2region xdb 至少大于 512KB
		return fmt.Errorf("file size too small (%d bytes), likely an incomplete or corrupted download", fi.Size())
	}

	headerBuff := make([]byte, xdb.HeaderInfoLength)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if _, err := io.ReadFull(f, headerBuff); err != nil {
		return fmt.Errorf("failed to read xdb header: %w", err)
	}

	header, err := xdb.LoadHeaderFromBuff(headerBuff)
	if err != nil {
		return fmt.Errorf("invalid xdb header data: %w", err)
	}

	if _, err := xdb.VersionFromHeader(header); err != nil {
		return fmt.Errorf("invalid xdb version metadata: %w", err)
	}

	return nil
}

// newGeoIPUpdateCmd 构造从官方源或镜像源下载最新 ip2region 数据库的命令。
func newGeoIPUpdateCmd(f Factory) *cobra.Command {
	opts := NewGeoIPUpdateOptions(f)

	cmd := &cobra.Command{
		Use:   "geoip:update",
		Short: "Download or update the offline ip2region IP database (ip2region_v4.xdb)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Complete(cmd, args); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return opts.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&opts.Path, "path", "", "Destination file path (default from GEOIP_DB_PATH or storage/app/geoip/ip2region_v4.xdb)")
	cmd.Flags().StringVar(&opts.URL, "url", "", "Custom remote URL to download ip2region_v4.xdb from")
	cmd.Flags().BoolVarP(&opts.Force, core.FlagForce, "f", false, "Overwrite existing database without confirmation")

	return cmd
}
