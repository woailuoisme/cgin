package money_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/money"
)

func TestCurrency_Registry(t *testing.T) {
	cny, ok := money.GetCurrency(money.CNY)
	require.True(t, ok)
	assert.Equal(t, "CNY", cny.Code)
	assert.Equal(t, int32(2), cny.Fraction)
	assert.Equal(t, "¥", cny.Symbol)

	jpy, ok := money.GetCurrency(money.JPY)
	require.True(t, ok)
	assert.Equal(t, int32(0), jpy.Fraction)

	// 自定义货币注册
	err := money.RegisterCurrency(money.Currency{
		Code:     "POINT",
		Fraction: 0,
		Symbol:   "P",
		Name:     "Reward Points",
	})
	require.NoError(t, err)

	point := money.MustGetCurrency("point")
	assert.Equal(t, "POINT", point.Code)
	assert.Equal(t, int32(0), point.Fraction)

	// 非法注册校验
	require.Error(t, money.RegisterCurrency(money.Currency{Code: "", Fraction: 2}))
	require.Error(t, money.RegisterCurrency(money.Currency{Code: "FAIL", Fraction: -1}))
}

func TestMoney_CreationAndBasicChecks(t *testing.T) {
	m1 := money.MustNew(decimal.NewFromFloat(100.50), money.CNY)
	assert.Equal(t, "100.5", m1.Amount().String())
	assert.Equal(t, money.CNY, m1.CurrencyCode())
	assert.True(t, m1.IsPositive())
	assert.False(t, m1.IsNegative())
	assert.False(t, m1.IsZero())

	mZero := money.Zero(money.USD)
	assert.True(t, mZero.IsZero())
	assert.False(t, mZero.IsPositive())

	mNeg, err := money.NewFromString("-50.25", money.USD)
	require.NoError(t, err)
	assert.True(t, mNeg.IsNegative())

	// 从分币创建
	mCents, err := money.NewFromCents(12345, money.CNY)
	require.NoError(t, err)
	assert.Equal(t, "123.45", mCents.Amount().String())

	// 从整型创建
	mInt, err := money.NewFromInt(99, money.EUR)
	require.NoError(t, err)
	assert.Equal(t, "99", mInt.Amount().String())

	// 非法参数测试
	_, err = money.NewFromString("invalid", money.CNY)
	require.Error(t, err)

	_, err = money.NewFromFloat(math.Inf(1), money.USD)
	require.Error(t, err)

	_, err = money.NewFromFloat(math.NaN(), money.USD)
	require.Error(t, err)
}

func TestMoney_Comparisons(t *testing.T) {
	m1 := money.MustNew(decimal.NewFromFloat(100.00), money.CNY)
	m2 := money.MustNew(decimal.NewFromFloat(100.00), money.CNY)
	m3 := money.MustNew(decimal.NewFromFloat(200.00), money.CNY)
	mUSD := money.MustNew(decimal.NewFromFloat(100.00), money.USD)

	assert.True(t, m1.Equal(m2))
	assert.False(t, m1.Equal(m3))
	assert.False(t, m1.Equal(mUSD))

	gt, err := m3.GreaterThan(m1)
	require.NoError(t, err)
	assert.True(t, gt)

	lt, err := m1.LessThan(m3)
	require.NoError(t, err)
	assert.True(t, lt)

	gte, err := m1.GreaterThanOrEqual(m2)
	require.NoError(t, err)
	assert.True(t, gte)

	// 币种不一致比较报错
	_, err = m1.Compare(mUSD)
	require.ErrorIs(t, err, money.ErrCurrencyMismatch)
}

