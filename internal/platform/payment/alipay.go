package payment

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-pay/gopay"
	alipaynotify "github.com/go-pay/gopay/alipay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/payment/enums"
)

// 支付宝交易状态常量，取值来自支付宝开放平台交易状态协议。
const (
	alipayTradeStatusWaitBuyerPay = "WAIT_BUYER_PAY"
	alipayTradeStatusSuccess      = "TRADE_SUCCESS"
	alipayTradeStatusFinished     = "TRADE_FINISHED"
	alipayTradeStatusClosed       = "TRADE_CLOSED"
)

// 支付宝退款状态与资金变动常量。
const (
	alipayRefundStatusSuccess = "REFUND_SUCCESS"
	alipayFundChangeYes       = "Y"
)

// alipayProvider 实现绑定到单个支付宝应用的 Provider。
//
// 支付宝 v3 客户端仅提供证书模式，且 SDK 在支付宝公钥缺失时会静默通过响应验签，
// 因此三本证书在构造期必须全部加载成功，否则构造直接失败。
type alipayProvider struct {
	appKey            string
	appID             string
	notifyURL         string
	returnURL         string
	scenes            map[enums.PaymentScene]struct{}
	client            *alipayv3.ClientV3
	publicCertContent []byte
}

var _ FullProvider = (*alipayProvider)(nil)

// newAlipayProvider 依据渠道应用配置构造支付宝 Provider。
// isProd 由运行环境推导，沙箱与生产各用一套应用凭据，不在同一份配置内并存。
func newAlipayProvider(
	appKey string,
	cfg config.AlipayPaymentAppConfig,
	isProd bool,
	certsDir string,
	logger *zap.Logger,
) (*alipayProvider, error) {
	scenes, err := resolveScenes(cfg.Scenes, enums.PaymentChannelAlipay)
	if err != nil {
		return nil, err
	}

	if err := requireAlipayCredentials(appKey, cfg); err != nil {
		return nil, err
	}

	privateKey, err := readCredentialFile(certsDir, cfg.PrivateKeyPath)
	if err != nil {
		return nil, err
	}

	appCert, err := readCredentialFile(certsDir, cfg.AppCertPath)
	if err != nil {
		return nil, err
	}
	publicCert, err := readCredentialFile(certsDir, cfg.PublicCertPath)
	if err != nil {
		return nil, err
	}
	rootCert, err := readCredentialFile(certsDir, cfg.RootCertPath)
	if err != nil {
		return nil, err
	}

	client, err := alipayv3.NewClientV3(cfg.AppID, string(privateKey), isProd)
	if err != nil {
		return nil, errs.Wrap(
			errs.CodeServiceUnavailable,
			fmt.Sprintf("failed to construct alipay client for %s: %v", appKey, err),
			err,
		)
	}

	// 证书加载失败的客户端会静默跳过响应验签，因此这里必须让构造直接失败。
	if err := client.SetCert(appCert, rootCert, publicCert); err != nil {
		return nil, errs.Wrap(
			errs.CodeServiceUnavailable,
			fmt.Sprintf("failed to load alipay certificates for %s: %v", appKey, err),
			err,
		)
	}
	client.SetLogger(newXLogAdapter(logger, !isProd))

	if strings.TrimSpace(client.AliPayPublicCertSN) == "" {
		return nil, errs.ServiceUnavailable(
			"alipay public certificate serial number is empty after loading for " + appKey,
		)
	}

	return &alipayProvider{
		appKey:            appKey,
		appID:             strings.TrimSpace(cfg.AppID),
		notifyURL:         strings.TrimSpace(cfg.NotifyURL),
		returnURL:         strings.TrimSpace(cfg.ReturnURL),
		scenes:            scenes,
		client:            client,
		publicCertContent: publicCert,
	}, nil
}

// requireAlipayCredentials 校验支付宝应用的必填凭据。
func requireAlipayCredentials(appKey string, cfg config.AlipayPaymentAppConfig) error {
	return requireCredentials("alipay", appKey, []credentialField{
		{Name: "app_id", Value: cfg.AppID},
		{Name: "private_key_path", Value: cfg.PrivateKeyPath},
		{Name: "app_cert_path", Value: cfg.AppCertPath},
		{Name: "public_cert_path", Value: cfg.PublicCertPath},
		{Name: "root_cert_path", Value: cfg.RootCertPath},
		{Name: "notify_url", Value: cfg.NotifyURL},
	})
}

