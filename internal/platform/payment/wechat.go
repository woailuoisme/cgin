package payment

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/wechat/v3"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/payment/enums"
)

// 微信支付币种常量。
const wechatCurrencyCNY = "CNY"

// 微信支付 H5 场景的终端类型，H5 支付仅支持 Wap。
const wechatH5InfoType = "Wap"

// wechatRefundEventPrefix 标识微信退款类通知事件。
const wechatRefundEventPrefix = "REFUND."

// 微信支付退款状态常量，取值来自微信支付 v3 退款协议。
const (
	wechatRefundStatusSuccess    = "SUCCESS"
	wechatRefundStatusClosed     = "CLOSED"
	wechatRefundStatusProcessing = "PROCESSING"
	wechatRefundStatusAbnormal   = "ABNORMAL"
)

// wechatProvider 实现绑定到单个微信支付应用的 Provider。
type wechatProvider struct {
	appKey            string
	appID             string
	mchID             string
	apiV3Key          string
	notifyURL         string
	scenes            map[enums.PaymentScene]struct{}
	client            *wechat.ClientV3
	platformPublicKey *rsa.PublicKey
}

var _ FullProvider = (*wechatProvider)(nil)

// newWechatProvider 依据渠道应用配置构造微信支付 Provider。
// 商户私钥与平台公钥在构造期读入内存；任一必需材料缺失或加载失败都会让构造直接失败，
// 从而杜绝「证书未加载导致验签被静默跳过」的隐患。
func newWechatProvider(
	appKey string,
	cfg config.WechatPaymentAppConfig,
	isProd bool,
	certsDir string,
	logger *zap.Logger,
) (*wechatProvider, error) {
	scenes, err := resolveScenes(cfg.Scenes, enums.PaymentChannelWechat)
	if err != nil {
		return nil, err
	}

	if err := requireWechatCredentials(appKey, cfg); err != nil {
		return nil, err
	}

	privateKey, err := readCredentialFile(certsDir, cfg.MerchantPrivateKeyPath)
	if err != nil {
		return nil, err
	}

	client, err := wechat.NewClientV3(cfg.MchID, cfg.MerchantSerialNo, cfg.APIv3Key, string(privateKey))
	if err != nil {
		return nil, errs.Wrap(
			errs.CodeServiceUnavailable,
			fmt.Sprintf("failed to construct wechat pay client for %s: %v", appKey, err),
			err,
		)
	}
	client.SetLogger(newXLogAdapter(logger, !isProd))

	provider := &wechatProvider{
		appKey:    appKey,
		appID:     cfg.AppID,
		mchID:     cfg.MchID,
		apiV3Key:  cfg.APIv3Key,
		notifyURL: strings.TrimSpace(cfg.NotifyURL),
		scenes:    scenes,
		client:    client,
	}

	if err := provider.loadPlatformKey(cfg, certsDir); err != nil {
		return nil, err
	}

	return provider, nil
}

// requireWechatCredentials 校验微信支付应用的必填凭据。
func requireWechatCredentials(appKey string, cfg config.WechatPaymentAppConfig) error {
	return requireCredentials("wechat pay", appKey, []credentialField{
		{Name: "app_id", Value: cfg.AppID},
		{Name: "mch_id", Value: cfg.MchID},
		{Name: "merchant_serial_no", Value: cfg.MerchantSerialNo},
		{Name: "api_v3_key", Value: cfg.APIv3Key},
		{Name: "merchant_private_key_path", Value: cfg.MerchantPrivateKeyPath},
		{Name: "notify_url", Value: cfg.NotifyURL},
	})
}

// loadPlatformKey 加载微信支付平台公钥材料。
// 配置了公钥文件时走公钥模式；否则触发平台证书自动下载与轮换。
// 两条路径都失败时构造必须中止，绝不进入「验签静默跳过」的状态。
func (p *wechatProvider) loadPlatformKey(cfg config.WechatPaymentAppConfig, certsDir string) error {
	if strings.TrimSpace(cfg.PlatformPublicKeyPath) != "" {
		return p.loadPlatformPublicKey(cfg, certsDir)
	}

	if err := p.client.AutoVerifySign(true); err != nil {
		return errs.Wrap(
			errs.CodeServiceUnavailable,
			fmt.Sprintf("failed to download wechat platform certificates for %s: %v", p.appKey, err),
			err,
		)
	}

	return nil
}

