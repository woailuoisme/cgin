// Package money 提供高精度不可变金融货币值对象（Value Object）与金融计算基础设施。
// 本文件定义 Money 核心值对象、构造器、比较、加减乘除四则运算、税费折扣计算、收单网关适配及 JSON/SQL 持久化序列化。
package money

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/shopspring/decimal"
)

// Money 是不可变高精度货币值对象（Value Object），封装代数运算、币种防错、舍入与分摊。
type Money struct {
	amount   decimal.Decimal
	currency Currency
}

// New 使用 Decimal 金额和 ISO 货币代码构建 Money 实例；币种未注册时返回 ErrUnknownCurrency。
func New(amount decimal.Decimal, currencyCode string) (Money, error) {
	curr, err := ResolveCurrency(currencyCode)
	if err != nil {
		return Money{}, err
	}
	return Money{
		amount:   amount,
		currency: curr,
	}, nil
}

// MustNew 构建 Money 实例；币种未注册或代码为空时 panic。
func MustNew(amount decimal.Decimal, currencyCode string) Money {
	m, err := New(amount, currencyCode)
	if err != nil {
		panic(err)
	}
	return m
}

// NewFromString 从字符串解析金额并构建 Money。
func NewFromString(amount string, currencyCode string) (Money, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(amount))
	if err != nil {
		return Money{}, fmt.Errorf("%w: %s", ErrInvalidAmount, err.Error())
	}
	return New(d, currencyCode)
}

// NewFromFloat 从 float64 构建 Money。
func NewFromFloat(amount float64, currencyCode string) (Money, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return Money{}, ErrInvalidAmount
	}
	return New(decimal.NewFromFloat(amount), currencyCode)
}

// NewFromInt 从整型金额构建 Money。
func NewFromInt(amount int64, currencyCode string) (Money, error) {
	return New(decimal.NewFromInt(amount), currencyCode)
}

// NewFromCents 从分币（次要单位整数）构建 Money（如 12345 CNY -> 123.45 CNY）。
func NewFromCents(cents int64, currencyCode string) (Money, error) {
	curr, err := ResolveCurrency(currencyCode)
	if err != nil {
		return Money{}, err
	}
	factor := decimal.NewFromInt(int64(math.Pow10(int(curr.Fraction))))
	d := decimal.NewFromInt(cents).Div(factor)
	return Money{amount: d, currency: curr}, nil
}

// Zero 返回指定货币的零值 Money。
func Zero(currencyCode string) Money {
	return MustNew(decimal.Zero, currencyCode)
}

// Amount 返回内部存储的原始高精度 decimal.Decimal 金额。
func (m Money) Amount() decimal.Decimal {
	return m.amount
}

// Currency 返回货币元数据。
func (m Money) Currency() Currency {
	return m.currency
}

// CurrencyCode 返回 ISO 货币代码字符串（如 "CNY"）。
func (m Money) CurrencyCode() string {
	return m.currency.Code
}

// IsZero 判定金额是否为零。
func (m Money) IsZero() bool {
	return m.amount.IsZero()
}

// IsPositive 判定金额是否大于零。
func (m Money) IsPositive() bool {
	return m.amount.IsPositive()
}

// IsNegative 判定金额是否小于零。
func (m Money) IsNegative() bool {
	return m.amount.IsNegative()
}

// Equal 比较两个 Money 的币种与金额是否绝对一致。
func (m Money) Equal(other Money) bool {
	return m.currency.Code == other.currency.Code && m.amount.Equal(other.amount)
}

// Compare 比较两个同币种 Money 的大小，返回 -1 (小于)、0 (等于)、1 (大于)。
func (m Money) Compare(other Money) (int, error) {
	if m.currency.Code != other.currency.Code {
		return 0, ErrCurrencyMismatch
	}
	return m.amount.Cmp(other.amount), nil
}

// GreaterThan 检查 m 是否大于 other。
func (m Money) GreaterThan(other Money) (bool, error) {
	cmp, err := m.Compare(other)
	if err != nil {
		return false, err
	}
	return cmp > 0, nil
}

// GreaterThanOrEqual 检查 m 是否大于等于 other。
func (m Money) GreaterThanOrEqual(other Money) (bool, error) {
	cmp, err := m.Compare(other)
	if err != nil {
		return false, err
	}
	return cmp >= 0, nil
}

// LessThan 检查 m 是否小于 other。
func (m Money) LessThan(other Money) (bool, error) {
	cmp, err := m.Compare(other)
	if err != nil {
		return false, err
	}
	return cmp < 0, nil
}

