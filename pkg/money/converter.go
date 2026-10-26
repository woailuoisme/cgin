// Package money 提供高精度不可变金融货币值对象（Value Object）与金融计算基础设施。
// 本文件定义基于静态内存汇率表的外汇换算实现。
package money

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/shopspring/decimal"
)

// ExchangeRate 定义两个币种之间的单向汇率。
type ExchangeRate struct {
	From string
	To   string
	Rate decimal.Decimal
}

// MemoryConverter 提供基于内存映射的并发安全外汇转换器实现。
// 调用方按需直接依赖该具体类型：汇率来源是外部决策（表、行情接口、配置），
// 提前抽一层接口只会把实现细节搬来搬去。
type MemoryConverter struct {
	mu    sync.RWMutex
	rates map[string]decimal.Decimal // key: "FROM:TO"
}

// NewMemoryConverter 实例化内存外汇换算器。
func NewMemoryConverter() *MemoryConverter {
	return &MemoryConverter{
		rates: make(map[string]decimal.Decimal),
	}
}

// SetRate 设置或更新货币对汇率（例如 "USD", "CNY", 7.25）。
func (c *MemoryConverter) SetRate(from, to string, rate decimal.Decimal) {
	from = strings.ToUpper(strings.TrimSpace(from))
	to = strings.ToUpper(strings.TrimSpace(to))

	c.mu.Lock()
	defer c.mu.Unlock()
	c.rates[from+":"+to] = rate
}

// GetRate 获取两个币种之间的即期汇率。
func (c *MemoryConverter) GetRate(from, to string) (decimal.Decimal, bool) {
	from = strings.ToUpper(strings.TrimSpace(from))
	to = strings.ToUpper(strings.TrimSpace(to))
	if from == to {
		return decimal.NewFromInt(1), true
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	rate, ok := c.rates[from+":"+to]
	return rate, ok
}

// Convert 按已配置汇率把金额换算为目标币种。
//
// ctx 当前未被使用（内存汇率表无 I/O），签名保留是为了让后续替换成"远程汇率源"
// 的实现无需改动任何调用方。
func (c *MemoryConverter) Convert(_ context.Context, amount Money, targetCurrencyCode string) (Money, error) {
	target := strings.ToUpper(strings.TrimSpace(targetCurrencyCode))
	if amount.CurrencyCode() == target {
		return amount, nil
	}

	rate, ok := c.GetRate(amount.CurrencyCode(), target)
	if !ok || rate.IsZero() || rate.IsNegative() {
		return Money{}, fmt.Errorf("exchange rate not found for %s to %s", amount.CurrencyCode(), target)
	}

	convertedAmount := amount.Amount().Mul(rate)
	return New(convertedAmount, target)
}
