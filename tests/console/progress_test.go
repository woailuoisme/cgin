package console_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"bgin/internal/console/core"
)

// TestProgressBar_Interactive_Records 验证交互模式下普通记录计数进度条的渲染输出。
func TestProgressBar_Interactive_Records(t *testing.T) {
	buf := new(bytes.Buffer)
	bar := core.NewProgressBarTo(buf, true, 1000, "Processing")

	bar.Set(250)
	bar.Set(500)
	bar.Set(1000)
	bar.Finish()

	out := buf.String()
	assert.Contains(t, out, "Processing")
	assert.Contains(t, out, "1,000")
	assert.Contains(t, out, "/s")
}

// TestProgressBar_NonInteractive_Milestones 验证非交互式环境（如 CI 管道）25% 离散里程碑输出。
func TestProgressBar_NonInteractive_Milestones(t *testing.T) {
	buf := new(bytes.Buffer)
	bar := core.NewProgressBarTo(buf, false, 100, "Exporting")

	bar.Set(10) // 未达 25% 里程碑，不输出
	assert.Empty(t, buf.String())

	bar.Set(25) // 到达 25%
	assert.Contains(t, buf.String(), "25%")

	bar.Set(50) // 到达 50%
	assert.Contains(t, buf.String(), "50%")

	bar.Set(100) // 到达 100%
	bar.Finish()
	assert.Contains(t, buf.String(), "100%")
}

// TestProgressBar_ByteMode 验证字节传输进度条自动格式化容量与下载速率。
func TestProgressBar_ByteMode(t *testing.T) {
	buf := new(bytes.Buffer)
	totalBytes := int64(10 * 1024 * 1024) // 10 MB
	bar := core.NewByteProgressBarTo(buf, true, totalBytes, "Downloading")

	bar.Set(5 * 1024 * 1024) // 5 MB
	time.Sleep(10 * time.Millisecond)
	bar.Set(totalBytes) // 10 MB (100% 强制刷新)
	bar.Finish()

	out := buf.String()
	assert.Contains(t, out, "Downloading")
	assert.Contains(t, out, "MB")
	assert.Contains(t, out, "/s")
}

// TestProgressBar_UnknownTotal 验证未知总长度（如分块传输 ContentLength <= 0）时的平稳渲染。
func TestProgressBar_UnknownTotal(t *testing.T) {
	buf := new(bytes.Buffer)
	bar := core.NewByteProgressBarTo(buf, true, -1, "Streaming")

	bar.Set(2 * 1024 * 1024)
	bar.Finish()

	out := buf.String()
	assert.Contains(t, out, "Streaming")
	assert.Contains(t, out, "2.1 MB")
	assert.Contains(t, out, "/s")
}

// TestProgressBar_NarrowTerminal 验证极窄终端（如 48 列分屏）下进度条不折行且关键指标完整输出。
func TestProgressBar_NarrowTerminal(t *testing.T) {
	buf := new(bytes.Buffer)
	totalBytes := int64(10 * 1024 * 1024)
	bar := core.NewByteProgressBarTo(buf, true, totalBytes, "Downloading")
	bar.SetTermWidth(48)

	bar.Set(5 * 1024 * 1024)
	bar.Finish()

	out := buf.String()
	assert.Contains(t, out, "50%")
	assert.Contains(t, out, "5.2MB/10MB")
	assert.Contains(t, out, "/s")
	// 验证包含 ANSI 颜色序列且未被切片破坏
	assert.Contains(t, out, "\x1b[")
}
