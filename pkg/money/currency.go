// Package money 提供高精度不可变金融货币值对象（Value Object）与金融计算基础设施。
// 本文件定义 ISO-4217 货币元数据规范、全局并发安全货币注册表以及主流法币（CNY、USD、EUR 等）预置常量。
package money

import (
	"fmt"
	"strings"
	"sync"
)

// Currency 承载 ISO-4217 货币元数据定义。
type Currency struct {
	// Code 国际标准三位大写字母货币代码（如 "CNY", "USD"）。
	Code string `json:"code" example:"CNY"`
	// Fraction 官方指定小数位数/次要单位幂次（如 JPY 为 0，CNY/USD 为 2，BHD 为 3）。
	Fraction int32 `json:"fraction" example:"2"`
	// Symbol 货币符号（如 "¥", "$", "€"）。
	Symbol string `json:"symbol" example:"¥"`
	// Name 货币标准英文全称。
	Name string `json:"name" example:"Chinese Yuan"`
}

// 预定义常见主流法定货币常数。
const (
	CNY = "CNY" // 人民币
	USD = "USD" // 美元
	EUR = "EUR" // 欧元
	JPY = "JPY" // 日元
	GBP = "GBP" // 英镑
	HKD = "HKD" // 港币
	SGD = "SGD" // 新加坡元
	AUD = "AUD" // 澳大利亚元
	CAD = "CAD" // 加拿大元
	CHF = "CHF" // 瑞士法郎
)

var (
	registryLock sync.RWMutex
	registry     = map[string]Currency{
		CNY: {Code: CNY, Fraction: 2, Symbol: "¥", Name: "Chinese Yuan"},
		USD: {Code: USD, Fraction: 2, Symbol: "$", Name: "US Dollar"},
		EUR: {Code: EUR, Fraction: 2, Symbol: "€", Name: "Euro"},
		JPY: {Code: JPY, Fraction: 0, Symbol: "¥", Name: "Japanese Yen"},
		GBP: {Code: GBP, Fraction: 2, Symbol: "£", Name: "British Pound"},
		HKD: {Code: HKD, Fraction: 2, Symbol: "HK$", Name: "Hong Kong Dollar"},
		SGD: {Code: SGD, Fraction: 2, Symbol: "S$", Name: "Singapore Dollar"},
		AUD: {Code: AUD, Fraction: 2, Symbol: "A$", Name: "Australian Dollar"},
		CAD: {Code: CAD, Fraction: 2, Symbol: "C$", Name: "Canadian Dollar"},
		CHF: {Code: CHF, Fraction: 2, Symbol: "CHF", Name: "Swiss Franc"},
	}
)

// RegisterCurrency 注册或覆盖自定义货币/虚拟资产元数据。
func RegisterCurrency(c Currency) error {
	code := strings.ToUpper(strings.TrimSpace(c.Code))
	if code == "" || c.Fraction < 0 {
		return ErrUnknownCurrency
	}
	c.Code = code

	registryLock.Lock()
	defer registryLock.Unlock()
	registry[code] = c
	return nil
}

// GetCurrency 根据货币代码获取元数据对象。
func GetCurrency(code string) (Currency, bool) {
	upper := strings.ToUpper(strings.TrimSpace(code))
	registryLock.RLock()
	defer registryLock.RUnlock()
	c, ok := registry[upper]
	return c, ok
}

// ResolveCurrency 严格解析已注册的货币元数据。
// 代码为空或未注册时返回 ErrUnknownCurrency——金融场景绝不静默捏造一个币种，
// 否则一次拼写错误（如 "USDD"）会以错误的精度参与后续全部计算。
func ResolveCurrency(code string) (Currency, error) {
	normalised := strings.ToUpper(strings.TrimSpace(code))
	if normalised == "" {
		return Currency{}, ErrUnknownCurrency
	}
	c, ok := GetCurrency(normalised)
	if !ok {
		return Currency{}, fmt.Errorf("%w: %s (register it via RegisterCurrency first)", ErrUnknownCurrency, normalised)
	}
	return c, nil
}

// MustGetCurrency 获取已注册币种元数据，未注册时 panic（遵循 Go 的 Must* 约定）。
func MustGetCurrency(code string) Currency {
	c, err := ResolveCurrency(code)
	if err != nil {
		panic(err)
	}
	return c
}