// Channel 返回支付宝渠道。
func (p *alipayProvider) Channel() enums.PaymentChannel { return enums.PaymentChannelAlipay }

// Application 返回该 Provider 绑定的应用标识。
func (p *alipayProvider) Application() string { return p.appKey }

// Scenes 返回该应用声明可承载的支付场景。
func (p *alipayProvider) Scenes() []enums.PaymentScene { return sortedScenes(p.scenes) }

// HealthCheck 检查客户端与证书是否就绪。
func (p *alipayProvider) HealthCheck() error {
	if p.client == nil {
		return errs.ServiceUnavailable("alipay client is not initialized for " + p.appKey)
	}
	if len(p.publicCertContent) == 0 || strings.TrimSpace(p.client.AliPayPublicCertSN) == "" {
		return errs.ServiceUnavailable("alipay certificates are not loaded for " + p.appKey)
	}

	return nil
}

// Create 发起一次支付宝收款并返回支付指令。
func (p *alipayProvider) Create(ctx context.Context, req CreateRequest) (*Instruction, error) {
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
	case enums.PaymentSceneAlipayPage:
		return p.createRedirectInstruction(ctx, req, body, true)
	case enums.PaymentSceneAlipayWap:
		return p.createRedirectInstruction(ctx, req, body, false)
	case enums.PaymentSceneAlipayNative:
		return p.createPrecreateInstruction(ctx, req, body)
	case enums.PaymentSceneAlipayApp:
		return p.createAppInstruction(ctx, req, body)
	case enums.PaymentSceneAlipayMiniProgram:
		return p.createMiniProgramInstruction(ctx, req, body)
	default:
		return nil, errs.BadRequest("unsupported alipay payment scene " + req.Scene.String())
	}
}

// createRedirectInstruction 调用电脑网站或手机网站下单接口并返回收银台跳转地址。
func (p *alipayProvider) createRedirectInstruction(
	ctx context.Context,
	req CreateRequest,
	body gopay.BodyMap,
	desktop bool,
) (*Instruction, error) {
	const operation = "create alipay redirect payment"

	var payURL string
	var err error
	if desktop {
		payURL, err = p.client.TradePagePay(ctx, body)
	} else {
		payURL, err = p.client.TradeWapPay(ctx, body)
	}
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if strings.TrimSpace(payURL) == "" {
		return nil, errs.ServiceUnavailable("alipay returned an empty cashier url for " + p.appKey)
	}

	return &Instruction{
		Scene:  req.Scene,
		Action: enums.PaymentInstructionRedirectURL,
		Value:  payURL,
	}, nil
}

// createPrecreateInstruction 调用当面付下单接口并返回二维码链接。
func (p *alipayProvider) createPrecreateInstruction(
	ctx context.Context,
	req CreateRequest,
	body gopay.BodyMap,
) (*Instruction, error) {
	const operation = "create alipay precreate payment"

	response, err := p.client.TradePrecreate(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.StatusCode, response.ErrResponse, operation); err != nil {
		return nil, err
	}

	return &Instruction{
		Scene:  req.Scene,
		Action: enums.PaymentInstructionQRCode,
		Value:  response.QrCode,
	}, nil
}

