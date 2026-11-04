package notification

import (
	"cmp"
	"context"
	"maps"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/samber/lo"
	"github.com/samber/oops"

	"cgin/pkg/errs"
)

const (
	// GotifyDefaultPriority Gotify 默认通知优先级。
	GotifyDefaultPriority = 5
	// GotifyPriorityMin 最低优先级（静默消息，无声音提示）。
	GotifyPriorityMin = 0
	// GotifyPriorityLow 低优先级。
	GotifyPriorityLow = 2
	// GotifyPriorityNormal 标准优先级。
	GotifyPriorityNormal = 5
	// GotifyPriorityHigh 高优先级（振动与通知栏醒目展示）。
	GotifyPriorityHigh = 8
	// GotifyPriorityUrgent 紧急最高优先级。
	GotifyPriorityUrgent = 10

	// GotifyExtraKeyClientDisplay Gotify 客户端展示格式扩展键。
	GotifyExtraKeyClientDisplay = "client::display"
	// GotifyExtraKeyClientNotification Gotify 客户端交互通知扩展键。
	GotifyExtraKeyClientNotification = "client::notification"
	// GotifyContentTypeMarkdown Markdown 内容类型。
	GotifyContentTypeMarkdown = "text/markdown"

	defaultGotifyTimeout = 10 * time.Second
)

// GotifyMessage 表示标准的 Gotify 推送通知载荷。
type GotifyMessage struct {
	Title     string
	Message   string
	Priority  int
	Extras    map[string]any
	Token     string
	ServerURL string
}

// NewGotifyMessage 创建初始化默认优先级的 Gotify 消息对象。
func NewGotifyMessage(msg ...string) *GotifyMessage {
	return &GotifyMessage{
		Message:  lo.FirstOr(msg, ""),
		Priority: GotifyDefaultPriority,
		Extras:   make(map[string]any),
	}
}

// WithTitle 设置消息标题。
func (m *GotifyMessage) WithTitle(title string) *GotifyMessage {
	m.Title = title
	return m
}

// WithMessage 设置消息正文文本。
func (m *GotifyMessage) WithMessage(msg string) *GotifyMessage {
	m.Message = msg
	return m
}

// WithPriority 设置消息优先级 (0-10)。
func (m *GotifyMessage) WithPriority(p int) *GotifyMessage {
	m.Priority = p
	return m
}

// UrgentPriority 设置紧急最高优先级 (10)。
func (m *GotifyMessage) UrgentPriority() *GotifyMessage {
	m.Priority = GotifyPriorityUrgent
	return m
}

// HighPriority 设置高优先级 (8)。
func (m *GotifyMessage) HighPriority() *GotifyMessage {
	m.Priority = GotifyPriorityHigh
	return m
}

// NormalPriority 设置普通优先级 (5)。
func (m *GotifyMessage) NormalPriority() *GotifyMessage {
	m.Priority = GotifyPriorityNormal
	return m
}

// LowPriority 设置低优先级 (2)。
func (m *GotifyMessage) LowPriority() *GotifyMessage {
	m.Priority = GotifyPriorityLow
	return m
}

// MinPriority 设置最低优先级 (0)。
func (m *GotifyMessage) MinPriority() *GotifyMessage {
	m.Priority = GotifyPriorityMin
	return m
}

// Markdown 开启 Markdown 渲染展示。
func (m *GotifyMessage) Markdown() *GotifyMessage {
	return m.WithMarkdown(true)
}

// WithMarkdown 控制是否开启 Markdown 渲染展示。
func (m *GotifyMessage) WithMarkdown(enable bool) *GotifyMessage {
	if m.Extras == nil {
		m.Extras = make(map[string]any)
	}
	if enable {
		m.Extras[GotifyExtraKeyClientDisplay] = map[string]any{
			"contentType": GotifyContentTypeMarkdown,
		}
	} else {
		delete(m.Extras, GotifyExtraKeyClientDisplay)
	}
	return m
}

// WithClickURL 设置点击通知时的外部跳转链接。
func (m *GotifyMessage) WithClickURL(clickURL string) *GotifyMessage {
	if m.Extras == nil {
		m.Extras = make(map[string]any)
	}
	if clickURL != "" {
		m.Extras[GotifyExtraKeyClientNotification] = map[string]any{
			"click": map[string]any{
				"url": clickURL,
			},
		}
	} else {
		delete(m.Extras, GotifyExtraKeyClientNotification)
	}
	return m
}

