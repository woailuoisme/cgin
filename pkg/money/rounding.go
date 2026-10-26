// Package money 提供高精度不可变金融货币值对象（Value Object）与金融计算基础设施。
// 本文件定义金融舍入模式枚举（四舍五入、银行家舍入、截断取整、向上进位）及底层 Decimal 舍入截取策略。
package money

import "github.com/shopspring/decimal"

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