// createAppInstruction 调用 APP 下单接口并返回客户端调起参数。
func (p *alipayProvider) createAppInstruction(
	ctx context.Context,
	req CreateRequest,
	body gopay.BodyMap,
) (*Instruction, error) {
	const operation = "create alipay app payment"

	orderStr, err := p.client.TradeAppPay(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if strings.TrimSpace(orderStr) == "" {
		return nil, errs.ServiceUnavailable("alipay returned empty app order string for " + p.appKey)
	}

	return &Instruction{
		Scene:  req.Scene,
		Action: enums.PaymentInstructionInvokeParams,
		Params: map[string]string{"order_str": orderStr},
	}, nil
}

// createMiniProgramInstruction 调用小程序下单接口并返回小程序调起参数。
func (p *alipayProvider) createMiniProgramInstruction(
	ctx context.Context,
	req CreateRequest,
	body gopay.BodyMap,
) (*Instruction, error) {
	const operation = "create alipay miniprogram payment"

	response, err := p.client.TradeCreate(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.StatusCode, response.ErrResponse, operation); err != nil {
		return nil, err
	}
	if strings.TrimSpace(response.TradeNo) == "" {
		return nil, errs.ServiceUnavailable("alipay returned empty trade no for " + p.appKey)
	}

	return &Instruction{
		Scene:  req.Scene,
		Action: enums.PaymentInstructionInvokeParams,
		Params: map[string]string{"trade_no": response.TradeNo},
	}, nil
}

// createBodyMap 构造支付宝下单接口的公共业务参数。
func (p *alipayProvider) createBodyMap(req CreateRequest) gopay.BodyMap {
	body := make(gopay.BodyMap).
		Set("out_trade_no", req.OutTradeNo).
		Set("total_amount", FormatYuanAmount(req.EffectiveAmount())).
		Set("subject", req.Subject)

	if notifyURL := resolveNotifyURL(req.NotifyURL, p.notifyURL); notifyURL != "" {
		body.Set("notify_url", notifyURL)
	}
	if p.returnURL != "" {
		body.Set("return_url", p.returnURL)
	}
	if req.Description != "" {
		body.Set("body", req.Description)
	}
	if req.PayerID != "" {
		body.Set("buyer_id", req.PayerID)
	}
	if timeout := formatAlipayTimeout(req.ExpireAt); timeout != "" {
		body.Set("timeout_express", timeout)
	}

	return body
}

// Query 主动查询支付宝交易状态。
func (p *alipayProvider) Query(ctx context.Context, req QueryRequest) (*PaymentResult, error) {
	const operation = "query alipay payment"

	if strings.TrimSpace(req.OutTradeNo) == "" {
		return nil, errs.BadRequest("out trade no is required")
	}

	response, err := p.client.TradeQuery(ctx, make(gopay.BodyMap).Set("out_trade_no", req.OutTradeNo))
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.StatusCode, response.ErrResponse, operation); err != nil {
		return nil, err
	}

	return mapAlipayTradeResponse(response)
}

// mapAlipayTradeResponse 把支付宝交易查询结果映射为规范化的支付结果。
func mapAlipayTradeResponse(response *alipayv3.TradeQueryRsp) (*PaymentResult, error) {
	paidAmount, err := ParseYuanAmount(response.TotalAmount)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to parse alipay total amount (%s): %w", response.TotalAmount, err))
	}

	result := &PaymentResult{
		OutTradeNo:     response.OutTradeNo,
		ChannelTradeNo: response.TradeNo,
		Status:         mapAlipayTradeStatus(response.TradeStatus),
		ChannelState:   response.TradeStatus,
		PaidAmount:     paidAmount,
	}
	if result.Status == enums.PaymentStatusSucceeded {
		result.PaidAt = optionalChannelTime(response.SendPayDate)
	}

	return result, nil
}

// Close 关闭一笔未支付的支付宝交易。
func (p *alipayProvider) Close(ctx context.Context, req CloseRequest) error {
	const operation = "close alipay payment"

	if strings.TrimSpace(req.OutTradeNo) == "" {
		return errs.BadRequest("out trade no is required")
	}

	response, err := p.client.TradeClose(ctx, make(gopay.BodyMap).Set("out_trade_no", req.OutTradeNo))
	if err != nil {
		return p.upstreamError(operation, err)
	}

	return p.checkResponse(response.StatusCode, response.ErrResponse, operation)
}

// Refund 发起支付宝退款。
func (p *alipayProvider) Refund(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	const operation = "create alipay refund"

	if err := validateRefund(req); err != nil {
		return nil, err
	}

	body := make(gopay.BodyMap).
		Set("out_trade_no", req.OutTradeNo).
		Set("out_request_no", req.OutRefundNo).
		Set("refund_amount", FormatYuanAmount(req.EffectiveRefundAmount()))

	if req.Reason != "" {
		body.Set("refund_reason", req.Reason)
	}

	response, err := p.client.TradeRefund(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.StatusCode, response.ErrResponse, operation); err != nil {
		return nil, err
	}

	refundAmount, err := ParseYuanAmount(response.RefundFee)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to parse alipay refund fee (%s): %w", response.RefundFee, err))
	}

	// 支付宝退款为同步返回，fund_change 为 Y 表示资金已变动；否则仍在处理中。
	// 响应中的 trade_no 是原支付的支付宝交易号而非退款流水号，支付宝不暴露后者，故不填入 ChannelRefundNo。
	result := &RefundResult{
		OutRefundNo:  req.OutRefundNo,
		OutTradeNo:   response.OutTradeNo,
		ChannelState: response.FundChange,
		RefundAmount: refundAmount,
		Status:       mapAlipayFundChange(response.FundChange),
	}
	if result.Status.Succeeded() {
		refundedAt := time.Now().UTC()
		result.RefundedAt = &refundedAt
	}

	return result, nil
}

