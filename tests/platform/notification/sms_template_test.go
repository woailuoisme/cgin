// Package notification_test 针对短信多厂商模板与参数自适应解析机制进行单元测试。
package notification_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/notification/channel/sms"
	"bgin/internal/platform/notification/channel/sms/aliyun"
	"bgin/internal/platform/notification/channel/sms/smsbao"
	"bgin/internal/platform/notification/channel/sms/tencent"
)

// TestSMSMessage_FluentTemplateAPIs 验证 Message 提供的多厂商模板链式 API 与解析优先级。
func TestSMSMessage_FluentTemplateAPIs(t *testing.T) {
	t.Parallel()

	msg := sms.NewMessage().
		WithPhone("13800138000").
		WithSignName("CommonSign").
		WithTemplate("COMMON_TPL_001").
		WithAliyunTemplate("SMS_ALI_99999").
		WithTencentTemplate("TC_88888", "1234", "5").
		WithSMSBaoTemplate("【{sign}】您的验证码为{code}，请于{time}分钟内使用。")

	// 1. 验证模板解析优先级：专属 > 通用 > 兜底
	assert.Equal(t, "SMS_ALI_99999", msg.ResolveTemplate("aliyun", "FALLBACK_TPL"))
	assert.Equal(t, "TC_88888", msg.ResolveTemplate("tencent", "FALLBACK_TPL"))
	assert.Equal(t, "【{sign}】您的验证码为{code}，请于{time}分钟内使用。", msg.ResolveTemplate("smsbao", "FALLBACK_TPL"))
	// 未指定专属模板的驱动，回退到通用模板
	assert.Equal(t, "COMMON_TPL_001", msg.ResolveTemplate("other_driver", "FALLBACK_TPL"))

	// 2. 验证签名解析优先级
	assert.Equal(t, "CommonSign", msg.ResolveSign("aliyun", "DefaultSign"))
	msg.WithDriverSign("aliyun", "AliCustomSign")
	assert.Equal(t, "AliCustomSign", msg.ResolveSign("aliyun", "DefaultSign"))
	assert.Equal(t, "CommonSign", msg.ResolveSign("tencent", "DefaultSign"))
}

// TestSMSMessage_ResolveTencentParamSet 验证腾讯云有序参数切片的智能自适应推断策略。
func TestSMSMessage_ResolveTencentParamSet(t *testing.T) {
	t.Parallel()

	t.Run("显式 ParamSet 优先", func(t *testing.T) {
		msg := sms.NewMessage().
			WithParam("code", "1234").
			WithParamSet("9999", "30")

		assert.Equal(t, []string{"9999", "30"}, msg.ResolveTencentParamSet())
	})

	t.Run("配置 ParamOrder 指定提取顺序", func(t *testing.T) {
		msg := sms.NewMessage().
			WithParam("expire", "10").
			WithParam("code", "4321").
			WithParam("action", "login").
			WithParamOrder("code", "expire")

		assert.Equal(t, []string{"4321", "10"}, msg.ResolveTencentParamSet())
	})

	t.Run("单参数 code/otp 自动提取", func(t *testing.T) {
		msg1 := sms.NewMessage().WithParam("code", "666888")
		assert.Equal(t, []string{"666888"}, msg1.ResolveTencentParamSet())

		msg2 := sms.NewMessage().WithParam("otp", "777999")
		assert.Equal(t, []string{"777999"}, msg2.ResolveTencentParamSet())
	})

	t.Run("纯数字键按数值升序提取", func(t *testing.T) {
		msg := sms.NewMessage().
			WithParam("2", "second").
			WithParam("1", "first").
			WithParam("10", "tenth")

		assert.Equal(t, []string{"first", "second", "tenth"}, msg.ResolveTencentParamSet())
	})

	t.Run("自然字典序兜底提取", func(t *testing.T) {
		msg := sms.NewMessage().
			WithParam("b_arg", "val_b").
			WithParam("a_arg", "val_a").
			WithParam("c_arg", "val_c")

		assert.Equal(t, []string{"val_a", "val_b", "val_c"}, msg.ResolveTencentParamSet())
	})

	t.Run("空参数安全返回", func(t *testing.T) {
		msg := sms.NewMessage()
		assert.Nil(t, msg.ResolveTencentParamSet())
	})
}

// TestSMSMessage_RenderContent 验证短信宝及通用占位符文本插值替换。
func TestSMSMessage_RenderContent(t *testing.T) {
	t.Parallel()

	msg := sms.NewMessage().
		WithParam("code", "8888").
		WithParam("time", "15").
		WithParamSet("8888", "15")

	tpl1 := "【{sign}】验证码是{code}，有效期${time}分钟。"
	rendered1 := msg.RenderContent(tpl1, "智能科技")
	assert.Equal(t, "【智能科技】验证码是8888，有效期15分钟。", rendered1)

	tpl2 := "【${sign}】您的业务代码为{1}，时长为{2}天。"
	rendered2 := msg.RenderContent(tpl2, "云服务")
	assert.Equal(t, "【云服务】您的业务代码为8888，时长为15天。", rendered2)

	assert.Empty(t, msg.RenderContent("", "Sign"))
}