// WithExtra 设置自定义扩展属性键值。
func (m *GotifyMessage) WithExtra(key string, val any) *GotifyMessage {
	if m.Extras == nil {
		m.Extras = make(map[string]any)
	}
	m.Extras[key] = val
	return m
}

// WithExtras 批量合并扩展属性。
func (m *GotifyMessage) WithExtras(extras map[string]any) *GotifyMessage {
	if m.Extras == nil {
		m.Extras = make(map[string]any)
	}
	maps.Copy(m.Extras, extras)
	return m
}

// WithToken 显式指定本次推送的应用 AppToken。
func (m *GotifyMessage) WithToken(token string) *GotifyMessage {
	m.Token = token
	return m
}

// WithServerURL 显式指定本次推送的目标服务器地址。
func (m *GotifyMessage) WithServerURL(serverURL string) *GotifyMessage {
	m.ServerURL = serverURL
	return m
}

// GotifyNotification 声明支持 Gotify 格式化的领域通知接口契约。
type GotifyNotification interface {
	ToGotify(notifiable any) any
}

// GotifyDriver 定义底层 Gotify 推送发送驱动接口。
type GotifyDriver interface {
	Name() string
	Send(ctx context.Context, msg *GotifyMessage) error
}

// GotifyChannel Gotify 推送通知渠道实现。
type GotifyChannel struct {
	mu            sync.RWMutex
	drivers       map[string]GotifyDriver
	defaultDriver string
	serverURL     string
	appToken      string
	priority      int
}

var (
	_ Channel   = (*GotifyChannel)(nil)
	_ Formatter = (*GotifyChannel)(nil)
)

// NewGotifyChannel 创建 Gotify 推送通知渠道实例。
func NewGotifyChannel(defaultDriver string) *GotifyChannel {
	return &GotifyChannel{
		drivers:       make(map[string]GotifyDriver),
		defaultDriver: defaultDriver,
		priority:      GotifyDefaultPriority,
	}
}

// WithDefaults 配置渠道默认服务地址、应用 Token 及优先级。
func (c *GotifyChannel) WithDefaults(serverURL, appToken string, priority ...int) *GotifyChannel {
	c.serverURL = serverURL
	c.appToken = appToken
	if len(priority) > 0 && priority[0] > 0 {
		c.priority = priority[0]
	}
	return c
}

// Name 返回渠道标识名。
func (c *GotifyChannel) Name() string {
	return "gotify"
}

// RegisterDriver 注册 Gotify 底层传输驱动。
func (c *GotifyChannel) RegisterDriver(name string, driver GotifyDriver) *GotifyChannel {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drivers[name] = driver
	if c.defaultDriver == "" {
		c.defaultDriver = name
	}
	return c
}

// Driver 获取指定驱动实例，若无传参则返回当前默认驱动。
func (c *GotifyChannel) Driver(name ...string) GotifyDriver {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.drivers[cmp.Or(lo.FirstOr(name, ""), c.defaultDriver)]
}

// Format 将领域通知对象转换为 Gotify 载荷 *GotifyMessage。
func (c *GotifyChannel) Format(n Notification, notifiable any) (any, error) {
	if gotifyNotif, ok := n.(GotifyNotification); ok {
		return toGotifyMessage(gotifyNotif.ToGotify(notifiable))
	}

	if cf, ok := n.(ChannelFormatter); ok {
		if raw := cf.Format(c.Name(), notifiable); raw != nil {
			return toGotifyMessage(raw)
		}
	}

	return nil, oops.Code(errs.CodeNotificationUnsupportedGotify).
		With("notification", n).
		Errorf("notification does not implement GotifyNotification or ChannelFormatter")
}

func toGotifyMessage(raw any) (*GotifyMessage, error) {
	switch v := raw.(type) {
	case *GotifyMessage:
		return v, nil
	case GotifyMessage:
		return &v, nil
	case string:
		return NewGotifyMessage(v), nil
	default:
		return nil, oops.Code(errs.CodeInvalidGotifyMessage).Errorf("unsupported gotify message payload type: %T", raw)
	}
}