// loadPlatformPublicKey 以微信支付公钥模式加载平台公钥。
func (p *wechatProvider) loadPlatformPublicKey(cfg config.WechatPaymentAppConfig, certsDir string) error {
	content, err := readCredentialFile(certsDir, cfg.PlatformPublicKeyPath)
	if err != nil {
		return err
	}

	publicKey, err := decodeRSAPublicKey(content)
	if err != nil {
		return errs.Internal(fmt.Errorf("failed to decode wechat platform public key for %s: %w", p.appKey, err))
	}

	if err := p.client.AutoVerifySignByPublicKey(content, cfg.PlatformPublicKeyID); err != nil {
		return errs.Wrap(
			errs.CodeServiceUnavailable,
			fmt.Sprintf("failed to register wechat platform public key for %s: %v", p.appKey, err),
			err,
		)
	}

	p.platformPublicKey = publicKey

	return nil
}

// Channel 返回微信支付渠道。
func (p *wechatProvider) Channel() enums.PaymentChannel { return enums.PaymentChannelWechat }

// Application 返回该 Provider 绑定的应用标识。
func (p *wechatProvider) Application() string { return p.appKey }

// Scenes 返回该应用声明可承载的支付场景。
func (p *wechatProvider) Scenes() []enums.PaymentScene { return sortedScenes(p.scenes) }

// HealthCheck 检查客户端与平台公钥材料是否就绪。
func (p *wechatProvider) HealthCheck() error {
	if p.client == nil {
		return errs.ServiceUnavailable("wechat pay client is not initialized for " + p.appKey)
	}

	_, _, err := p.verifyKeys()

	return err
}

// verifyKeys 返回通知验签所需的平台公钥材料。
// 公钥模式下只有单把公钥；平台证书模式下从客户端已下载的证书缓存中提取。
// 两者皆空时必须报错，绝不允许静默跳过验签。
func (p *wechatProvider) verifyKeys() (map[string]*rsa.PublicKey, *rsa.PublicKey, error) {
	if p.platformPublicKey != nil {
		return nil, p.platformPublicKey, nil
	}

	certificates := make(map[string]*rsa.PublicKey)
	p.client.SnCertMap.Range(func(serial string, publicKey *rsa.PublicKey) bool {
		certificates[serial] = publicKey
		return true
	})
	if len(certificates) == 0 {
		return nil, nil, errs.ServiceUnavailable(
			"wechat platform certificates are not loaded for " + p.appKey,
		)
	}

	return certificates, nil, nil
}

// Create 发起一次微信收款并返回支付指令。
func (p *wechatProvider) Create(ctx context.Context, req CreateRequest) (*Instruction, error) {
	if err := validateCreate(req); err != nil {
		return nil, err
	}
	if _, ok := p.scenes[req.Scene]; !ok {
		return nil, errs.BadRequest(
			fmt.Sprintf("payment scene %s is not enabled for application %s", req.Scene.String(), p.appKey),
		)
	}

	body := p.createBodyMap(req)

	switch req.Scene {
	case enums.PaymentSceneWechatNative:
		return p.createNativeInstruction(ctx, req, body)
	case enums.PaymentSceneWechatH5:
		return p.createH5Instruction(ctx, req, body)
	case enums.PaymentSceneWechatApp:
		return p.createAppInstruction(ctx, req, body)
	case enums.PaymentSceneWechatJsapi:
		return p.createJsapiInstruction(ctx, req, body)
	case enums.PaymentSceneWechatMiniProgram:
		return p.createMiniProgramInstruction(ctx, req, body)
	default:
		return nil, errs.BadRequest("unsupported wechat payment scene " + req.Scene.String())
	}
}

// createNativeInstruction 调用 Native 下单接口并返回二维码链接。
func (p *wechatProvider) createNativeInstruction(
	ctx context.Context,
	req CreateRequest,
	body gopay.BodyMap,
) (*Instruction, error) {
	const operation = "create wechat native payment"

	response, err := p.client.V3TransactionNative(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.Code, response.Error, operation); err != nil {
		return nil, err
	}

	return &Instruction{
		Scene:  req.Scene,
		Action: enums.PaymentInstructionQRCode,
		Value:  response.Response.CodeUrl,
	}, nil
}

