package wechat_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/config"
	"bgin/internal/platform/wechat"
)

func TestWeChatClientCreationAndAccessors(t *testing.T) {
	cfg := config.WeChatMiniProgramConfig{
		AppID:     "wx1234567890abcdef",
		AppSecret: "secret1234567890abcdef1234567890",
	}

	client := wechat.NewClient(cfg, nil)
	require.NotNil(t, client)
	assert.Equal(t, cfg.AppID, client.AppID())

	// 验证各细粒度子服务门面
	assert.NotNil(t, client.Auth())
	assert.NotNil(t, client.Phone())
	assert.NotNil(t, client.WxaCode())

	// 验证链式 WithBaseURL
	returned := client.WithBaseURL("https://api.weixin.qq.com/")
	assert.Equal(t, client, returned)
}

func TestWeChatClientUninitializedOperations(t *testing.T) {
	// 空凭据构造客户端应返回 nil
	client := wechat.NewClient(config.WeChatMiniProgramConfig{}, nil)
	assert.Nil(t, client)

	ctx := t.Context()

	// nil 客户端调用各方法应优雅返回错误
	_, err := client.Code2Session(ctx, "mock_code")
	require.Error(t, err)

	_, err = client.GetPhoneNumber(ctx, "mock_code")
	require.Error(t, err)

	_, err = client.GetUnlimitedCode(ctx, wechat.UnlimitedCodeParams{
		Scene: "id=123",
	})
	require.Error(t, err)

	assert.Empty(t, client.AppID())
	assert.Nil(t, client.Auth())
	assert.Nil(t, client.Phone())
	assert.Nil(t, client.WxaCode())
}

func TestWeChatManagerMultiAppResolution(t *testing.T) {
	cfg := config.WeChatConfig{
		ApplicationsJSON: `{
			"user": {
				"app_id": "wx_user_111111",
				"app_secret": "secret_user_111111"
			},
			"merchant": {
				"app_id": "wx_merchant_222222",
				"app_secret": "secret_merchant_222222"
			}
		}`,
		MiniProgram: config.WeChatMiniProgramConfig{
			AppID:     "wx_default_000000",
			AppSecret: "secret_default_000000",
		},
	}

	mgr, err := wechat.NewManager(cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, mgr)

	// 验证应用列表
	apps := mgr.Applications()
	assert.Contains(t, apps, "default")
	assert.Contains(t, apps, "user")
	assert.Contains(t, apps, "merchant")

	// 默认应用获取
	defApp, err := mgr.Default()
	require.NoError(t, err)
	assert.Equal(t, "wx_default_000000", defApp.AppID())

	// 按名称检索
	userApp, err := mgr.App("user")
	require.NoError(t, err)
	assert.Equal(t, "wx_user_111111", userApp.AppID())

	merchantApp, err := mgr.App("merchant")
	require.NoError(t, err)
	assert.Equal(t, "wx_merchant_222222", merchantApp.AppID())

	// 按 AppID 检索
	appByID, err := mgr.AppByID("wx_user_111111")
	require.NoError(t, err)
	assert.Equal(t, "wx_user_111111", appByID.AppID())

	// 未找到场景
	_, err = mgr.App("non_existent")
	require.Error(t, err)

	_, err = mgr.AppByID("non_existent_appid")
	require.Error(t, err)

	// 动态注册
	customMock := wechat.NewFakeClient("wx_custom_333333")
	mgr.Register("custom", customMock)
	retrieved, err := mgr.App("custom")
	require.NoError(t, err)
	assert.Equal(t, "wx_custom_333333", retrieved.AppID())
}

func TestWeChatManagerUnconfigured(t *testing.T) {
	mgr, err := wechat.NewManager(config.WeChatConfig{}, nil)
	require.NoError(t, err)
	assert.Nil(t, mgr)

	_, err = mgr.Default()
	require.Error(t, err)

	_, err = mgr.App("user")
	require.Error(t, err)

	_, err = mgr.AppByID("wx123")
	require.Error(t, err)

	assert.Nil(t, mgr.Applications())
}