// QueryRefund 主动查询支付宝退款状态。
func (p *alipayProvider) QueryRefund(ctx context.Context, req QueryRefundRequest) (*RefundResult, error) {
	const operation = "query alipay refund"

	if strings.TrimSpace(req.OutTradeNo) == "" {
		return nil, errs.BadRequest("out trade no is required")
	}
	if strings.TrimSpace(req.OutRefundNo) == "" {
		return nil, errs.BadRequest("out refund no is required")
	}

	body := make(gopay.BodyMap).
		Set("out_trade_no", req.OutTradeNo).
		Set("out_request_no", req.OutRefundNo)

	response, err := p.client.TradeFastPayRefundQuery(ctx, body)
	if err != nil {
		return nil, p.upstreamError(operation, err)
	}
	if err := p.checkResponse(response.StatusCode, response.ErrResponse, operation); err != nil {
		return nil, err
	}

	refundAmount, err := ParseYuanAmount(response.RefundAmount)
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to parse alipay refund amount (%s): %w", response.RefundAmount, err))
	}

	outRefundNo := req.OutRefundNo
	if trimmed := strings.TrimSpace(response.OutRequestNo); trimmed != "" {
		outRefundNo = trimmed
	}

	result := &RefundResult{
		OutRefundNo:  outRefundNo,
		OutTradeNo:   response.OutTradeNo,
		Status:       mapAlipayRefundStatus(response.RefundStatus),
		ChannelState: response.RefundStatus,
		RefundAmount: refundAmount,
	}
	if result.Status.Succeeded() {
		result.RefundedAt = optionalChannelTime(response.GmtRefundPay)
	}

	return result, nil
}

// ParseNotification 解析并验签支付宝异步通知。
//
// 支付宝的异步通知验签能力只存在于该库的非 v3 旧包中（v3 包未导出任何验签函数），
// 因此这里必须同时引入旧包完成解析与验签，否则会漏掉渠道侧的伪造通知。
func (p *alipayProvider) ParseNotification(req *http.Request) (*Notification, error) {
	if req == nil {
		return nil, errs.BadRequest("alipay notification request is required")
	}

	body, err := alipaynotify.ParseNotifyToBodyMap(req)
	if err != nil {
		return nil, errs.BadRequest("failed to parse alipay notification for " + p.appKey)
	}

	verified, err := alipaynotify.VerifySignWithCert(p.publicCertContent, body)
	if err != nil || !verified {
		return nil, errs.Unauthorized(errs.CodeAuthTokenInvalid, "invalid alipay notification signature for "+p.appKey)
	}

	// 验签只证明报文来自支付宝，不证明报文属于本应用：报文携带 app_id 时必须与本应用一致，
	// 防止同一支付宝账号下的多个应用之间发生通知串号。
	if appID := body.GetString("app_id"); appID != "" && appID != p.appID {
		return nil, errs.Forbidden("alipay notification app_id mismatch for " + p.appKey)
	}

	notification := &Notification{
		ID:         body.GetString("notify_id"),
		OccurredAt: channelTimeOrNow(body.GetString("notify_time")),
		Payload:    []byte(body.JsonBody()),
	}

	// 支付宝的支付与退款通知共用同一 notify_url，凭报文中是否携带退款金额区分。
	if body.GetString("refund_fee") != "" {
		return p.attachRefundNotification(body, notification)
	}

	return p.attachPaymentNotification(body, notification)
}

// attachPaymentNotification 把支付宝支付通知映射为规范化结果。
func (p *alipayProvider) attachPaymentNotification(
	body gopay.BodyMap,
	notification *Notification,
) (*Notification, error) {
	paidAmount, err := ParseYuanAmount(body.GetString("total_amount"))
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to parse alipay notification amount: %w", err))
	}

	status := mapAlipayTradeStatus(body.GetString("trade_status"))
	event := body.GetString("trade_status")

	notification.Kind = enums.PaymentNotificationKindPayment
	notification.Event = event
	notification.Payment = &PaymentResult{
		OutTradeNo:     body.GetString("out_trade_no"),
		ChannelTradeNo: body.GetString("trade_no"),
		Status:         status,
		ChannelState:   event,
		PaidAmount:     paidAmount,
		PayerID:        body.GetString("buyer_id"),
	}
	if status == enums.PaymentStatusSucceeded {
		notification.Payment.PaidAt = optionalChannelTime(body.GetString("gmt_payment"))
	}

	return notification, nil
}