// createH5Instruction 调用 H5 下单接口并返回收银台跳转地址。
func (p *wechatProvider) createH5Instruction(
	ctx context.Context,
	req CreateRequest,
	body gopay.BodyMap,
) (*Instruction, error) {
	const operation = "create wechat h5 payment"

	if strings.TrimSpace(req.ClientIP) == "" {
		return nil, errs.BadRequest("client ip is required for wechat h5 payment")
	}

	body.SetBodyMap("scene_info", func(info gopay.BodyMap) {
		info.Set("payer_client_ip", req.ClientIP)
		info.SetBodyMap("h5_info", func(h5 gopay.BodyMap) { h5.Set("type", wechatH5InfoType) })
	})

	response, err := p.client.V3TransactionH5(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.Code, response.Error, operation); err != nil {
		return nil, err
	}

	return &Instruction{
		Scene:  req.Scene,
		Action: enums.PaymentInstructionRedirectURL,
		Value:  response.Response.H5Url,
	}, nil
}

// createAppInstruction 调用 APP 下单接口并返回客户端调起参数。
func (p *wechatProvider) createAppInstruction(
	ctx context.Context,
	req CreateRequest,
	body gopay.BodyMap,
) (*Instruction, error) {
	const operation = "create wechat app payment"

	response, err := p.client.V3TransactionApp(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.Code, response.Error, operation); err != nil {
		return nil, err
	}

	params, err := p.client.PaySignOfApp(p.appID, response.Response.PrepayId)
	if err != nil {
		return nil, p.upstreamError("sign wechat app payment", err)
	}

	return newAppInstruction(req.Scene, params), nil
}

// createJsapiInstruction 调用 JSAPI 下单接口并返回公众号调起参数。
func (p *wechatProvider) createJsapiInstruction(
	ctx context.Context,
	req CreateRequest,
	body gopay.BodyMap,
) (*Instruction, error) {
	const operation = "create wechat jsapi payment"

	response, err := p.client.V3TransactionJsapi(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.Code, response.Error, operation); err != nil {
		return nil, err
	}

	params, err := p.client.PaySignOfJSAPI(p.appID, response.Response.PrepayId)
	if err != nil {
		return nil, p.upstreamError("sign wechat jsapi payment", err)
	}

	return newJsapiInstruction(req.Scene, params), nil
}

// createMiniProgramInstruction 调用 JSAPI 下单接口并返回小程序调起参数。
// 微信小程序支付与 JSAPI 共用下单接口，仅二次签名方式不同。
func (p *wechatProvider) createMiniProgramInstruction(
	ctx context.Context,
	req CreateRequest,
	body gopay.BodyMap,
) (*Instruction, error) {
	const operation = "create wechat miniprogram payment"

	response, err := p.client.V3TransactionJsapi(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.Code, response.Error, operation); err != nil {
		return nil, err
	}

	params, err := p.client.PaySignOfApplet(p.appID, response.Response.PrepayId)
	if err != nil {
		return nil, p.upstreamError("sign wechat miniprogram payment", err)
	}

	return newJsapiInstruction(req.Scene, (*wechat.JSAPIPayParams)(params)), nil
}

// createBodyMap 构造微信下单接口的公共请求参数。
func (p *wechatProvider) createBodyMap(req CreateRequest) gopay.BodyMap {
	body := make(gopay.BodyMap).
		Set("appid", p.appID).
		Set("mchid", p.mchID).
		Set("description", req.Subject).
		Set("out_trade_no", req.OutTradeNo).
		SetBodyMap("amount", func(amount gopay.BodyMap) {
			amount.Set("total", req.EffectiveAmount()).Set("currency", wechatCurrencyCNY)
		})

	if notifyURL := resolveNotifyURL(req.NotifyURL, p.notifyURL); notifyURL != "" {
		body.Set("notify_url", notifyURL)
	}
	if req.PayerID != "" {
		body.SetBodyMap("payer", func(payer gopay.BodyMap) { payer.Set("openid", req.PayerID) })
	}
	if req.Attach != "" {
		body.Set("attach", req.Attach)
	}
	if !req.ExpireAt.IsZero() {
		body.Set("time_expire", req.ExpireAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
	}

	return body
}

// Query 主动查询微信支付状态。
func (p *wechatProvider) Query(ctx context.Context, req QueryRequest) (*PaymentResult, error) {
	const operation = "query wechat payment"

	if strings.TrimSpace(req.OutTradeNo) == "" {
		return nil, errs.BadRequest("out trade no is required")
	}

	response, err := p.client.V3TransactionQueryOrder(ctx, wechat.OutTradeNo, req.OutTradeNo)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.Code, response.Error, operation); err != nil {
		return nil, err
	}

	return mapWechatQueryOrderResponse(response.Response), nil
}