func TestWeChatFakeClientAndManager(t *testing.T) {
	fakeClient := wechat.NewFakeClient("wx_fake_test_appid")
	require.NotNil(t, fakeClient)
	assert.Equal(t, "wx_fake_test_appid", fakeClient.AppID())

	ctx := t.Context()

	// 默认 Mock 输出验证
	session, err := fakeClient.Auth().Code2Session(ctx, "test_code_123")
	require.NoError(t, err)
	assert.Equal(t, "mock_openid_test_code_123", session.OpenID)
	assert.Equal(t, "mock_unionid_test_code_123", session.UnionID)
	assert.Equal(t, "mock_session_key_1234567890", session.SessionKey)

	phone, err := fakeClient.Phone().GetPhoneNumber(ctx, "test_code_123")
	require.NoError(t, err)
	assert.Equal(t, "+8613800000000", phone.PhoneNumber)
	assert.Equal(t, "13800000000", phone.PurePhoneNumber)
	assert.Equal(t, "wx_fake_test_appid", phone.Watermark.AppID)

	codeBytes, err := fakeClient.WxaCode().GetUnlimited(ctx, wechat.UnlimitedCodeParams{Scene: "id=1"})
	require.NoError(t, err)
	assert.NotEmpty(t, codeBytes)

	// 自定义钩子验证
	fakeClient.Code2SessionFunc = func(_ context.Context, code string) (*wechat.Session, error) {
		return &wechat.Session{OpenID: "custom_" + code}, nil
	}
	customSession, err := fakeClient.Auth().Code2Session(ctx, "custom_code")
	require.NoError(t, err)
	assert.Equal(t, "custom_custom_code", customSession.OpenID)

	// FakeManager 验证
	fakeMgr := wechat.NewFakeManager("wx_fake_mgr_app")
	require.NotNil(t, fakeMgr)
	defApp, err := fakeMgr.Default()
	require.NoError(t, err)
	assert.Equal(t, "wx_fake_mgr_app", defApp.AppID())
}

func TestWeChatSessionSecurityAndMasking(t *testing.T) {
	session := &wechat.Session{
		OpenID:     "test_openid",
		UnionID:    "test_unionid",
		SessionKey: "super_secret_session_key_12345",
	}

	// 1. JSON 序列化严禁包含 SessionKey
	raw, err := json.Marshal(session)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "super_secret_session_key_12345")
	assert.NotContains(t, string(raw), "session_key")
	assert.NotContains(t, string(raw), "SessionKey")

	// 2. fmt.Stringer 与 GoStringer 强制脱敏
	str := session.String()
	assert.Contains(t, str, "[REDACTED]")
	assert.NotContains(t, str, "super_secret_session_key_12345")

	goStr := fmt.Sprintf("%#v", session)
	assert.Contains(t, goStr, "[REDACTED]")
	assert.NotContains(t, goStr, "super_secret_session_key_12345")

	var nilSession *wechat.Session
	assert.Equal(t, "<nil>", nilSession.String())
}

func TestWeChatWatermarkValidationAndClockDrift(t *testing.T) {
	now := time.Now()
	expectedAppID := "wx_my_app_id"

	// 1. 正常水印
	validWm := wechat.Watermark{
		AppID:     expectedAppID,
		Timestamp: now.Unix(),
	}
	require.NoError(t, validWm.Validate(expectedAppID, now))

	// 2. AppID 不匹配
	mismatchWm := wechat.Watermark{
		AppID:     "wx_other_app_id",
		Timestamp: now.Unix(),
	}
	require.Error(t, mismatchWm.Validate(expectedAppID, now))

	// 3. 超过 5 分钟前（重放攻击）
	expiredWm := wechat.Watermark{
		AppID:     expectedAppID,
		Timestamp: now.Add(-6 * time.Minute).Unix(),
	}
	require.Error(t, expiredWm.Validate(expectedAppID, now))

	// 4. 超前 5 分钟后（时钟异常）
	futureWm := wechat.Watermark{
		AppID:     expectedAppID,
		Timestamp: now.Add(6 * time.Minute).Unix(),
	}
	require.Error(t, futureWm.Validate(expectedAppID, now))
}