func TestMoney_Arithmetic(t *testing.T) {
	m1 := money.MustNew(decimal.NewFromFloat(100.25), money.CNY)
	m2 := money.MustNew(decimal.NewFromFloat(50.75), money.CNY)
	mUSD := money.MustNew(decimal.NewFromFloat(10.00), money.USD)

	// Add
	sum, err := m1.Add(m2)
	require.NoError(t, err)
	assert.Equal(t, "151", sum.Amount().String())
	assert.Equal(t, "151", m1.MustAdd(m2).Amount().String())

	_, err = m1.Add(mUSD)
	require.ErrorIs(t, err, money.ErrCurrencyMismatch)
	assert.Panics(t, func() { m1.MustAdd(mUSD) })

	// Sub
	diff, err := m1.Sub(m2)
	require.NoError(t, err)
	assert.Equal(t, "49.5", diff.Amount().String())
	assert.Equal(t, "49.5", m1.MustSub(m2).Amount().String())

	_, err = m1.Sub(mUSD)
	require.ErrorIs(t, err, money.ErrCurrencyMismatch)

	// Mul
	prod := m1.Mul(decimal.NewFromInt(2))
	assert.Equal(t, "200.5", prod.Amount().String())
	assert.Equal(t, "300.75", m1.MulInt(3).Amount().String())
	assert.Equal(t, "50.125", m1.MulFloat64(0.5).Amount().String())

	// Div
	quot, err := m1.Div(decimal.NewFromInt(2))
	require.NoError(t, err)
	assert.Equal(t, "50.125", quot.Amount().String())
	assert.Equal(t, "50.125", m1.MustDiv(decimal.NewFromInt(2)).Amount().String())

	_, err = m1.Div(decimal.Zero)
	require.ErrorIs(t, err, money.ErrDivisionByZero)
	assert.Panics(t, func() { m1.MustDiv(decimal.Zero) })

	// Abs & Neg
	assert.Equal(t, "-100.25", m1.Neg().Amount().String())
	assert.Equal(t, "100.25", m1.Neg().Abs().Amount().String())
}

func TestMoney_RoundingModes(t *testing.T) {
	// 2.555 -> 2.56 (HalfUp)
	m := money.MustNew(decimal.NewFromFloat(2.555), money.USD)
	assert.Equal(t, "2.56", m.Round(2, money.RoundHalfUp).Amount().String())

	// 银行家舍入 (Banker's Rounding: 遇 0.5 舍入到最近偶数)
	// 2.5 -> 2 (Even)
	mEven1 := money.MustNew(decimal.NewFromFloat(2.5), money.USD)
	assert.Equal(t, "2", mEven1.Round(0, money.RoundHalfEven).Amount().String())
	// 3.5 -> 4 (Even)
	mEven2 := money.MustNew(decimal.NewFromFloat(3.5), money.USD)
	assert.Equal(t, "4", mEven2.Round(0, money.RoundHalfEven).Amount().String())

	// 截断取整 RoundDown
	assert.Equal(t, "2.55", m.Round(2, money.RoundDown).Amount().String())

	// 向上进位 RoundUp
	assert.Equal(t, "2.56", m.Round(2, money.RoundUp).Amount().String())

	// 币种法定小数位直接舍入 RoundToSubunit (CNY 为 2 位)
	assert.Equal(t, "2.56", m.RoundToSubunit(money.RoundHalfUp).Amount().String())

	// JPY 法定小数位为 0
	jpy := money.MustNew(decimal.NewFromFloat(1234.56), money.JPY)
	assert.Equal(t, "1235", jpy.RoundToSubunit(money.RoundHalfUp).Amount().String())
}