// Close 关闭一笔未支付的微信收款。
func (p *wechatProvider) Close(ctx context.Context, req CloseRequest) error {
	const operation = "close wechat payment"

	if strings.TrimSpace(req.OutTradeNo) == "" {
		return errs.BadRequest("out trade no is required")
	}

	response, err := p.client.V3TransactionCloseOrder(ctx, req.OutTradeNo)
	if err != nil {
		return p.upstreamError(operation, err)
	}

	return p.checkResponse(response.Code, response.Error, operation)
}

// Refund 发起微信退款。
func (p *wechatProvider) Refund(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	const operation = "create wechat refund"

	if err := validateRefund(req); err != nil {
		return nil, err
	}

	body := make(gopay.BodyMap).
		Set("out_trade_no", req.OutTradeNo).
		Set("out_refund_no", req.OutRefundNo).
		SetBodyMap("amount", func(amount gopay.BodyMap) {
			amount.Set("total", req.EffectiveTotalAmount()).
				Set("refund", req.EffectiveRefundAmount()).
				Set("currency", wechatCurrencyCNY)
		})

	if req.Reason != "" {
		body.Set("reason", req.Reason)
	}
	if notifyURL := resolveNotifyURL(req.NotifyURL, p.notifyURL); notifyURL != "" {
		body.Set("notify_url", notifyURL)
	}

	response, err := p.client.V3Refund(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.Code, response.Error, operation); err != nil {
		return nil, err
	}

	return mapWechatRefundResponse(response.Response), nil
}

// QueryRefund 主动查询微信退款状态。
func (p *wechatProvider) QueryRefund(ctx context.Context, req QueryRefundRequest) (*RefundResult, error) {
	const operation = "query wechat refund"

	if strings.TrimSpace(req.OutRefundNo) == "" {
		return nil, errs.BadRequest("out refund no is required")
	}

	// 直连商户模式无需额外 query 参数，gopay 允许传 nil。
	response, err := p.client.V3RefundQuery(ctx, req.OutRefundNo, nil)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.Code, response.Error, operation); err != nil {
		return nil, err
	}

	return mapWechatRefundResponse((*wechat.RefundOrderResponse)(response.Response)), nil
}

// ParseNotification 解析并验签微信支付通知。
func (p *wechatProvider) ParseNotification(req *http.Request) (*Notification, error) {
	if req == nil {
		return nil, errs.BadRequest("wechat notification request is required")
	}

	notifyRequest, err := wechat.V3ParseNotify(req)
	if err != nil {
		return nil, errs.BadRequest("failed to parse wechat notification for " + p.appKey)
	}

	if err := p.verifyNotificationSign(notifyRequest); err != nil {
		return nil, err
	}

	notification := &Notification{
		ID:         notifyRequest.Id,
		Event:      notifyRequest.EventType,
		OccurredAt: channelTimeOrNow(notifyRequest.CreateTime),
	}

	if strings.HasPrefix(notifyRequest.EventType, wechatRefundEventPrefix) {
		return p.attachRefundNotification(notifyRequest, notification)
	}

	return p.attachPaymentNotification(notifyRequest, notification)
}

// verifyNotificationSign 校验微信通知签名。
// gopay 在平台公钥缺失时会静默通过库内验签，因此这里必须自行取用公钥材料，
// 取不到材料一律报错，绝不放过任何未验签的通知。
func (p *wechatProvider) verifyNotificationSign(notifyRequest *wechat.V3NotifyReq) error {
	certificates, publicKey, err := p.verifyKeys()
	if err != nil {
		return err
	}

	if publicKey != nil {
		if err := notifyRequest.VerifySignByPK(publicKey); err != nil {
			return errs.Unauthorized(errs.CodeAuthTokenInvalid, "invalid wechat notification signature for "+p.appKey)
		}

		return nil
	}

	if err := notifyRequest.VerifySignByPKMap(certificates); err != nil {
		return errs.Unauthorized(errs.CodeAuthTokenInvalid, "invalid wechat notification signature for "+p.appKey)
	}

	return nil
}