// attachRefundNotification 把支付宝退款通知映射为规范化结果。
func (p *alipayProvider) attachRefundNotification(
	body gopay.BodyMap,
	notification *Notification,
) (*Notification, error) {
	refundAmount, err := ParseYuanAmount(body.GetString("refund_fee"))
	if err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to parse alipay refund notification amount: %w", err))
	}

	event := body.GetString("refund_status")
	status := mapAlipayRefundStatus(event)
	if status == enums.PaymentRefundStatusUnknown {
		// 退款通知不带 refund_status 时，出现 refund_fee 即代表退款已完成。
		status = enums.PaymentRefundStatusSucceeded
		event = alipayRefundStatusSuccess
	}

	notification.Kind = enums.PaymentNotificationKindRefund
	notification.Event = event
	notification.Refund = &RefundResult{
		OutRefundNo:  body.GetString("out_biz_no"),
		OutTradeNo:   body.GetString("out_trade_no"),
		Status:       status,
		ChannelState: event,
		RefundAmount: refundAmount,
	}
	if status.Succeeded() {
		notification.Refund.RefundedAt = optionalChannelTime(body.GetString("gmt_refund"))
	}

	return notification, nil
}

// upstreamError 包装支付宝调用失败，统一挂载上游不可用错误码。
func (p *alipayProvider) upstreamError(operation string, err error) error {
	return upstreamUnavailable("alipay", p.appKey, operation, err)
}

// checkResponse 判定支付宝响应是否为成功。
// gopay 仅在 HTTP 状态为 200 时填充业务字段，非 200 时把错误码写入 ErrResponse 且不返回 err，
// 因此必须显式检查状态码。
func (p *alipayProvider) checkResponse(statusCode int, errResponse alipayv3.ErrResponse, operation string) error {
	if statusCode == http.StatusOK {
		return nil
	}

	cause := fmt.Errorf(
		"alipay responded with status code %d: %s %s",
		statusCode, errResponse.Code, truncateForLog(errResponse.Message),
	)
	return upstreamUnavailable("alipay", p.appKey, operation, cause)
}

// formatAlipayTimeout 把过期时刻折算为支付宝要求的相对超时表达式（如 30m）。
func formatAlipayTimeout(expireAt time.Time) string {
	if expireAt.IsZero() {
		return ""
	}

	remaining := time.Until(expireAt)
	if remaining <= 0 {
		return ""
	}

	minutes := max(int(math.Ceil(remaining.Minutes())), 1)

	return fmt.Sprintf("%dm", minutes)
}

// mapAlipayTradeStatus 把支付宝交易状态映射为本地支付状态。
// 注意：支付宝在收款完成后发生全额退款同样会返回 TRADE_CLOSED，
// 该情形下本地必须以退款记录为准，不能据本映射反向推断「未支付」。
func mapAlipayTradeStatus(status string) enums.PaymentStatus {
	switch status {
	case alipayTradeStatusSuccess, alipayTradeStatusFinished:
		return enums.PaymentStatusSucceeded
	case alipayTradeStatusWaitBuyerPay:
		return enums.PaymentStatusPending
	case alipayTradeStatusClosed:
		return enums.PaymentStatusClosed
	default:
		return enums.PaymentStatusUnknown
	}
}

// mapAlipayFundChange 把支付宝同步退款返回的资金变动标记映射为本地退款状态。
func mapAlipayFundChange(fundChange string) enums.PaymentRefundStatus {
	if fundChange == alipayFundChangeYes {
		return enums.PaymentRefundStatusSucceeded
	}

	return enums.PaymentRefundStatusPending
}

// mapAlipayRefundStatus 把支付宝退款查询状态映射为本地退款状态。
func mapAlipayRefundStatus(status string) enums.PaymentRefundStatus {
	switch status {
	case alipayRefundStatusSuccess:
		return enums.PaymentRefundStatusSucceeded
	case "":
		return enums.PaymentRefundStatusPending
	default:
		return enums.PaymentRefundStatusUnknown
	}
}