func TestWeChatPhoneInfoE164(t *testing.T) {
	// 标准情况
	p1 := &wechat.PhoneInfo{
		PurePhoneNumber: "13800138000",
		CountryCode:     "86",
	}
	e164, err := p1.E164()
	require.NoError(t, err)
	assert.Equal(t, "+8613800138000", e164)

	// 带前导 +
	p2 := &wechat.PhoneInfo{
		PurePhoneNumber: "13800138000",
		CountryCode:     "+86",
	}
	e164, err = p2.E164()
	require.NoError(t, err)
	assert.Equal(t, "+8613800138000", e164)

	// 缺字段
	p3 := &wechat.PhoneInfo{PurePhoneNumber: "", CountryCode: "86"}
	_, err = p3.E164()
	require.Error(t, err)

	var nilInfo *wechat.PhoneInfo
	_, err = nilInfo.E164()
	require.Error(t, err)
}

func TestWeChatCryptoDecryptionAndSignature(t *testing.T) {
	keyBytes := []byte("0123456789abcdef") // 16 bytes
	ivBytes := []byte("fedcba9876543210")  // 16 bytes
	plainData := []byte(`{"phoneNumber":"13800138000","watermark":{"appid":"wx123","timestamp":1600000000}}`)

	// 构造 PKCS#7 填充
	blockSize := 16
	padding := blockSize - (len(plainData) % blockSize)
	padded := append(slices.Clone(plainData), bytes.Repeat([]byte{byte(padding)}, padding)...)

	block, err := aes.NewCipher(keyBytes)
	require.NoError(t, err)

	cipherData := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, ivBytes)
	mode.CryptBlocks(cipherData, padded)

	sessionKeyB64 := base64.StdEncoding.EncodeToString(keyBytes)
	ivB64 := base64.StdEncoding.EncodeToString(ivBytes)
	encryptedDataB64 := base64.StdEncoding.EncodeToString(cipherData)

	// 执行解密
	decrypted, err := wechat.Decrypt(sessionKeyB64, encryptedDataB64, ivB64)
	require.NoError(t, err)
	assert.Equal(t, plainData, decrypted)

	// 非法入参拦截
	_, err = wechat.Decrypt("invalid_base64!", encryptedDataB64, ivB64)
	require.Error(t, err)

	_, err = wechat.Decrypt(sessionKeyB64, "invalid_base64!", ivB64)
	require.Error(t, err)

	_, err = wechat.Decrypt(sessionKeyB64, encryptedDataB64, "invalid_base64!")
	require.Error(t, err)

	// 签名验证
	rawData := `{"nickName":"Bandit","gender":1}`
	hasher := sha1.New()
	hasher.Write([]byte(rawData))
	hasher.Write([]byte(sessionKeyB64))
	correctSignature := hex.EncodeToString(hasher.Sum(nil))

	assert.True(t, wechat.VerifySignature(sessionKeyB64, rawData, correctSignature))
	assert.False(t, wechat.VerifySignature(sessionKeyB64, rawData, "wrong_signature_here"))
	assert.False(t, wechat.VerifySignature("", rawData, correctSignature))
}

