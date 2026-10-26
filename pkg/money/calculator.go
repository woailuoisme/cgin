// Package money 提供高精度不可变金融货币值对象（Value Object）与金融计算基础设施。
// 本文件定义链式金融计算器（Calculator），为复合财务运算（加减乘除、税率、折扣、舍入）提供链式流畅接口与错误聚集。
package money

import (
	"github.com/shopspring/decimal"
)

// Calculator 提供用于链式复合金融算术计算的高阶构建器，内置错误收集机制。
type Calculator struct {
	current Money
	err     error
}

// NewCalculator 从初始金额构建复合计算器。
func NewCalculator(initial Money) *Calculator {
	return &Calculator{current: initial}
}

// Add 链式累加金额。若发生币种不匹配，将记录错误。
func (c *Calculator) Add(m Money) *Calculator {
	if c.err != nil {
		return c
	}
	res, err := c.current.Add(m)
	if err != nil {
		c.err = err
		return c
	}
	c.current = res
	return c
}

// Sub 链式扣减金额。若发生币种不匹配，将记录错误。
func (c *Calculator) Sub(m Money) *Calculator {
	if c.err != nil {
		return c
	}
	res, err := c.current.Sub(m)
	if err != nil {
		c.err = err
		return c
	}
	c.current = res
	return c
}

// Mul 链式乘以系数（如数量、税率系数）。
func (c *Calculator) Mul(factor decimal.Decimal) *Calculator {
	if c.err != nil {
		return c
	}
	c.current = c.current.Mul(factor)
	return c
}

// MulInt 链式乘以整型系数。
func (c *Calculator) MulInt(factor int64) *Calculator {
	return c.Mul(decimal.NewFromInt(factor))
}

// MulFloat64 链式乘以浮点系数。
func (c *Calculator) MulFloat64(factor float64) *Calculator {
	return c.Mul(decimal.NewFromFloat(factor))
}

// Div 链式除以除数。若除数为零，将记录 ErrDivisionByZero。
func (c *Calculator) Div(divisor decimal.Decimal) *Calculator {
	if c.err != nil {
		return c
	}
	res, err := c.current.Div(divisor)
	if err != nil {
		c.err = err
		return c
	}
	c.current = res
	return c
}

// DivInt 链式除以整型除数。
func (c *Calculator) DivInt(divisor int64) *Calculator {
	return c.Div(decimal.NewFromInt(divisor))
}

// DivFloat64 链式除以浮点除数。
func (c *Calculator) DivFloat64(divisor float64) *Calculator {
	return c.Div(decimal.NewFromFloat(divisor))
}

// Round 链式执行指定精度的舍入。
func (c *Calculator) Round(scale int32, mode RoundMode) *Calculator {
	if c.err != nil {
		return c
	}
	c.current = c.current.Round(scale, mode)
	return c
}

// RoundToSubunit 链式按照法定最小单位舍入。
func (c *Calculator) RoundToSubunit(mode RoundMode) *Calculator {
	if c.err != nil {
		return c
	}
	c.current = c.current.RoundToSubunit(mode)
	return c
}

// Result 获取计算器最终计算结果与错误。
func (c *Calculator) Result() (Money, error) {
	if c.err != nil {
		return Money{}, c.err
	}
	return c.current, nil
}

// MustResult 获取计算器最终结果，若计算过程中发生错误则直接 panic。
func (c *Calculator) MustResult() Money {
	res, err := c.Result()
	if err != nil {
		panic(err)
	}
	return res
}