// attachPaymentNotification 解密并映射支付结果通知。
func (p *wechatProvider) attachPaymentNotification(
	notifyRequest *wechat.V3NotifyReq,
	notification *Notification,
) (*Notification, error) {
	decrypted, err := notifyRequest.DecryptPayCipherText(p.apiV3Key)
	if err != nil {
		return nil, errs.BadRequest("failed to decrypt wechat payment notification for " + p.appKey)
	}

	// 平台证书只证明报文来自微信，不证明报文属于本商户：必须再比对报文内的商户号与应用号。
	if decrypted.Mchid != p.mchID || decrypted.Appid != p.appID {
		return nil, errs.Forbidden("wechat payment notification merchant or app mismatch for " + p.appKey)
	}

	encoded, err := json.Marshal(decrypted)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to encode wechat payment notification payload: %w", err))
	}

	notification.Kind = enums.PaymentNotificationKindPayment
	notification.Payload = encoded
	notification.Payment = mapWechatDecryptPayResult(decrypted)

	return notification, nil
}

// attachRefundNotification 解密并映射退款结果通知。
func (p *wechatProvider) attachRefundNotification(
	notifyRequest *wechat.V3NotifyReq,
	notification *Notification,
) (*Notification, error) {
	decrypted, err := notifyRequest.DecryptRefundCipherText(p.apiV3Key)
	if err != nil {
		return nil, errs.BadRequest("failed to decrypt wechat refund notification for " + p.appKey)
	}

	// 退款通知同样必须归属本商户。
	if decrypted.Mchid != p.mchID {
		return nil, errs.Forbidden("wechat refund notification merchant mismatch for " + p.appKey)
	}

	encoded, err := json.Marshal(decrypted)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to encode wechat refund notification payload: %w", err))
	}

	notification.Kind = enums.PaymentNotificationKindRefund
	notification.Payload = encoded
	notification.Refund = mapWechatDecryptRefundResult(decrypted)

	return notification, nil
}

// upstreamError 包装微信支付调用失败，统一挂载上游不可用错误码。
func (p *wechatProvider) upstreamError(operation string, err error) error {
	return upstreamUnavailable("wechat pay", p.appKey, operation, err)
}

// checkResponse 判定微信支付响应是否为成功。
// gopay 在 HTTP 状态非 200 时返回 err == nil 且把 Code 置为 HTTP 状态码，
// 若不显式检查 Code 就会把渠道失败误判为成功。
func (p *wechatProvider) checkResponse(code int, rawError string, operation string) error {
	if code == wechat.Success {
		return nil
	}

	cause := fmt.Errorf("wechat pay responded with status code %d: %s", code, truncateForLog(rawError))
	return upstreamUnavailable("wechat pay", p.appKey, operation, cause)
}

// newAppInstruction 将微信 App 支付二次签名参数转为规范化支付指令。
func newAppInstruction(scene enums.PaymentScene, params *wechat.AppPayParams) *Instruction {
	return &Instruction{
		Scene:  scene,
		Action: enums.PaymentInstructionInvokeParams,
		Params: map[string]string{
			"appid":     params.Appid,
			"partnerid": params.Partnerid,
			"prepayid":  params.Prepayid,
			"package":   params.Package,
			"noncestr":  params.Noncestr,
			"timestamp": params.Timestamp,
			"sign":      params.Sign,
		},
	}
}

// newJsapiInstruction 将微信 JSAPI/公众号或小程序支付二次签名参数转为规范化支付指令。
func newJsapiInstruction(scene enums.PaymentScene, params *wechat.JSAPIPayParams) *Instruction {
	return &Instruction{
		Scene:  scene,
		Action: enums.PaymentInstructionInvokeParams,
		Params: map[string]string{
			"appId":     params.AppId,
			"timeStamp": params.TimeStamp,
			"nonceStr":  params.NonceStr,
			"package":   params.Package,
			"signType":  params.SignType,
			"paySign":   params.PaySign,
		},
	}
}

