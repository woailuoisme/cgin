// Package money 提供高精度不可变金融货币值对象（Value Object）与金融计算基础设施。
// 本文件定义货币金额计算、币种校验与均摊转换过程中可能触发的领域哨兵错误（如币种不匹配、非法金额、除零等）。
package money

import "errors"

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
