// Package money 提供高精度不可变金融货币值对象（Value Object）与金融计算基础设施。
// 本文件实现基于 Fowler 模式的零损耗资金均摊（Allocate）与等额切分（Split），确保分账残差总和严格守恒。
package money

import (
	"math"
	"math/big"
	"slices"

	"github.com/shopspring/decimal"
)

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
