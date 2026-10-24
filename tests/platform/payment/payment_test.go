package payment_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/money"
	"bgin/internal/platform/payment"
	"bgin/internal/platform/payment/enums"
)

func TestPaymentManagerDisabledWhenEmpty(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Env: "development"},
		Payment: config.PaymentConfig{
			FakeEnabled: false,
		},
	}

	manager, err := payment.NewManager(cfg, zap.NewNop())
	require.NoError(t, err)
	assert.False(t, manager.Enabled())
	require.NoError(t, manager.HealthCheck())
}

func TestPaymentManagerFakeLifecycle(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Env: "development"},
		Payment: config.PaymentConfig{
			FakeEnabled: true,
			CertsDir:    "./storage/app/certs",
		},
	}

	manager, err := payment.NewManager(cfg, zap.NewNop())
	require.NoError(t, err)
	assert.True(t, manager.Enabled())
	require.NoError(t, manager.HealthCheck())

	ctx := t.Context()

	// 1. 验证微信与支付宝均注册了 Fake 渠道应用
	assert.Contains(t, manager.Applications(enums.PaymentChannelWechat), payment.FakeApplicationKey)
	assert.Contains(t, manager.Applications(enums.PaymentChannelAlipay), payment.FakeApplicationKey)

	// 2. 发起微信扫码支付 (Native)
	createReq := payment.CreateRequest{
		Scene:       enums.PaymentSceneWechatNative,
		OutTradeNo:  "ORDER_20260927_001",
		Amount:      8800, // 88.00 元
		Subject:     "测试订单",
		Description: "订单明细",
		ClientIP:    "127.0.0.1",
		ExpireAt:    time.Now().Add(30 * time.Minute),
	}
	inst, err := manager.Create(ctx, createReq)
	require.NoError(t, err)
	require.NotNil(t, inst)
	assert.Equal(t, enums.PaymentSceneWechatNative, inst.Scene)
	assert.Equal(t, enums.PaymentInstructionQRCode, inst.Action)
	assert.NotEmpty(t, inst.Value)

	// 3. 发起支付宝手机网站支付 (Wap)
	alipayCreateReq := payment.CreateRequest{
		Scene:      enums.PaymentSceneAlipayWap,
		OutTradeNo: "ORDER_20260927_002",
		Amount:     12800,
		Subject:    "支付宝充值",
	}
	alipayInst, err := manager.Create(ctx, alipayCreateReq)
	require.NoError(t, err)
	require.NotNil(t, alipayInst)
	assert.Equal(t, enums.PaymentSceneAlipayWap, alipayInst.Scene)
	assert.Equal(t, enums.PaymentInstructionQRCode, alipayInst.Action)
	assert.NotEmpty(t, alipayInst.Value)

	// 4. 查询订单状态：Fake 默认未注入时返回 Pending
	queryRes, err := manager.Query(ctx, enums.PaymentChannelWechat, payment.QueryRequest{
		OutTradeNo: "ORDER_20260927_001",
	})
	require.NoError(t, err)
	require.NotNil(t, queryRes)
	assert.Equal(t, "ORDER_20260927_001", queryRes.OutTradeNo)
	assert.Equal(t, enums.PaymentStatusPending, queryRes.Status)

	// 获取 Fake Provider 并注入成功状态
	provider, err := manager.ForScene(enums.PaymentChannelWechat, enums.PaymentSceneWechatNative)
	require.NoError(t, err)
	fakeProvider, ok := provider.(*payment.FakeProvider)
	require.True(t, ok)
	fakeProvider.SetPaymentResult("ORDER_20260927_001", &payment.PaymentResult{
		OutTradeNo:     "ORDER_20260927_001",
		ChannelTradeNo: "WX_TRADE_123456",
		Status:         enums.PaymentStatusSucceeded,
	})

	queryRes, err = manager.Query(ctx, enums.PaymentChannelWechat, payment.QueryRequest{
		OutTradeNo: "ORDER_20260927_001",
	})
	require.NoError(t, err)
	assert.Equal(t, enums.PaymentStatusSucceeded, queryRes.Status)
	assert.Equal(t, "WX_TRADE_123456", queryRes.ChannelTradeNo)

	// 5. 关闭未支付订单
	err = manager.Close(ctx, enums.PaymentChannelWechat, payment.CloseRequest{
		OutTradeNo: "ORDER_20260927_001",
	})
	require.NoError(t, err)

	// 6. 发起退款：未注入时 Fake 默认返回 Pending
	refundRes, err := manager.Refund(ctx, enums.PaymentChannelWechat, payment.RefundRequest{
		OutTradeNo:   "ORDER_20260927_001",
		OutRefundNo:  "REFUND_20260927_001",
		TotalAmount:  8800,
		RefundAmount: 8800,
		Reason:       "用户主动取消",
	})
	require.NoError(t, err)
	require.NotNil(t, refundRes)
	assert.Equal(t, "REFUND_20260927_001", refundRes.OutRefundNo)
	assert.Equal(t, enums.PaymentRefundStatusPending, refundRes.Status)

	// 注入退款成功状态
	fakeProvider.MarkRefundSucceeded("REFUND_20260927_001", 8800)

	// 7. 查询退款
	queryRefundRes, err := manager.QueryRefund(ctx, enums.PaymentChannelWechat, payment.QueryRefundRequest{
		OutTradeNo:  "ORDER_20260927_001",
		OutRefundNo: "REFUND_20260927_001",
	})
	require.NoError(t, err)
	require.NotNil(t, queryRefundRes)
	assert.Equal(t, "REFUND_20260927_001", queryRefundRes.OutRefundNo)
	assert.Equal(t, enums.PaymentRefundStatusSucceeded, queryRefundRes.Status)

	// 8. 解析通知：Fake 必须预先入队通知
	fakeProvider.QueueNotification(&payment.Notification{
		Kind: enums.PaymentNotificationKindPayment,
		Payment: &payment.PaymentResult{
			OutTradeNo: "ORDER_20260927_001",
			Status:     enums.PaymentStatusSucceeded,
		},
	})

	httpReq := httptest.NewRequest(
		http.MethodPost,
		"/v1/payments/notify/wechat",
		bytes.NewBufferString(`{"event_id":"NOTIFY_123","event_type":"TRANSACTION.SUCCESS","out_trade_no":"ORDER_20260927_001","amount":8800}`),
	)
	httpReq.Header.Set("Content-Type", "application/json")
	notifyRes, err := manager.ParseNotification(ctx, enums.PaymentChannelWechat, payment.FakeApplicationKey, httpReq)
	require.NoError(t, err)
	require.NotNil(t, notifyRes)
	assert.Equal(t, enums.PaymentNotificationKindPayment, notifyRes.Kind)
	assert.Equal(t, "ORDER_20260927_001", notifyRes.Payment.OutTradeNo)
}