func TestMoney_FowlerAllocation(t *testing.T) {
	// 10.00 元按 1:1:1 三等分（除不尽，应保证总和严格为 10.00）
	m := money.MustNew(decimal.NewFromFloat(10.00), money.CNY)
	parts, err := m.Allocate(1, 1, 1)
	require.NoError(t, err)
	require.Len(t, parts, 3)

	assert.Equal(t, "3.34", parts[0].Amount().String())
	assert.Equal(t, "3.33", parts[1].Amount().String())
	assert.Equal(t, "3.33", parts[2].Amount().String())

	// 守恒校验
	sum := parts[0].MustAdd(parts[1]).MustAdd(parts[2])
	assert.True(t, sum.Equal(m))

	// Split 辅助语法糖
	splitParts, err := m.Split(3)
	require.NoError(t, err)
	assert.Equal(t, parts[0].Amount().String(), splitParts[0].Amount().String())

	// 70:30 分账场景：10.05 元
	mRev := money.MustNew(decimal.NewFromFloat(10.05), money.CNY)
	revParts, err := mRev.Allocate(70, 30)
	require.NoError(t, err)
	assert.Equal(t, "7.04", revParts[0].Amount().String())
	assert.Equal(t, "3.01", revParts[1].Amount().String())
	assert.True(t, revParts[0].MustAdd(revParts[1]).Equal(mRev))

	// 负数均摊守恒
	mNeg := money.MustNew(decimal.NewFromFloat(-10.00), money.CNY)
	negParts, err := mNeg.Allocate(1, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, "-3.34", negParts[0].Amount().String())
	assert.Equal(t, "-3.33", negParts[1].Amount().String())
	assert.Equal(t, "-3.33", negParts[2].Amount().String())
	assert.True(t, negParts[0].MustAdd(negParts[1]).MustAdd(negParts[2]).Equal(mNeg))

	// 非法比例
	_, err = m.Allocate()
	require.ErrorIs(t, err, money.ErrInvalidRatio)
	_, err = m.Allocate(0, 0)
	require.ErrorIs(t, err, money.ErrInvalidRatio)
	_, err = m.Allocate(1, -1)
	require.ErrorIs(t, err, money.ErrInvalidRatio)
}

func TestMoney_TaxDiscountAndExchange(t *testing.T) {
	price := money.MustNew(decimal.NewFromFloat(100.00), money.CNY)

	// 13% 增值税
	tax := price.Tax(decimal.NewFromFloat(0.13))
	assert.Equal(t, "13", tax.Amount().String())

	// 含税价
	withTax := price.AddPercentage(decimal.NewFromFloat(0.13))
	assert.Equal(t, "113", withTax.Amount().String())

	// 八折优惠 (20% 折扣)
	discount := price.Discount(decimal.NewFromFloat(0.20))
	assert.Equal(t, "20", discount.Amount().String())
	afterDiscount := price.SubPercentage(decimal.NewFromFloat(0.20))
	assert.Equal(t, "80", afterDiscount.Amount().String())

	// 汇率转换：100 USD 转换为 CNY (汇率 7.1234)
	usd := money.MustNew(decimal.NewFromFloat(100.00), money.USD)
	cny, err := usd.ExchangeTo(money.CNY, decimal.NewFromFloat(7.1234))
	require.NoError(t, err)
	assert.Equal(t, money.CNY, cny.CurrencyCode())
	assert.Equal(t, "712.34", cny.Amount().StringFixed(2))

	// 非法汇率
	_, err = usd.ExchangeTo(money.CNY, decimal.Zero)
	require.ErrorIs(t, err, money.ErrNegativeRate)
}

func TestMoney_GatewayAdaptersAndFormatting(t *testing.T) {
	m := money.MustNew(decimal.NewFromFloat(12.345), money.CNY)

	// 微信支付整型分 (四舍五入 12.35 -> 1235)
	assert.Equal(t, int64(1235), m.ToCents())

	// 支付宝字串两位小数 ("12.35")
	assert.Equal(t, "12.35", m.ToGatewayString())

	// 格式化展示
	assert.Equal(t, "¥12.35", m.Format(true))
	assert.Equal(t, "12.35 CNY", m.Format(false))
	assert.Equal(t, "¥12.35", m.FormatWithSymbol())
	assert.Equal(t, "12.35 CNY", m.FormatWithCode())
	assert.Equal(t, "12.35 CNY", m.String())
}