// TestSMSBao_DriverTemplateAndFallback 验证短信宝驱动对模板、签名回退与纯文本的完整处理流程。
func TestSMSBao_DriverTemplateAndFallback(t *testing.T) {
	t.Parallel()

	var receivedContent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContent = r.URL.Query().Get("c")
		_, _ = w.Write([]byte("0")) // 0 表示短信宝发送成功
	}))
	defer server.Close()

	driver, err := smsbao.NewDriver(smsbao.Config{
		Username: "test_user",
		APIKey:   "d41d8cd98f00b204e9800998ecf8427e",
		SignName: "默认签名",
		Template: "【{sign}】您的验证码为{code}，请勿告知他人。",
		Endpoint: server.URL,
		Timeout:  2 * time.Second,
	})
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("使用配置默认模板渲染发送", func(t *testing.T) {
		msg := sms.NewMessage().
			WithPhone("13900000000").
			WithParam("code", "123456")

		err := driver.Send(ctx, msg)
		require.NoError(t, err)
		assert.Equal(t, "【默认签名】您的验证码为123456，请勿告知他人。", receivedContent)
	})

	t.Run("使用消息级专属模板渲染覆盖", func(t *testing.T) {
		msg := sms.NewMessage().
			WithPhone("13900000000").
			WithSignName("自定义APP").
			WithSMSBaoTemplate("【{sign}】正在登录系统，动态码：{code}。").
			WithParam("code", "987654")

		err := driver.Send(ctx, msg)
		require.NoError(t, err)
		assert.Equal(t, "【自定义APP】正在登录系统，动态码：987654。", receivedContent)
	})

	t.Run("显式 Content 优先级高于模板", func(t *testing.T) {
		msg := sms.NewMessage().
			WithPhone("13900000000").
			WithSMSBaoTemplate("【{sign}】模板内容").
			WithContent("【直接内容】完全定制的纯文本短信")

		err := driver.Send(ctx, msg)
		require.NoError(t, err)
		assert.Equal(t, "【直接内容】完全定制的纯文本短信", receivedContent)
	})
}

// TestTencent_DriverTemplateResolution 验证腾讯云驱动对多参数有序切片及专属模板的组装解析。
func TestTencent_DriverTemplateResolution(t *testing.T) {
	t.Parallel()

	var reqBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 2048)
		n, _ := r.Body.Read(buf)
		reqBody = string(buf[:n])

		// 模拟腾讯云成功响应
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Response": {
				"SendStatusSet": [
					{"SerialNo": "123", "PhoneNumber": "+8613800000000", "Fee": 1, "Code": "Ok", "Message": "send success"}
				],
				"RequestId": "req-123"
			}
		}`))
	}))
	defer server.Close()

	driver, err := tencent.NewDriver(tencent.Config{
		SecretID:   "AKIDtest123",
		SecretKey:  "SecretKeyTest123",
		AppID:      "1400000001",
		SignName:   "腾讯默认签名",
		TemplateID: "10001",
		Endpoint:   server.URL,
		Timeout:    2 * time.Second,
	})
	require.NoError(t, err)

	ctx := context.Background()

	// 传入包含厂商专属模板与有序多参数的消息
	msg := sms.NewMessage().
		WithPhone("13800000000").
		WithTencentTemplate("99999", "7788", "10").
		WithDriverSign("tencent", "专属腾讯签名")

	err = driver.Send(ctx, msg)
	require.NoError(t, err)

	// 验证请求报文中包含正确的 TemplateId、SignName 与 TemplateParamSet
	assert.Contains(t, reqBody, `"TemplateId":"99999"`)
	assert.Contains(t, reqBody, `"SignName":"专属腾讯签名"`)
	assert.Contains(t, reqBody, `"TemplateParamSet":["7788","10"]`)
}

// TestAliyun_DriverTemplateResolution 验证阿里云驱动对专属模板与参数字典的解析。
func TestAliyun_DriverTemplateResolution(t *testing.T) {
	t.Parallel()

	var formValues map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		formValues = make(map[string]string)
		for k, v := range r.PostForm {
			if len(v) > 0 {
				formValues[k] = v[0]
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Code":"OK","Message":"OK","BizId":"biz123","RequestId":"req123"}`))
	}))
	defer server.Close()

	driver, err := aliyun.NewDriver(aliyun.Config{
		AccessKeyID:     "ak_aliyun_test",
		AccessKeySecret: "secret_aliyun_test",
		SignName:        "阿里默认签名",
		TemplateCode:    "SMS_DEFAULT",
		Endpoint:        server.URL,
		Timeout:         2 * time.Second,
	})
	require.NoError(t, err)

	ctx := context.Background()

	msg := sms.NewMessage().
		WithPhone("13800000000").
		WithAliyunTemplate("SMS_CUSTOM_888", map[string]string{
			"code": "112233",
			"ttl":  "5",
		})

	err = driver.Send(ctx, msg)
	require.NoError(t, err)

	assert.Equal(t, "SMS_CUSTOM_888", formValues["TemplateCode"])
	assert.Equal(t, "阿里默认签名", formValues["SignName"])
	assert.Contains(t, formValues["TemplateParam"], `"code":"112233"`)
	assert.Contains(t, formValues["TemplateParam"], `"ttl":"5"`)
}