// LessThanOrEqual 检查 m 是否小于等于 other。
func (m Money) LessThanOrEqual(other Money) (bool, error) {
	cmp, err := m.Compare(other)
	if err != nil {
		return false, err
	}
	return cmp <= 0, nil
}

// Add 执行同币种相加并返回新的 Money。不同币种直接返回 ErrCurrencyMismatch。
func (m Money) Add(other Money) (Money, error) {
	if m.currency.Code != other.currency.Code {
		return Money{}, ErrCurrencyMismatch
	}
	return Money{
		amount:   m.amount.Add(other.amount),
		currency: m.currency,
	}, nil
}

// MustAdd 执行同币种相加；若币种冲突则直接 panic，用于已知同币种的声明式管道计算。
func (m Money) MustAdd(other Money) Money {
	res, err := m.Add(other)
	if err != nil {
		panic(err)
	}
	return res
}

// Sub 执行同币种相减并返回新的 Money。不同币种直接返回 ErrCurrencyMismatch。
func (m Money) Sub(other Money) (Money, error) {
	if m.currency.Code != other.currency.Code {
		return Money{}, ErrCurrencyMismatch
	}
	return Money{
		amount:   m.amount.Sub(other.amount),
		currency: m.currency,
	}, nil
}

// MustSub 执行同币种相减；若币种冲突则直接 panic。
func (m Money) MustSub(other Money) Money {
	res, err := m.Sub(other)
	if err != nil {
		panic(err)
	}
	return res
}

// Mul 将金额乘以 Decimal 乘数（如数量、税率系数），保留高精度中间态。
func (m Money) Mul(factor decimal.Decimal) Money {
	return Money{
		amount:   m.amount.Mul(factor),
		currency: m.currency,
	}
}

// MulInt 将金额乘以整数乘数。
func (m Money) MulInt(factor int64) Money {
	return m.Mul(decimal.NewFromInt(factor))
}

// MulFloat64 将金额乘以 float64 乘数。
func (m Money) MulFloat64(factor float64) Money {
	return m.Mul(decimal.NewFromFloat(factor))
}

// Div 将金额除以 Decimal 除数，保留高精度中间态。
func (m Money) Div(divisor decimal.Decimal) (Money, error) {
	if divisor.IsZero() {
		return Money{}, ErrDivisionByZero
	}
	return Money{
		amount:   m.amount.Div(divisor),
		currency: m.currency,
	}, nil
}

// MustDiv 将金额除以 Decimal 除数；除以零时 panic。
func (m Money) MustDiv(divisor decimal.Decimal) Money {
	res, err := m.Div(divisor)
	if err != nil {
		panic(err)
	}
	return res
}

// Abs 返回金额的绝对值。
func (m Money) Abs() Money {
	return Money{
		amount:   m.amount.Abs(),
		currency: m.currency,
	}
}

// Neg 返回金额的相反数。
func (m Money) Neg() Money {
	return Money{
		amount:   m.amount.Neg(),
		currency: m.currency,
	}
}

// Round 对金额按照指定的精度位数与舍入模式执行舍入。
func (m Money) Round(scale int32, mode RoundMode) Money {
	return Money{
		amount:   roundDecimal(m.amount, scale, mode),
		currency: m.currency,
	}
}

// RoundToSubunit 按照币种标准的法定最小单位小数位（如 CNY 2 位，JPY 0 位）执行舍入。
func (m Money) RoundToSubunit(mode RoundMode) Money {
	return m.Round(m.currency.Fraction, mode)
}

// Percentage 计算百分比金额（如 pct 为 0.15 表示 15%）。
func (m Money) Percentage(pct decimal.Decimal) Money {
	return m.Mul(pct)
}

// Tax 计算含税/税费金额（别名，语义更清晰）。
func (m Money) Tax(rate decimal.Decimal) Money {
	return m.Percentage(rate)
}

// Discount 计算折扣金额。
func (m Money) Discount(rate decimal.Decimal) Money {
	return m.Percentage(rate)
}

// AddPercentage 在当前金额基础上加上指定百分比金额：m + (m * pct)。
func (m Money) AddPercentage(pct decimal.Decimal) Money {
	return m.Mul(decimal.NewFromInt(1).Add(pct))
}

// SubPercentage 在当前金额基础上扣减指定百分比金额：m - (m * pct)。
func (m Money) SubPercentage(pct decimal.Decimal) Money {
	return m.Mul(decimal.NewFromInt(1).Sub(pct))
}

