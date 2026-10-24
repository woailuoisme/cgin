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
)

// serverURL 构造本地服务访问 URL。
func serverURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}

// Serve 阻塞式启动 HTTP 服务，收到 ctx 取消信号后执行优雅停机。
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

	errCh := make(chan error, 1)
	go func() {
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

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	if a.eventbus != nil {
		go func() {
			if err := a.eventbus.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Warn("event bus router stopped with error", zap.Error(err))
			}
		}()
	}

	select {
	case err := <-errCh:
		return fmt.Errorf("HTTP server exited unexpectedly: %w", err)
	case <-ctx.Done():
		a.logger.Info("shutdown signal received, initiating graceful shutdown")
	}

	shutdownTimeout := cmp.Or(srvCfg.ShutdownTimeout, 3*time.Second)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	return nil
}