func TestMoney_JSONSerialization(t *testing.T) {
	m := money.MustNew(decimal.NewFromFloat(1234.5678), money.USD)

	// 序列化为对象结构
	data, err := json.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"amount":"1234.5678"`)
	assert.Contains(t, string(data), `"currency":"USD"`)

	// 反序列化对象
	var parsed money.Money
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)
	assert.True(t, parsed.Equal(m))

	// 反序列化复合字串 "USD 1234.5678"
	var parsedFromStr money.Money
	err = json.Unmarshal([]byte(`"USD 1234.5678"`), &parsedFromStr)
	require.NoError(t, err)
	assert.True(t, parsedFromStr.Equal(m))

	// 反序列化纯数字字串（默认 CNY）
	var defaultCNY money.Money
	err = json.Unmarshal([]byte(`"99.50"`), &defaultCNY)
	require.NoError(t, err)
	assert.Equal(t, money.CNY, defaultCNY.CurrencyCode())
	assert.Equal(t, "99.5", defaultCNY.Amount().String())
}

func TestMoney_DatabaseValueAndScan(t *testing.T) {
	m := money.MustNew(decimal.NewFromFloat(88.88), money.CNY)

	// Value()
	val, err := m.Value()
	require.NoError(t, err)

	// Scan() bytes
	var scanned money.Money
	err = scanned.Scan(val)
	require.NoError(t, err)
	assert.True(t, scanned.Equal(m))

	// Scan() cents (int64)
	var scannedFromCents money.Money
	err = scannedFromCents.Scan(int64(8888))
	require.NoError(t, err)
	assert.Equal(t, "88.88", scannedFromCents.Amount().String())
	assert.Equal(t, money.CNY, scannedFromCents.CurrencyCode())

	// Scan() nil
	var nilScanned money.Money
	err = nilScanned.Scan(nil)
	require.NoError(t, err)
	assert.True(t, nilScanned.IsZero())
}

func TestMoney_Calculator(t *testing.T) {
	initial := money.MustNew(decimal.NewFromInt(100), money.CNY)
	calc := money.NewCalculator(initial).
		Add(money.MustNew(decimal.NewFromInt(50), money.CNY)).
		Sub(money.MustNew(decimal.NewFromInt(20), money.CNY)).
		MulInt(2).
		DivInt(2)

	res, err := calc.Result()
	require.NoError(t, err)
	assert.Equal(t, "130", res.Amount().String())
	assert.Equal(t, money.CNY, res.CurrencyCode())
	assert.Equal(t, res, calc.MustResult())

	// 币种不匹配错误聚集
	calcErr := money.NewCalculator(initial).
		Add(money.MustNew(decimal.NewFromInt(10), money.USD)).
		Sub(money.MustNew(decimal.NewFromInt(5), money.CNY))
	_, err = calcErr.Result()
	require.ErrorIs(t, err, money.ErrCurrencyMismatch)
	assert.Panics(t, func() {
		calcErr.MustResult()
	})

	// 除以零错误聚集
	calcDivZero := money.NewCalculator(initial).DivInt(0)
	_, err = calcDivZero.Result()
	require.ErrorIs(t, err, money.ErrDivisionByZero)

	// 舍入方法
	rounded := money.NewCalculator(money.MustNew(decimal.NewFromFloat(123.456), money.CNY)).
		RoundToSubunit(money.RoundHalfUp).
		MustResult()
	assert.Equal(t, "123.46", rounded.Amount().String())
}

func TestMoney_MemoryConverter(t *testing.T) {
	ctx := t.Context()
	converter := money.NewMemoryConverter()

	converter.SetRate(money.USD, money.CNY, decimal.NewFromFloat(7.25))

	usd := money.MustNew(decimal.NewFromInt(100), money.USD)

	// 同币种转换
	same, err := converter.Convert(ctx, usd, money.USD)
	require.NoError(t, err)
	assert.True(t, same.Equal(usd))

	// USD 转换为 CNY
	cny, err := converter.Convert(ctx, usd, money.CNY)
	require.NoError(t, err)
	assert.Equal(t, money.CNY, cny.CurrencyCode())
	assert.Equal(t, "725", cny.Amount().String())

	// 缺失汇率报错
	_, err = converter.Convert(ctx, usd, money.EUR)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exchange rate not found")
}
