package service

import (
	"cmp"
	"context"
	"encoding/json"
	"time"

	"cgin/internal/config"
	"cgin/internal/database/sqlc"
	"cgin/internal/dto"
	appuser "cgin/internal/user"
	"cgin/pkg/errs"
	platformRealtime "cgin/pkg/realtime"
)

// userRoleReader 声明签发连接凭据所需的用户角色读取能力。
type userRoleReader interface {
	Get(ctx context.Context, id string) (*sqlc.User, error)
}

// RealtimeDeps 汇总实时通信服务的装配依赖。
type RealtimeDeps struct {
	// Gateway 实时通信网关（向 Centrifugo 下发指令）。
	Gateway platformRealtime.Gateway
	// Cfg 全局配置，用于判定 realtime 是否启用与读取令牌有效期。
	Cfg *config.Config
	// Secret Centrifugo 反向代理请求的 HMAC 签名密钥。
	Secret string
	// Users 注入后签发的连接凭据以库中最新角色为准；未注入时沿用调用方传入的角色。
	Users userRoleReader
}

// RealtimeService 承载实时通信业务规则与鉴权编排。
type RealtimeService struct {
	gateway platformRealtime.Gateway
	cfg     *config.Config
	// secret 是 Centrifugo 反向代理请求的 HMAC 签名密钥。
	secret string
	// users 用于签发前核对账号当前角色；未注入时退回调用方传入的角色。
	users userRoleReader
}

// NewRealtimeService 创建实时通信服务。
func NewRealtimeService(deps RealtimeDeps) *RealtimeService {
	return &RealtimeService{
		gateway: deps.Gateway,
		cfg:     deps.Cfg,
		secret:  deps.Secret,
		users:   deps.Users,
	}
}

func (s *RealtimeService) isEnabled() bool {
	return s != nil && s.gateway != nil && s.cfg != nil && s.cfg.Realtime.Enabled
}

// IssueToken 为指定用户签发直连 Centrifugo (WSS/SSE) 所需的 JWT 凭据。
func (s *RealtimeService) IssueToken(ctx context.Context, userID, role string) (*dto.TokenResource, error) {
	if !s.isEnabled() {
		return nil, errs.BadRequest("Realtime service is disabled")
	}

	if s.users != nil {
		entity, err := s.users.Get(ctx, userID)
		if err != nil {
			return nil, err
		}
		if err := appuser.GuardState(entity); err != nil {
			return nil, err
		}
		role = string(entity.Role)
	}

	// 有效期来自配置；cmp.Or 让显式零值回退到 2 小时（envDefault 已覆盖常规情况）。
	exp := time.Now().UTC().Add(cmp.Or(s.cfg.Realtime.TokenTTL, 2*time.Hour))
	tok, err := s.gateway.GenerateConnectionToken(userID, exp, map[string]any{
		"role": role,
	}, nil)
	if err != nil {
		return nil, errs.Internal(err)
	}

	return &dto.TokenResource{
		Token:     tok,
		ExpiresAt: exp,
		WSURL:     s.cfg.Realtime.URL,
	}, nil
}

// AuthorizeSubscribe 校验 Centrifugo 反向代理请求签名并裁定频道订阅权限。
func (s *RealtimeService) AuthorizeSubscribe(
	ctx context.Context,
	body []byte,
	signature string,
) (*platformRealtime.SubscribeResponse, error) {
	if !platformRealtime.VerifySignature(s.secret, body, signature) {
		return nil, errs.Forbidden("Invalid Centrifugo signature")
	}

	var req platformRealtime.SubscribeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errs.BadRequest("Invalid JSON payload")
	}

	if !s.isEnabled() {
		return proxyError(platformRealtime.ProxyErrorCodeInternalError, "Realtime service is disabled"), nil
	}

	user := platformRealtime.User{ID: req.User}
	if s.users != nil && req.User != "" {
		entity, err := s.users.Get(ctx, req.User)
		if err != nil || appuser.GuardState(entity) != nil {
			return proxyError(platformRealtime.ProxyErrorCodePermissionDenied, "Permission denied"), nil //nolint:nilerr // 协议要求以 200 + error 字段表达拒绝
		}
		user.Role = string(entity.Role)
	}
	if err := s.gateway.Registry().Authorize(ctx, user, req.Channel); err != nil {
		// 订阅被拒按 Centrifugo 协议是 200 响应内的 error 字段，Go 层不构成错误
		return proxyError(platformRealtime.ProxyErrorCodePermissionDenied, "Permission denied"), nil //nolint:nilerr // 协议要求以 200 + error 字段表达拒绝
	}

	return &platformRealtime.SubscribeResponse{
		Result: &platformRealtime.SubscribeResult{},
	}, nil
}

func proxyError(code uint32, message string) *platformRealtime.SubscribeResponse {
	return &platformRealtime.SubscribeResponse{
		Error: &platformRealtime.ProxyError{
			Code:    code,
			Message: message,
		},
	}
}
