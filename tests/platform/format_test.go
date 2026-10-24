package platform_test

import (
	"testing"
	"time"

	"github.com/dromara/carbon/v2"
	"github.com/dustin/go-humanize"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCarbon_TimeCalculationsAndUTC 验证 Carbon 的时间加减、比较与原生 UTC time.Time 互转。
func TestCarbon_TimeCalculationsAndUTC(t *testing.T) {
	// 固定基准时间
	baseTime := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	c := carbon.CreateFromStdTime(baseTime)

	// 1. 链式运算：加 3 天 2 小时
	later := c.AddDays(3).AddHours(2)
	assert.Equal(t, 2026, later.Year())
	assert.Equal(t, 9, later.Month())
	assert.Equal(t, 30, later.Day())
	assert.Equal(t, 14, later.Hour())

	// 2. 转回原生 Go 标准 time.Time 必须能够无缝兼容
	stdTime := later.StdTime()
	assert.Equal(t, time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC), stdTime)

	// 3. 符合 api.md 2.2 规范：UTC RFC3339 字符串以 Z 结尾
	rfc3339Z := later.ToRfc3339String()
	assert.Equal(t, "2026-09-30T14:00:00Z", rfc3339Z)
}

// TestCarbon_DiffForHumans_I18n 验证 Carbon 的多语言相对时间计算。
func TestCarbon_DiffForHumans_I18n(t *testing.T) {
	now := carbon.Now()
	twoHoursAgo := now.SubHours(2)

	// 英文测试
	diffEn := twoHoursAgo.SetLocale("en").DiffForHumans()
	assert.Contains(t, diffEn, "2 hours ago")

	// 中文测试
	diffZh := twoHoursAgo.SetLocale("zh-CN").DiffForHumans()
	assert.Contains(t, diffZh, "2 小时前")
}

// TestCarbon_TimezoneConversion 验证 Carbon 在系统时区（如 Asia/Shanghai）与 UTC 之间的精准转换。
func TestCarbon_TimezoneConversion(t *testing.T) {
	// 2026-09-27 12:00:00 UTC 对应 Asia/Shanghai 应该是 20:00:00
	utcTime := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	c := carbon.CreateFromStdTime(utcTime)

	shanghai := c.SetTimezone("Asia/Shanghai")
	require.Equal(t, 20, shanghai.Hour())
	assert.Equal(t, "Asia/Shanghai", shanghai.Timezone())

	// 转回 UTC 应该恢复 12 点
	backToUTC := shanghai.SetTimezone("UTC")
	assert.Equal(t, 12, backToUTC.Hour())
}

// TestHumanize_BytesAndNumbers 验证 go-humanize 的字节容量与千分位数字格式化。
func TestHumanize_BytesAndNumbers(t *testing.T) {
	// 1. 存储容量 (十进制 SI 与 二进制 IEC)
	assert.Equal(t, "1.0 MB", humanize.Bytes(1000*1000))
	assert.Equal(t, "1.0 MiB", humanize.IBytes(1024*1024))
	assert.Equal(t, "15 GB", humanize.Bytes(15*1000*1000*1000))
	assert.Equal(t, "15 GiB", humanize.IBytes(15*1024*1024*1024))

	// 2. 大数字千分位
	assert.Equal(t, "1,234,567", humanize.Comma(1234567))
	assert.Equal(t, "100,000,000", humanize.Comma(100000000))

	// 3. 序数
	assert.Equal(t, "1st", humanize.Ordinal(1))
	assert.Equal(t, "2nd", humanize.Ordinal(2))
	assert.Equal(t, "3rd", humanize.Ordinal(3))
	assert.Equal(t, "4th", humanize.Ordinal(4))
}

// TestCarbon_ParseFlexibleFormats 验证 Carbon 智能解析常见日期时间字符串。
func TestCarbon_ParseFlexibleFormats(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"2026-09-27 15:04:05", "2026-09-27 15:04:05"},
		{"2026-09-27T15:04:05Z", "2026-09-27 15:04:05"},
		{"2026-09-27", "2026-09-27 00:00:00"},
		{"2026/09/27 15:04:05", "2026-09-27 15:04:05"},
	}

	for _, tc := range cases {
		c := carbon.Parse(tc.input)
		require.NoError(t, c.Error)
		require.True(t, c.IsValid())
		assert.Equal(t, tc.expected, c.ToDateTimeString())
	}
}