func TestWeChatUnlimitedCodeParamsValidation(t *testing.T) {
	// 合法参数
	validParams := wechat.UnlimitedCodeParams{
		Scene:      "id=123&type=abc",
		Page:       "pages/index/index",
		EnvVersion: wechat.EnvVersionRelease,
	}
	require.NoError(t, validParams.Validate())

	// Scene 为空
	require.Error(t, wechat.UnlimitedCodeParams{Scene: ""}.Validate())

	// Scene 超长（> 32 字符）
	require.Error(t, wechat.UnlimitedCodeParams{Scene: "123456789012345678901234567890123"}.Validate())

	// Scene 包含非法字符
	require.Error(t, wechat.UnlimitedCodeParams{Scene: "id=123^bad"}.Validate())

	// Page 带前导斜杠
	require.Error(t, wechat.UnlimitedCodeParams{
		Scene: "id=123",
		Page:  "/pages/index/index",
	}.Validate())

	// 非法 EnvVersion
	require.Error(t, wechat.UnlimitedCodeParams{
		Scene:      "id=123",
		EnvVersion: "invalid_env",
	}.Validate())

	// Normalize 清洗
	raw := wechat.UnlimitedCodeParams{
		Scene: "  id=123  ",
		Page:  "  pages/index/index  ",
	}
	normalized := raw.Normalize()
	assert.Equal(t, "id=123", normalized.Scene)
	assert.Equal(t, "pages/index/index", normalized.Page)
}

func TestWeChatTokenL1AndL2Caching(t *testing.T) {
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/stable_token" {
			tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "mock_stable_token_xyz",
				"expires_in":   7200,
			})
			return
		}
		if r.URL.Path == "/wxa/business/getuserphonenumber" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errcode": 0,
				"phone_info": map[string]any{
					"phoneNumber":     "+8613800138000",
					"purePhoneNumber": "13800138000",
					"countryCode":     "86",
					"watermark": map[string]any{
						"appid":     "wx_test_caching",
						"timestamp": time.Now().Unix(),
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := config.WeChatMiniProgramConfig{
		AppID:     "wx_test_caching",
		AppSecret: "secret_test_caching",
	}

	client := wechat.NewClient(cfg, nil, wechat.WithBaseURL(server.URL))
	require.NotNil(t, client)

	ctx := t.Context()

	// 并发 10 次获取手机号
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			info, err := client.Phone().GetPhoneNumber(ctx, "mock_code")
			require.NoError(t, err)
			assert.NotNil(t, info)
		})
	}
	wg.Wait()

	// 10 次并发请求，由于 SingleFlight 与 L1/L2 缓存，stable_token 应该只被请求 1 次！
	assert.Equal(t, int32(1), tokenCalls.Load())
}

func TestWeChatTokenRefreshOnCredentialFailure(t *testing.T) {
	var tokenCalls atomic.Int32
	var phoneCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/stable_token" {
			tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": fmt.Sprintf("mock_token_%d", tokenCalls.Load()),
				"expires_in":   7200,
			})
			return
		}
		if r.URL.Path == "/wxa/business/getuserphonenumber" {
			call := phoneCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if call == 1 {
				// 第一次返回 40001 (凭据失效)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"errcode": 40001,
					"errmsg":  "invalid credential, access_token is invalid or not latest",
				})
				return
			}
			// 刷新凭据重试后成功
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errcode": 0,
				"phone_info": map[string]any{
					"phoneNumber":     "+8613800138000",
					"purePhoneNumber": "13800138000",
					"countryCode":     "86",
					"watermark": map[string]any{
						"appid":     "wx_test_refresh",
						"timestamp": time.Now().Unix(),
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := config.WeChatMiniProgramConfig{
		AppID:     "wx_test_refresh",
		AppSecret: "secret_test_refresh",
	}

	client := wechat.NewClient(cfg, nil, wechat.WithBaseURL(server.URL))
	require.NotNil(t, client)

	phone, err := client.Phone().GetPhoneNumber(t.Context(), "code123")
	require.NoError(t, err)
	assert.Equal(t, "+8613800138000", phone.PhoneNumber)

	// 第一次 token 取了 1 次，被 40001 击退后刷新又取了 1 次，共 2 次
	assert.Equal(t, int32(2), tokenCalls.Load())
	// 手机号接口调用了 2 次（第 1 次 40001，第 2 次重试成功）
	assert.Equal(t, int32(2), phoneCalls.Load())
}