// mapWechatQueryOrderResponse 把微信订单查询响应映射为规范化的支付结果。
func mapWechatQueryOrderResponse(order *wechat.QueryOrder) *PaymentResult {
	if order == nil {
		return nil
	}

	result := &PaymentResult{
		OutTradeNo:     order.OutTradeNo,
		ChannelTradeNo: order.TransactionId,
		Status:         mapWechatTradeState(order.TradeState),
		ChannelState:   order.TradeState,
	}
	if order.Amount != nil {
		result.PaidAmount = int64(order.Amount.Total)
	}
	if order.Payer != nil {
		result.PayerID = order.Payer.Openid
	}
	if result.Status == enums.PaymentStatusSucceeded {
		result.PaidAt = optionalChannelTime(order.SuccessTime)
	}

	return result
}

// mapWechatRefundResponse 把微信退款响应（申请或查询）映射为规范化的退款结果。
func mapWechatRefundResponse(resp *wechat.RefundOrderResponse) *RefundResult {
	if resp == nil {
		return nil
	}

	result := &RefundResult{
		OutRefundNo:     resp.OutRefundNo,
		OutTradeNo:      resp.OutTradeNo,
		ChannelRefundNo: resp.RefundId,
		Status:          mapWechatRefundStatus(resp.Status),
		ChannelState:    resp.Status,
	}
	if resp.Amount != nil {
		result.RefundAmount = int64(resp.Amount.Refund)
	}
	if result.Status.Succeeded() {
		result.RefundedAt = optionalChannelTime(resp.SuccessTime)
	}

	return result
}

// mapWechatDecryptPayResult 把微信支付通知解密明文映射为规范化的支付结果。
func mapWechatDecryptPayResult(decrypted *wechat.V3DecryptPayResult) *PaymentResult {
	if decrypted == nil {
		return nil
	}

	result := &PaymentResult{
		OutTradeNo:     decrypted.OutTradeNo,
		ChannelTradeNo: decrypted.TransactionId,
		Status:         mapWechatTradeState(decrypted.TradeState),
		ChannelState:   decrypted.TradeState,
	}
	if decrypted.Amount != nil {
		result.PaidAmount = int64(decrypted.Amount.Total)
	}
	if decrypted.Payer != nil {
		result.PayerID = decrypted.Payer.Openid
	}
	if result.Status == enums.PaymentStatusSucceeded {
		result.PaidAt = optionalChannelTime(decrypted.SuccessTime)
	}

	return result
}

// mapWechatDecryptRefundResult 把微信退款通知解密明文映射为规范化的退款结果。
func mapWechatDecryptRefundResult(decrypted *wechat.V3DecryptRefundResult) *RefundResult {
	if decrypted == nil {
		return nil
	}

	result := &RefundResult{
		OutRefundNo:     decrypted.OutRefundNo,
		OutTradeNo:      decrypted.OutTradeNo,
		ChannelRefundNo: decrypted.RefundId,
		Status:          mapWechatRefundStatus(decrypted.RefundStatus),
		ChannelState:    decrypted.RefundStatus,
	}
	if decrypted.Amount != nil {
		result.RefundAmount = int64(decrypted.Amount.Refund)
	}
	if result.Status.Succeeded() {
		result.RefundedAt = optionalChannelTime(decrypted.SuccessTime)
	}

	return result
}

// mapWechatTradeState 把微信交易状态映射为本地支付状态。
// REFUND 表示该笔收款已转入退款，其支付本身是成功的，因此同样归入成功态。
func mapWechatTradeState(state string) enums.PaymentStatus {
	switch state {
	case wechat.TradeStateSuccess, wechat.TradeStateRefund:
		return enums.PaymentStatusSucceeded
	case wechat.TradeStateNoPay, wechat.TradeStatePaying:
		return enums.PaymentStatusPending
	case wechat.TradeStateClosed, wechat.TradeStateRevoked:
		return enums.PaymentStatusClosed
	case wechat.TradeStatePayError:
		return enums.PaymentStatusFailed
	default:
		return enums.PaymentStatusUnknown
	}
}

// mapWechatRefundStatus 把微信退款状态映射为本地退款状态。
func mapWechatRefundStatus(state string) enums.PaymentRefundStatus {
	switch state {
	case wechatRefundStatusSuccess:
		return enums.PaymentRefundStatusSucceeded
	case wechatRefundStatusProcessing:
		return enums.PaymentRefundStatusPending
	case wechatRefundStatusClosed:
		return enums.PaymentRefundStatusCanceled
	case wechatRefundStatusAbnormal:
		return enums.PaymentRefundStatusFailed
	default:
		return enums.PaymentRefundStatusUnknown
	}
}
