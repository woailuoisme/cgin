package wechat

import (
	"context"
	"time"
)

// FakeClient 实现了 MiniProgramClient 接口，用于本地脱机开发与自动化测试。
type FakeClient struct {
	appID string

	// 自定义钩子，为 nil 时走默认 mock 逻辑
	Code2SessionFunc   func(ctx context.Context, code string) (*Session, error)
	GetPhoneNumberFunc func(ctx context.Context, code string) (*PhoneInfo, error)
	GetUnlimitedFunc   func(ctx context.Context, params UnlimitedCodeParams) ([]byte, error)
}

var (
	_ MiniProgramClient = (*FakeClient)(nil)
	_ AuthService       = (*fakeAuthService)(nil)
	_ PhoneService      = (*fakePhoneService)(nil)
	_ WxaCodeService    = (*fakeWxaCodeService)(nil)
)

// NewFakeClient 构造一个具有可预测输出的 FakeClient 实例。
func NewFakeClient(appID string) *FakeClient {
	if appID == "" {
		appID = "wx_fake_default_appid"
	}
	return &FakeClient{appID: appID}
}

// AppID 返回 FakeClient 的 AppID。
func (f *FakeClient) AppID() string {
	return f.appID
}

// Auth 返回认证子服务。
func (f *FakeClient) Auth() AuthService {
	return &fakeAuthService{fake: f}
}

// Phone 返回手机号验证子服务。
func (f *FakeClient) Phone() PhoneService {
	return &fakePhoneService{fake: f}
}

// WxaCode 返回小程序码子服务。
func (f *FakeClient) WxaCode() WxaCodeService {
	return &fakeWxaCodeService{fake: f}
}

type fakeAuthService struct {
	fake *FakeClient
}

func (s *fakeAuthService) Code2Session(ctx context.Context, code string) (*Session, error) {
	if s.fake.Code2SessionFunc != nil {
		return s.fake.Code2SessionFunc(ctx, code)
	}

	return &Session{
		OpenID:     "mock_openid_" + code,
		UnionID:    "mock_unionid_" + code,
		SessionKey: "mock_session_key_1234567890",
	}, nil
}

type fakePhoneService struct {
	fake *FakeClient
}

func (s *fakePhoneService) GetPhoneNumber(ctx context.Context, code string) (*PhoneInfo, error) {
	if s.fake.GetPhoneNumberFunc != nil {
		return s.fake.GetPhoneNumberFunc(ctx, code)
	}

	return &PhoneInfo{
		PhoneNumber:     "+8613800000000",
		PurePhoneNumber: "13800000000",
		CountryCode:     "86",
		Watermark: Watermark{
			AppID:     s.fake.appID,
			Timestamp: time.Now().Unix(),
		},
	}, nil
}

type fakeWxaCodeService struct {
	fake *FakeClient
}

func (s *fakeWxaCodeService) GetUnlimited(ctx context.Context, params UnlimitedCodeParams) ([]byte, error) {
	if s.fake.GetUnlimitedFunc != nil {
		return s.fake.GetUnlimitedFunc(ctx, params)
	}

	// 返回合法的最小 JPEG 字节流
	return []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01, 0x01, 0x01, 0x00, 0x60, 0x00, 0x60, 0x00, 0x00, 0xFF, 0xD9}, nil
}

// NewFakeManager 构造注入 FakeClient 的多应用管理器。
func NewFakeManager(defaultAppID string) *Manager {
	mgr := &Manager{
		defaultApp: DefaultAppKey,
		clients:    make(map[string]MiniProgramClient),
		byAppID:    make(map[string]MiniProgramClient),
	}

	fakeClient := NewFakeClient(defaultAppID)
	mgr.Register(DefaultAppKey, fakeClient)

	return mgr
}
