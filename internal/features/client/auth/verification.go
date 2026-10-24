package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/uptrace/bun"
	"go.uber.org/zap"

	"bgin/internal/config"
	"bgin/internal/database/models"
	"bgin/internal/platform/cache"
	"bgin/internal/platform/errs"
	"bgin/internal/platform/logger"
)

// 一次性凭据的底层存储用途前缀（写入 verification.identifier），使同一邮箱的不同用途在库中互不干扰。
// 业务层对应的宏观语义契约见 internal/shared/enums.OTPType。
const (
	purposeEmailVerify = "email-verify"
	purposeResetLink   = "password-reset-link"
	purposeResetOTP    = "password-reset-otp"
)

// 验证码规格与失败尝试计数。计数键由 cache.Key 构造（"auth" / "otp-attempts" / 标识）。
const (
	otpSpace = 1000000
	// otpAttemptsTTL 必须不短于任何一种凭据的寿命，否则计数会先于凭据失效。
	otpAttemptsTTL = time.Hour
)

// verifier 负责一次性凭据的签发与核销。
// 明文只出现在邮件里，库里存的是 HMAC-SHA256 摘要，因此拿到库也还原不出可用凭据。
type verifier struct {
	db          bun.IDB
	cache       cache.Store
	key         []byte
	maxAttempts int
}

// newVerifier 构造 verifier，密钥由 JWT 密钥派生并做用途域分离。
func newVerifier(db bun.IDB, cacheClient cache.Store, cfg *config.Config) *verifier {
	return &verifier{
		db:          db,
		cache:       cacheClient,
		key:         deriveKey(cfg.JWT.Secret, "verification"),
		maxAttempts: cfg.Auth.OTPMaxAttempts,
	}
}

// issueCode 为用户邮箱签发一枚 6 位数字验证码，返回明文。
func (v *verifier) issueCode(ctx context.Context, purpose, email string, ttl time.Duration) (string, error) {
	code, err := randomCode()
	if err != nil {
		return "", err
	}
	if err := v.store(ctx, purpose, email, code, ttl); err != nil {
		return "", err
	}
	return code, nil
}

// issueToken 为用户邮箱签发一枚 URL 安全的链接令牌，返回明文。
func (v *verifier) issueToken(ctx context.Context, purpose, email string, ttl time.Duration) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := v.store(ctx, purpose, email, token, ttl); err != nil {
		return "", err
	}
	return token, nil
}

// store 写入凭据摘要。
func (v *verifier) store(ctx context.Context, purpose, email, code string, ttl time.Duration) error {
	identifier := verificationIdentifier(purpose, email)
	expiresAt := time.Now().UTC().Add(ttl)
	if err := models.SaveVerification(ctx, v.db, identifier, v.digest(identifier, code), expiresAt); err != nil {
		return errs.Internal(fmt.Errorf("save verification failed: %w", err))
	}
	return nil
}

var (
	errOTPExpired = errs.New(errs.CodeAuthOTPExpired, "The verification code has expired")
	errOTPInvalid = errs.New(errs.CodeAuthOTPInvalid, "The verification code is incorrect")
)

// consume 核销一枚凭据。区分"已过期"与"不正确"，并累计失败次数以防暴力破解。
func (v *verifier) consume(ctx context.Context, purpose, email, code string) error {
	identifier := verificationIdentifier(purpose, email)

	if err := v.checkAttempts(ctx, identifier); err != nil {
		return err
	}

	record, err := models.FindVerification(ctx, v.db, identifier)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errOTPExpired
		}
		return errs.Internal(err)
	}

	if !time.Now().UTC().Before(record.ExpiresAt) {
		if err := models.DeleteVerification(ctx, v.db, record.ID); err != nil {
			logger.FromContext(ctx).Warn("failed to clean up expired credential", zap.Error(err))
		}
		return errOTPExpired
	}

	if subtle.ConstantTimeCompare([]byte(record.Value), []byte(v.digest(identifier, code))) != 1 {
		return errOTPInvalid
	}

	// 核销是原子的：WHERE 里带上刚读到的摘要，只有真正删掉那一行的调用算成功。
	// 并发提交同一枚凭据时另一路会拿到 0 行，按"已失效"处理而不是也判成功。
	consumed, err := models.ConsumeVerification(ctx, v.db, identifier, record.Value)
	if err != nil {
		return errs.Internal(err)
	}
	if !consumed {
		return errOTPExpired
	}

	if _, err := v.cache.Delete(ctx, cache.Key("auth", "otp-attempts", identifier)); err != nil {
		logger.FromContext(ctx).Warn("failed to clear verification attempts", zap.Error(err))
	}
	return nil
}

// checkAttempts 在核销前累计失败次数，超限直接拒绝，避免把凭据当密码来枚举。
func (v *verifier) checkAttempts(ctx context.Context, identifier string) error {
	attempts, err := v.cache.Increment(ctx, cache.Key("auth", "otp-attempts", identifier), otpAttemptsTTL)
	if err != nil {
		// 计数不可用时放行：凭据本身仍有 HMAC 与过期时间保护。
		logger.FromContext(ctx).Error("failed to record verification attempts, request allowed", zap.Error(err))
		return nil
	}
	if attempts > int64(v.maxAttempts) {
		return errs.New(errs.CodeAuthOTPTooManyAttempts, "Too many failed attempts, please request a new code")
	}
	return nil
}

// digest 计算凭据摘要：identifier 参与运算，避免同一验证码被跨用途重放。
func (v *verifier) digest(identifier, code string) string {
	mac := hmac.New(sha256.New, v.key)
	mac.Write([]byte(identifier))
	mac.Write([]byte{0})
	mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil))
}

// verificationIdentifier 拼出凭据在库中的主体标识。
func verificationIdentifier(purpose, email string) string {
	return purpose + ":" + strings.ToLower(strings.TrimSpace(email))
}

// deriveKey 从主密钥派生用途专属密钥，避免不同场景共用同一把密钥。
func deriveKey(secret, purpose string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(purpose))
	return mac.Sum(nil)
}

// randomCode 生成 6 位数字验证码，固定宽度便于用户抄录。
func randomCode() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(otpSpace))
	if err != nil {
		return "", errs.Internal(fmt.Errorf("generate verification code failed: %w", err))
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

// randomToken 生成 32 字节的 URL 安全令牌。
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", errs.Internal(fmt.Errorf("generate token failed: %w", err))
	}
	return hex.EncodeToString(buf), nil
}
