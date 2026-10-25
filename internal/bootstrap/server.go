package bootstrap

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// serverURL 构造本地服务访问 URL。
func serverURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}

// Serve 阻塞式启动 HTTP 服务与后台路由器，收到 ctx 取消信号后执行有序停机。
//
// 所有长生命周期 goroutine 都由同一个 errgroup 托管：任一环节异常会取消其余环节，
// Wait 返回之后才轮到调用方释放数据库与缓存，停机顺序因此是可证明的。
func (a *Application) Serve(ctx context.Context) error {
	srvCfg := a.cfg.App.Server
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", a.cfg.App.Port),
		Handler:           a.engine,
		ReadHeaderTimeout: srvCfg.ReadHeaderTimeout,
		ReadTimeout:       srvCfg.ReadTimeout,
		WriteTimeout:      srvCfg.WriteTimeout,
		IdleTimeout:       srvCfg.IdleTimeout,
		MaxHeaderBytes:    srvCfg.MaxHeaderBytes,
	}

	group, groupCtx := errgroup.WithContext(ctx)

	// 1. HTTP 监听。
	group.Go(func() error {
		a.logStartup(server)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server exited unexpectedly: %w", err)
		}
		return nil
	})

	// 2. 停机协调：上下文取消（或任一环节报错）时触发优雅关闭。
	group.Go(func() error {
		<-groupCtx.Done()

		a.logger.Info("shutdown signal received, initiating graceful shutdown")
		shutdownTimeout := cmp.Or(srvCfg.ShutdownTimeout, 3*time.Second)
		// 停机本身不能被已取消的父上下文带停。
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown failed: %w", err)
		}
		return nil
	})

	// 3. 领域事件路由器：生命周期与 HTTP 对齐，随上下文一起退出。
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

// logStartup 输出一次启动横幅，便于本地与容器日志定位监听地址。
func (a *Application) logStartup(server *http.Server) {
	base := serverURL(a.cfg.App.Port)
	fields := []zap.Field{
		zap.String("addr", server.Addr),
		zap.String("url", base),
		zap.String("env", a.cfg.App.Env),
		zap.String("gin", gin.Mode()),
		zap.String("storage", a.cfg.Storage.Driver),
	}
	if a.cfg.App.EnableDocs {
		fields = append(fields, zap.String("docs", base+docsPagePath))
	}
	a.logger.Info("HTTP server started and listening", fields...)
}
