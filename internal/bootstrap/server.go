package bootstrap

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-contrib/graceful"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"

	"cgin/pkg/logger"
)

// Serve 基于 gin-contrib/graceful 启动 HTTP 服务与后台路由器，阻塞直到平滑停机。
//
// 全部长生命周期 goroutine 由同一个 errgroup 托管：任一环节异常即取消其余环节，
// Wait 返回后才释放数据库与缓存连接，停机顺序确定。
func (a *Application) Serve(ctx context.Context) error {
	srvCfg := a.cfg.App.Server
	shutdownTimeout := cmp.Or(srvCfg.ShutdownTimeout, 3*time.Second)

	server := &http.Server{
		Addr:              ":" + strconv.Itoa(a.cfg.App.Port),
		Handler:           a.engine,
		ReadHeaderTimeout: srvCfg.ReadHeaderTimeout,
		ReadTimeout:       srvCfg.ReadTimeout,
		WriteTimeout:      srvCfg.WriteTimeout,
		IdleTimeout:       srvCfg.IdleTimeout,
		MaxHeaderBytes:    srvCfg.MaxHeaderBytes,
	}

	g, err := graceful.New(
		a.engine,
		graceful.WithServer(server),
		graceful.WithShutdownTimeout(shutdownTimeout),
		graceful.WithBeforeShutdown(func(_ context.Context) error {
			a.logger.Info("shutdown signal received, initiating graceful shutdown")
			return nil
		}),
	)
	if err != nil {
		return fmt.Errorf("bootstrap graceful init failed: %w", err)
	}
	defer g.Close()

	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		a.logStartup(server)
		if err := g.RunWithContext(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("http server exited unexpectedly: %w", err)
		}
		return nil
	})

	// 事件路由器与 HTTP 同生命周期，随上下文一起退出。
	if a.eventbus != nil {
		group.Go(func() error {
			if err := a.eventbus.Run(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("event bus router stopped: %w", err)
			}
			return nil
		})
	}

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// logStartup 输出启动横幅，便于从日志定位监听地址。
func (a *Application) logStartup(server *http.Server) {
	base := serverURL(a.cfg.App.Port)
	log := a.logger
	if log == nil {
		log = logger.Default()
	}

	attrs := []slog.Attr{
		slog.String("addr", server.Addr),
		slog.String("url", base),
		slog.String("env", a.cfg.App.Env),
		slog.String("gin", gin.Mode()),
		slog.String("storage", a.cfg.Storage.Driver),
	}
	if a.cfg.App.EnableDocs {
		attrs = append(attrs, slog.String("docs", base+docsPagePath))
	}
	log.LogAttrs(context.Background(), slog.LevelInfo, "HTTP server started and listening", attrs...)
}

// serverURL 构造本地服务访问 URL。
func serverURL(port int) string {
	return "http://localhost:" + strconv.Itoa(port)
}