func TestAmountConversion(t *testing.T) {
	// 元到分与格式化
	assert.Equal(t, "1.00", payment.FormatYuanAmount(100))
	assert.Equal(t, "0.01", payment.FormatYuanAmount(1))
	assert.Equal(t, "1234.56", payment.FormatYuanAmount(123456))
	assert.Equal(t, "0.00", payment.FormatYuanAmount(0))

	// 分解析
	fen, err := payment.ParseYuanAmount("1.00")
	require.NoError(t, err)
	assert.Equal(t, int64(100), fen)

	fen, err = payment.ParseYuanAmount("0.01")
	require.NoError(t, err)
	assert.Equal(t, int64(1), fen)

	fen, err = payment.ParseYuanAmount("1234.56")
	require.NoError(t, err)
	assert.Equal(t, int64(123456), fen)

	// 非法格式报错
	_, err = payment.ParseYuanAmount("invalid")
	require.Error(t, err)
	_, err = payment.ParseYuanAmount("-1.00")
	require.Error(t, err)
}

func TestCertsDirResolution(t *testing.T) {
	tempDir := t.TempDir()
	certsDir := filepath.Join(tempDir, "storage", "app", "certs")
	wechatDir := filepath.Join(certsDir, "wechat")
	require.NoError(t, os.MkdirAll(wechatDir, 0o755))

	certContent := []byte("-----BEGIN CERTIFICATE-----\nMOCK\n-----END CERTIFICATE-----")
	certFile := filepath.Join(wechatDir, "apiclient_key.pem")
	require.NoError(t, os.WriteFile(certFile, certContent, 0o600))

	// 配置相对路径，测试通过 CertsDir 解析
	cfg := &config.Config{
		App: config.AppConfig{Env: "development"},
		Payment: config.PaymentConfig{
			CertsDir: certsDir,
			Wechat: config.WechatPaymentChannelConfig{
				Default: config.WechatPaymentAppConfig{
					AppID:                  "wx123456",
					MchID:                  "1234567890",
					MerchantSerialNo:       "SERIAL123",
					APIv3Key:               "12345678901234567890123456789012",
					MerchantPrivateKeyPath: "wechat/apiclient_key.pem",
					NotifyURL:              "https://example.com/notify",
					Scenes:                 []string{"wechat_native"},
				},
			},
		},
	}

	// 因 mock 证书内容不是合法 RSA 私钥，NewManager 在解析私钥时会触发底层解析失败，
	// 但这证实了文件已成功定位并被读取（而非报文件不存在）！
	_, err := payment.NewManager(cfg, zap.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wechat pay client")
}

func TestPaymentMoneyVO(t *testing.T) {
	// 1. CreateRequest 优先使用 Money 值对象
	m, err := money.NewFromCents(9999, "CNY")
	require.NoError(t, err)
	req := payment.CreateRequest{
		Money:  &m,
		Amount: 100, // 应当被 Money 覆盖
	}
	assert.Equal(t, int64(9999), req.EffectiveAmount())
	assert.Equal(t, "CNY", req.EffectiveMoney().Currency().Code)
	assert.Equal(t, int64(9999), req.EffectiveMoney().ToCents())

	// 回退至普通 Amount
	reqFallback := payment.CreateRequest{
		Amount: 8888,
	}
	assert.Equal(t, int64(8888), reqFallback.EffectiveAmount())
	assert.Equal(t, int64(8888), reqFallback.EffectiveMoney().ToCents())

	// 2. RefundRequest 金额推导
	totalM, err := money.NewFromCents(10000, "CNY")
	require.NoError(t, err)
	refundM, err := money.NewFromCents(5000, "CNY")
	require.NoError(t, err)
	refundReq := payment.RefundRequest{
		TotalMoney:   &totalM,
		RefundMoney:  &refundM,
		TotalAmount:  10,
		RefundAmount: 5,
	}
	assert.Equal(t, int64(10000), refundReq.EffectiveTotalAmount())
	assert.Equal(t, int64(5000), refundReq.EffectiveRefundAmount())

	// 3. PaymentResult WithMoney
	res := (&payment.PaymentResult{PaidAmount: 6600}).WithMoney(m)
	assert.NotNil(t, res.Money)
	assert.Equal(t, int64(9999), res.Money.ToCents())
}

type stubPayOnlyProvider struct{}

func (s *stubPayOnlyProvider) Channel() enums.PaymentChannel { return enums.PaymentChannelWechat }
func (s *stubPayOnlyProvider) Application() string           { return "stub" }
func (s *stubPayOnlyProvider) Scenes() []enums.PaymentScene {
	return []enums.PaymentScene{enums.PaymentSceneWechatNative}
}
func (s *stubPayOnlyProvider) HealthCheck() error { return nil }
func (s *stubPayOnlyProvider) Create(ctx context.Context, req payment.CreateRequest) (*payment.Instruction, error) {
	return &payment.Instruction{Scene: req.Scene}, nil
}
func (s *stubPayOnlyProvider) Close(ctx context.Context, req payment.CloseRequest) error { return nil }

func TestPaymentCapabilityISP(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Env: "development"},
		Payment: config.PaymentConfig{
			FakeEnabled: false,
		},
	}
	mgr, err := payment.NewManager(cfg, zap.NewNop())
	require.NoError(t, err)

	// 注册仅实现了 Payable 的提供者
	require.NoError(t, mgr.Register(&stubPayOnlyProvider{}))

	ctx := t.Context()

	// 1. Payable 能力支持，正常调用
	inst, err := mgr.Create(ctx, payment.CreateRequest{
		Scene:      enums.PaymentSceneWechatNative,
		OutTradeNo: "STUB_001",
		Amount:     100,
		Subject:    "Stub Pay",
	})
	require.NoError(t, err)
	assert.NotNil(t, inst)

	// 2. Refundable 能力未实现，调用退款必须返回 ErrCapabilityNotSupported
	_, err = mgr.Refund(ctx, enums.PaymentChannelWechat, payment.RefundRequest{
		OutTradeNo:   "STUB_001",
		OutRefundNo:  "REFUND_001",
		TotalAmount:  100,
		RefundAmount: 100,
	}, "stub")
	require.ErrorIs(t, err, payment.ErrCapabilityNotSupported)

	// 3. Queryable 能力未实现，调用查询必须返回 ErrCapabilityNotSupported
	_, err = mgr.Query(ctx, enums.PaymentChannelWechat, payment.QueryRequest{
		OutTradeNo: "STUB_001",
	}, "stub")
	require.ErrorIs(t, err, payment.ErrCapabilityNotSupported)
}

func TestPaymentDriverRegistry(t *testing.T) {
	// 验证动态注册驱动工厂 (OCP 原则)
	called := false
	payment.RegisterDriver("custom_driver", func(application string, appCfg any, isProd bool, certsDir string, logger *zap.Logger) (payment.Provider, error) {
		called = true
		return &stubPayOnlyProvider{}, nil
	})

	factory, ok := payment.GetDriverFactory("custom_driver")
	require.True(t, ok)
	require.NotNil(t, factory)

	p, err := factory("test_app", nil, false, "", zap.NewNop())
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "stub", p.Application())
}
