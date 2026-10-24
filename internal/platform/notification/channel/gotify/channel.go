// Package gotify 提供 Gotify 实时推送通知渠道与驱动实现。
package gotify

import (
	"cmp"
	"context"
	"net/url"
	"strings"
	"sync"

	"github.com/samber/lo"
	"github.com/samber/oops"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/notification/contract"
)

// Channel Gotify 推送通知渠道实现。
type Channel struct {
	mu            sync.RWMutex
	drivers       map[string]Driver
	defaultDriver string
	serverURL     string
	appToken      string
	priority      int
}

var _ contract.Channel = (*Channel)(nil)

// NewChannel 创建 Gotify 推送通知渠道实例。
func NewChannel(defaultDriver string) *Channel {
	return &Channel{
		drivers:       make(map[string]Driver),
		defaultDriver: defaultDriver,
		priority:      DefaultPriority,
	}
}

// WithDefaults 配置渠道默认服务地址、应用 Token 及优先级。
func (c *Channel) WithDefaults(serverURL, appToken string, priority ...int) *Channel {
	c.serverURL = serverURL
	c.appToken = appToken
	if len(priority) > 0 && priority[0] > 0 {
		c.priority = priority[0]
	}
	return c
}

// Name 返回渠道标识名。
func (c *Channel) Name() string {
	return "gotify"
}

// RegisterDriver 注册 Gotify 底层传输驱动。
func (c *Channel) RegisterDriver(name string, driver Driver) *Channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drivers[name] = driver
	if c.defaultDriver == "" {
		c.defaultDriver = name
	}
	return c
}

// Driver 获取指定驱动实例，若无传参则返回当前默认驱动。
func (c *Channel) Driver(name ...string) Driver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.drivers[cmp.Or(lo.FirstOr(name, ""), c.defaultDriver)]
}

// Format 将领域通知对象转换为 Gotify 载荷 *Message。
func (c *Channel) Format(n contract.Notification, notifiable any) (any, error) {
	if gotifyNotif, ok := n.(Notification); ok {
		raw := gotifyNotif.ToGotify(notifiable)
		return toGotifyMessage(raw)
	}

	if cf, ok := n.(contract.ChannelFormatter); ok {
		raw := cf.Format(c.Name(), notifiable)
		if raw != nil {
			return toGotifyMessage(raw)
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedGotify).
		With("notification", n).
		Errorf("notification does not implement gotify.Notification or ChannelFormatter")
}

func toGotifyMessage(raw any) (*Message, error) {
	switch v := raw.(type) {
	case *Message:
		return v, nil
	case Message:
		return &v, nil
	case string:
		return NewMessage(v), nil
	default:
		return nil, oops.Code(errs.CodeInvalidGotifyMessage).Errorf("unsupported gotify message payload type: %T", raw)
	}
}

// parseRouteTarget 解析接收者路由返回的标识，支持完整 Webhook URL 或纯 Token。
func parseRouteTarget(routeVal string) (serverURL, token string) {
	if !strings.HasPrefix(routeVal, "http://") && !strings.HasPrefix(routeVal, "https://") {
		return "", routeVal
	}

	parsed, err := url.Parse(routeVal)
	if err != nil {
		return "", routeVal
	}

	queryToken := parsed.Query().Get("token")
	if queryToken == "" {
		return "", routeVal
	}

	parsed.RawQuery = ""
	return strings.TrimSuffix(parsed.String(), "/message"), queryToken
}

// Send 执行 Gotify 渠道的消息分发逻辑。
func (c *Channel) Send(ctx context.Context, notifiable any, message any) error {
	msg, ok := message.(*Message)
	if !ok {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: expected *gotify.Message, got %T", message)
	}

	// 1. 提取接收者路由寻址目标
	var routeServerURL, routeToken string
	if route := resolveRouteTarget(notifiable); route != "" {
		routeServerURL, routeToken = parseRouteTarget(route)
	}

	// 2. 服务地址与 Token 寻址：Message 显式覆盖优先 -> 接收者实体路由 -> 渠道配置兜底
	msg.ServerURL = cmp.Or(msg.ServerURL, routeServerURL, c.serverURL)
	msg.Token = cmp.Or(msg.Token, routeToken, c.appToken)

	// 3. 优先级回退：Message 显式配置优先 -> 渠道配置兜底
	if msg.Priority == 0 && c.priority > 0 {
		msg.Priority = c.priority
	}

	driver := c.Driver()
	if driver == nil {
		return oops.Code(errs.CodeGotifyDriverNotFound).Errorf("gotify: default driver %s is not registered", c.defaultDriver)
	}

	return driver.Send(ctx, msg)
}

// resolveRouteTarget 解析通知对象针对 gotify 渠道的专属路由标识。
func resolveRouteTarget(notifiable any) string {
	if notifiable == nil {
		return ""
	}
	target := contract.ToNotifiable(notifiable)
	if target == nil {
		return ""
	}
	return target.RouteNotificationFor("gotify")
}

// SendSimple 简易快速推送一条 Gotify 消息。
func (c *Channel) SendSimple(ctx context.Context, title, content string, priority ...int) error {
	p := cmp.Or(lo.FirstOr(priority, 0), c.priority)
	msg := NewMessage(content).
		WithTitle(title).
		WithPriority(p)

	driver := c.Driver()
	if driver == nil {
		return oops.Code(errs.CodeGotifyDriverNotFound).Errorf("gotify: default driver %s is not registered", c.defaultDriver)
	}

	if msg.ServerURL == "" {
		msg.ServerURL = c.serverURL
	}
	if msg.Token == "" {
		msg.Token = c.appToken
	}

	return driver.Send(ctx, msg)
}