func parseGotifyRouteTarget(routeVal string) (serverURL, token string) {
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
func (c *GotifyChannel) Send(ctx context.Context, notifiable any, message any) error {
	msg, ok := message.(*GotifyMessage)
	if !ok {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: expected *GotifyMessage, got %T", message)
	}

	var routeServerURL, routeToken string
	if route := resolveGotifyRouteTarget(notifiable); route != "" {
		routeServerURL, routeToken = parseGotifyRouteTarget(route)
	}

	msg.ServerURL = cmp.Or(msg.ServerURL, routeServerURL, c.serverURL)
	msg.Token = cmp.Or(msg.Token, routeToken, c.appToken)

	if msg.Priority == 0 && c.priority > 0 {
		msg.Priority = c.priority
	}

	driver := c.Driver()
	if driver == nil {
		return oops.Code(errs.CodeGotifyDriverNotFound).Errorf("gotify: default driver %s is not registered", c.defaultDriver)
	}

	return driver.Send(ctx, msg)
}

func resolveGotifyRouteTarget(notifiable any) string {
	if notifiable == nil {
		return ""
	}
	target := ToNotifiable(notifiable)
	if target == nil {
		return ""
	}
	return target.RouteNotificationFor("gotify")
}

// SendSimple 简易快速推送一条 Gotify 消息。
func (c *GotifyChannel) SendSimple(ctx context.Context, title, content string, priority ...int) error {
	p := cmp.Or(lo.FirstOr(priority, 0), c.priority)
	msg := NewGotifyMessage(content).
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

// GotifyHTTPConfig Gotify HTTP 驱动配置。
type GotifyHTTPConfig struct {
	ServerURL string
	AppToken  string
	Priority  int
	Timeout   time.Duration
}

// GotifyHTTPDriver Gotify 官方 REST API 驱动。
type GotifyHTTPDriver struct {
	cfg    GotifyHTTPConfig
	client *resty.Client
}

var _ GotifyDriver = (*GotifyHTTPDriver)(nil)

// NewGotifyHTTPDriver 创建 Gotify HTTP 驱动实例。
func NewGotifyHTTPDriver(cfg GotifyHTTPConfig) (*GotifyHTTPDriver, error) {
	timeout := cmp.Or(cfg.Timeout, defaultGotifyTimeout)
	client := resty.New().SetTimeout(timeout)

	return &GotifyHTTPDriver{
		cfg:    cfg,
		client: client,
	}, nil
}

// Name 返回驱动标识名。
func (d *GotifyHTTPDriver) Name() string {
	return "http"
}

type gotifyMessagePayload struct {
	Title    string         `json:"title,omitempty"`
	Message  string         `json:"message"`
	Priority int            `json:"priority"`
	Extras   map[string]any `json:"extras,omitempty"`
}

type gotifyErrorResponse struct {
	Error            string `json:"error"`
	ErrorCode        int    `json:"errorCode"`
	ErrorDescription string `json:"errorDescription"`
}

// Send 执行向 Gotify 发送推送通知。
func (d *GotifyHTTPDriver) Send(ctx context.Context, msg *GotifyMessage) error {
	if msg == nil {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: message cannot be nil")
	}

	serverURL := cmp.Or(msg.ServerURL, d.cfg.ServerURL)
	if serverURL == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: server_url is required")
	}

	token := cmp.Or(msg.Token, d.cfg.AppToken)
	if token == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: app_token is required")
	}

	if msg.Message == "" {
		return oops.Code(errs.CodeBadRequest).Errorf("gotify: message text is required")
	}

	priority := msg.Priority
	if priority == 0 && d.cfg.Priority > 0 {
		priority = d.cfg.Priority
	}

	reqBody := gotifyMessagePayload{
		Title:    msg.Title,
		Message:  msg.Message,
		Priority: priority,
		Extras:   msg.Extras,
	}

	endpoint := strings.TrimRight(serverURL, "/") + "/message"

	var errResp gotifyErrorResponse
	resp, err := d.client.R().
		SetContext(ctx).
		SetHeader("X-Gotify-Key", token).
		SetHeader("Content-Type", "application/json").
		SetBody(reqBody).
		SetError(&errResp).
		Post(endpoint)
	if err != nil {
		return oops.Code(errs.CodeInternal).In("GotifyHTTPDriver.Send").Wrap(err)
	}

	if resp.IsError() {
		errMsg := cmp.Or(errResp.ErrorDescription, errResp.Error, resp.Status())
		return oops.Code(errs.CodeGotifyAPIError).
			With("status", resp.StatusCode()).
			With("server_url", serverURL).
			Errorf("gotify notification delivery failed: %s (status: %d)", errMsg, resp.StatusCode())
	}

	return nil
}
