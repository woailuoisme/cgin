package handler

import (
	"cmp"
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"cgin/internal/dto"
	"cgin/internal/service"
	"cgin/pkg/logger"
	"cgin/pkg/ready"
	"cgin/pkg/response"
)

// probeTimeout 是就绪探针调度的总超时时限。
const probeTimeout = 2 * time.Second

// SystemHandler 是探针端点的 HTTP 适配层。
type SystemHandler struct {
	svc    *service.SystemService
	logger *zerolog.Logger
}

// NewSystemHandler 构造系统探针 SystemHandler，日志器未注入时静默兜底。
func NewSystemHandler(svc *service.SystemService, log *zerolog.Logger) *SystemHandler {
	return &SystemHandler{svc: svc, logger: cmp.Or(log, logger.Nop())}
}

// Index 处理 GET / 根路径请求。
func (h *SystemHandler) Index(c *gin.Context) {
	h.Liveness(c)
}

// Liveness 处理 GET /livez 存活探针请求。
func (h *SystemHandler) Liveness(c *gin.Context) {
	response.OK(c, dto.HealthResource{Status: dto.StatusOK})
}

// Readiness 处理 GET /readyz 就绪探针请求：并发探测各依赖，关键项失败返回 503，非关键项失败返回 200 degraded。
func (h *SystemHandler) Readiness(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), probeTimeout)
	defer cancel()

	result, errs := h.svc.Check(ctx)
	for name, err := range errs {
		h.logger.Warn().Err(err).Str("component", name).Msg("readiness probe failed")
	}

	if result.Status == ready.StatusNotReady {
		c.JSON(result.StatusCode(), result)
		return
	}
	response.OK(c, result)
}
