package handler

import (
	"cmp"
	"net/http"

	"github.com/gin-gonic/gin"

	"cgin/internal/service"
	"cgin/pkg/authentication"
	"cgin/pkg/errs"
	platformRealtime "cgin/pkg/realtime"
	"cgin/pkg/response"
)

// RealtimeHandler 是实时通信端点的 HTTP 适配层。
type RealtimeHandler struct {
	svc *service.RealtimeService
}

// NewRealtimeHandler 构造 RealtimeHandler。
func NewRealtimeHandler(svc *service.RealtimeService) *RealtimeHandler {
	return &RealtimeHandler{
		svc: svc,
	}
}

// GetToken 处理 GET /v1/realtime/token。
//
// @Summary      获取实时通信连接凭据
// @Description  为当前已认证用户签发直连 Centrifugo (WSS/SSE) 所需的 JWT 令牌，有效期由 CENTRIFUGO_TOKEN_TTL 决定（默认 2 小时）。
// @Tags         实时通信
// @Produce      json
// @Security     BearerAuth
// @Success      200 {object} dto.TokenResource "实时通信连接凭据"
// @x-order      1
// @Router       /realtime/token [get]
func (h *RealtimeHandler) GetToken(c *gin.Context) {
	identity, err := authentication.RequireIdentity(c.Request.Context())
	if err != nil {
		_ = c.Error(err)
		return
	}

	res, err := h.svc.IssueToken(c.Request.Context(), identity.UserID, identity.Role)
	if err != nil {
		_ = c.Error(err)
		return
	}
	response.OK(c, res)
}

// ProxySubscribe 处理 POST /v1/realtime/proxy/subscribe。
//
// @Summary      Centrifugo 订阅鉴权代理端点
// @Description  接收 Centrifugo 反向代理发起的频道订阅鉴权请求，校验 HMAC-SHA256 签名并调度频道权限策略。
// @Tags         实时通信
// @Accept       json
// @Produce      json
// @Param        payload body dto.SubscribeBody true "Centrifugo 订阅代理请求"
// @Success      200 {object} dto.SubscribeResource "Centrifugo 订阅代理响应"
// @x-order      2
// @Router       /realtime/proxy/subscribe [post]
func (h *RealtimeHandler) ProxySubscribe(c *gin.Context) {
	body, err := c.GetRawData()
	if err != nil {
		// 读取请求体失败属本地错误，与 Centrifugo Proxy 协议无关，统一走应用错误模型渲染。
		_ = c.Error(errs.BadRequest(""))
		return
	}

	// 签名头优先读取 X-Centrifugo-Signature 或 X-Signature
	sig := cmp.Or(
		c.GetHeader(platformRealtime.CentrifugoSignatureHeader),
		c.GetHeader(platformRealtime.CentrifugoSignatureLegacyHeader),
	)

	resp, err := h.svc.AuthorizeSubscribe(c.Request.Context(), body, sig)
	if err != nil {
		// 本端点的错误体遵循 Centrifugo Proxy 协议（error.code 为状态码），
		// 因此这里不用 errs.Render 的应用统一错误模型，只用它定状态码与文案。
		status, mapped := errs.Map(err)
		c.JSON(status, gin.H{"error": gin.H{"code": status, "message": mapped.Message}})
		return
	}

	c.JSON(http.StatusOK, resp)
}