// ExchangeTo 按照指定汇率将金额折算为目标币种：targetAmount = m.amount * rate。
func (m Money) ExchangeTo(targetCode string, rate decimal.Decimal) (Money, error) {
	if rate.LessThanOrEqual(decimal.Zero) {
		return Money{}, ErrNegativeRate
	}
	targetCurr, err := ResolveCurrency(targetCode)
	if err != nil {
		return Money{}, err
	}
	return Money{
		amount:   m.amount.Mul(rate),
		currency: targetCurr,
	}, nil
}

// ToCents 将金额转换为以分（或最小货币单位）为基准的整型数值（如 12.34 CNY -> 1234）。
func (m Money) ToCents() int64 {
	rounded := m.RoundToSubunit(RoundHalfUp)
	factor := decimal.NewFromInt(int64(math.Pow10(int(m.currency.Fraction))))
	return rounded.amount.Mul(factor).IntPart()
}

// ToGatewayString 转换为满足收单网关（如支付宝、Stripe）标准格式的两位/标定小数位字符串（如 "12.34"）。
func (m Money) ToGatewayString() string {
	rounded := m.RoundToSubunit(RoundHalfUp)
	return rounded.amount.StringFixed(m.currency.Fraction)
}

// Format 格式化为地道的人类可读字符串展示（如 "¥123.45" 或 "123.45 CNY"）。
func (m Money) Format(showSymbol bool) string {
	valStr := m.amount.StringFixed(m.currency.Fraction)
	if showSymbol && m.currency.Symbol != "" {
		return m.currency.Symbol + valStr
	}
	return valStr + " " + m.currency.Code
}

// FormatWithSymbol 格式化为携带货币符号的人类可读字符串展示（如 "¥123.45"）。
func (m Money) FormatWithSymbol() string {
	return m.Format(true)
}

// FormatWithCode 格式化为携带 ISO 货币代码的人类可读字符串展示（如 "123.45 CNY"）。
func (m Money) FormatWithCode() string {
	return m.Format(false)
}

// String 实现 fmt.Stringer 接口。
func (m Money) String() string {
	return m.FormatWithCode()
}

type jsonMoneyPayload struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON 实现 json.Marshaler 接口，序列化为安全的字符串形式以防前端 JavaScript 浮点截断。
func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(jsonMoneyPayload{
		Amount:   m.amount.String(),
		Currency: m.currency.Code,
	})
}

// parseMoneyString 解析纯字符串形式的金额（如 "123.45 CNY"、"USD 123.45" 或纯数字 "123.45"）。
func parseMoneyString(raw string) (Money, error) {
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) == 2 {
		if _, ok := GetCurrency(parts[0]); ok {
			return NewFromString(parts[1], parts[0])
		}
		return NewFromString(parts[0], parts[1])
	}
	return NewFromString(raw, CNY)
}

// UnmarshalJSON 实现 json.Unmarshaler 接口，支持对象结构与复合字符串兼容反序列化。
func (m *Money) UnmarshalJSON(data []byte) error {
	// 尝试反序列化为对象结构
	var payload jsonMoneyPayload
	if err := json.Unmarshal(data, &payload); err == nil {
		if payload.Currency == "" {
			return fmt.Errorf("%w: money payload is missing the currency field", ErrInvalidAmount)
		}
		parsed, err := NewFromString(payload.Amount, payload.Currency)
		if err != nil {
			return err
		}
		*m = parsed
		return nil
	}

	// 尝试反序列化为纯字符串
	var rawStr string
	if err := json.Unmarshal(data, &rawStr); err == nil {
		parsed, err := parseMoneyString(rawStr)
		if err != nil {
			return err
		}
		*m = parsed
		return nil
	}

	return ErrInvalidAmount
}

// Value 实现 driver.Valuer，支持将 Money 整体存入数据库单列（JSON 文本）。
func (m Money) Value() (driver.Value, error) {
	return m.MarshalJSON()
}

// Scan 实现 sql.Scanner 接口，支持从数据库读取单列（JSON 文本或分币）。
// NULL 按零额 CNY 处理：单列存储无法从 NULL 推回币种，币种必须由调用方从相邻列显式恢复。
func (m *Money) Scan(src any) error {
	if src == nil {
		*m = Zero(CNY)
		return nil
	}
	switch v := src.(type) {
	case []byte:
		return m.UnmarshalJSON(v)
	case string:
		return m.UnmarshalJSON([]byte(v))
	case int64:
		parsed, err := NewFromCents(v, CNY)
		if err != nil {
			return err
		}
		*m = parsed
		return nil
	default:
		return fmt.Errorf("money: cannot scan type %T into Money", src)
	}
}
