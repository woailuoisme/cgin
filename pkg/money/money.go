// Package money 提供高精度不可变金融货币值对象（Value Object）与金融计算基础设施。
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
)

var (
	// ErrCurrencyMismatch 两个货币对象的币种不匹配。
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	// ErrUnknownCurrency 货币代码未在币种注册表中定义。
	ErrUnknownCurrency = errors.New("money: unknown currency code")
	// ErrInvalidAmount 金额参数格式不合法或为 NaN/Inf。
	ErrInvalidAmount = errors.New("money: invalid amount")
	// ErrInvalidRatio 均摊比例切片为空、包含负数或全为零。
	ErrInvalidRatio = errors.New("money: invalid allocation ratios")
	// ErrDivisionByZero 除数为零。
	ErrDivisionByZero = errors.New("money: division by zero")
	// ErrNegativeRate 汇率必须大于零。
	ErrNegativeRate = errors.New("money: exchange rate must be positive")
)

// RoundMode 定义金融舍入模式。
type RoundMode int

const (
	// RoundHalfUp 商业四舍五入：>= 0.5 远离零向上进位（如 1.5 -> 2, 2.5 -> 3, -1.5 -> -2）。
	RoundHalfUp RoundMode = iota
	// RoundHalfEven 银行家舍入：遇 0.5 舍入到最近的偶数，消除长期统计性正向漂移偏差（如 1.5 -> 2, 2.5 -> 2）。
	RoundHalfEven
	// RoundDown 截断取整：直接丢弃目标精度之后的尾数（朝零方向截断，如 1.9 -> 1, -1.9 -> -1）。
	RoundDown
	// RoundUp 向上进位：只要尾数非零则远离零向上进位（如 1.1 -> 2, -1.1 -> -2）。
	RoundUp
)

// roundDecimal 按照指定舍入模式对 decimal.Decimal 执行精度截取。
func roundDecimal(d decimal.Decimal, scale int32, mode RoundMode) decimal.Decimal {
	switch mode {
	case RoundHalfEven:
		return d.RoundBank(scale)
	case RoundDown:
		return d.Truncate(scale)
	case RoundUp:
		return d.RoundCeil(scale)
	case RoundHalfUp:
		fallthrough
	default:
		return d.Round(scale)
	}
}

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

// Percentage 计算按比例折算的金额（pct 为 0.13 即 13%），税额与折扣额均用它表达。
// 需要得到含税/折后价时用 AddPercentage / SubPercentage。
func (m Money) Percentage(pct decimal.Decimal) Money {
	return m.Mul(pct)
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

// String 实现 fmt.Stringer 接口。
func (m Money) String() string {
	return m.Format(false)
}

// Allocate 按照给定比例切片对 Money 执行零损耗均摊（Fowler 模式）。
// 内部基于币种自身的最小法定小数位（Subunit）拆分，残余分币按比例余数大小依次递补，确保各项之和严格守恒。
func (m Money) Allocate(ratios ...int) ([]Money, error) {
	return m.AllocateWithScale(m.currency.Fraction, ratios...)
}

// AllocateWithScale 按照指定的尾数精度 scale 执行无损均摊。
func (m Money) AllocateWithScale(scale int32, ratios ...int) ([]Money, error) {
	if len(ratios) == 0 {
		return nil, ErrInvalidRatio
	}

	var totalRatio int64
	for _, r := range ratios {
		if r < 0 {
			return nil, ErrInvalidRatio
		}
		totalRatio += int64(r)
	}
	if totalRatio == 0 {
		return nil, ErrInvalidRatio
	}

	// 放大系数 factor = 10^scale
	factor := decimal.NewFromInt(int64(math.Pow10(int(scale))))
	// 转换为主币种的最小单位总整数：totalUnits = m.amount * factor
	totalUnitsDec := m.amount.Mul(factor).Truncate(0)
	totalUnits := totalUnitsDec.IntPart()

	sign := int64(1)
	if totalUnits < 0 {
		sign = -1
		totalUnits = -totalUnits
	}

	results := make([]Money, len(ratios))
	portions := make([]int64, len(ratios))
	remainders := make([]int64, len(ratios))

	var allocatedSum int64
	totalUnitsBig := big.NewInt(totalUnits)
	totalRatioBig := big.NewInt(totalRatio)
	remBig := new(big.Int)

	for i, r := range ratios {
		prod := new(big.Int).Mul(totalUnitsBig, big.NewInt(int64(r)))
		quo, rem := new(big.Int).QuoRem(prod, totalRatioBig, remBig)
		portions[i] = quo.Int64()
		allocatedSum += portions[i]
		remainders[i] = rem.Int64()
	}

	// 剩余待分配的最小单位
	remainderUnits := totalUnits - allocatedSum

	// 将剩余的最小单位优先分给余数最大的项
	for remainderUnits > 0 {
		maxRemainderIndex := 0
		var maxRemainder int64 = -1
		for i, rem := range remainders {
			if rem > maxRemainder {
				maxRemainder = rem
				maxRemainderIndex = i
			}
		}
		portions[maxRemainderIndex]++
		remainders[maxRemainderIndex] = -1 // 避免单项重复递补
		remainderUnits--
	}

	for i, portion := range portions {
		actualUnit := decimal.NewFromInt(portion * sign)
		results[i] = Money{
			amount:   actualUnit.Div(factor),
			currency: m.currency,
		}
	}

	return results, nil
}

// Split 将金额均等拆分为 n 等份，底层基于 Allocate 实现零损耗。
func (m Money) Split(parts int) ([]Money, error) {
	if parts <= 0 {
		return nil, ErrInvalidRatio
	}
	return m.Allocate(slices.Repeat([]int{1}, parts)...)
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

// UnmarshalJSON 实现 json.Unmarshaler 接口，仅接受对象结构。
// 不兼容裸字符串：币种必须随金额一起出现，否则无法安全推断。
func (m *Money) UnmarshalJSON(data []byte) error {
	var payload jsonMoneyPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAmount, err)
	}
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
