package core

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
	"github.com/dustin/go-humanize"
)

// ProgressBar 提供自适应终端进度条，自动区分交互式终端 (动态单行就地渲染) 与非交互式环境 (离散里程碑输出)。
type ProgressBar struct {
	w             io.Writer
	model         progress.Model
	total         int64
	current       int64
	label         string
	interactive   bool
	isBytes       bool
	startTime     time.Time
	lastMilestone int
	lastRender    time.Time
	mu            sync.Mutex
	finished      bool
	termWidth     int
}

// SetTermWidth 设置指定进度条的终端列宽（为 0 时恢复自动探测，主要用于窄终端适配与单元测试）。
func (b *ProgressBar) SetTermWidth(width int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.termWidth = width
}

func (b *ProgressBar) getTerminalWidth() int {
	if b.termWidth > 0 {
		return b.termWidth
	}
	return terminalWidth(b.w)
}

// terminalWidth 获取指定输出目标的可用终端列宽，探测失败时安全退回默认值 80。
func terminalWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if width, _, err := term.GetSize(f.Fd()); err == nil && width > 0 {
			return width
		}
	}
	if width, _, err := term.GetSize(os.Stdout.Fd()); err == nil && width > 0 {
		return width
	}
	return 80
}

// NewProgressBarTo 构造写入指定 writer 的全新终端进度条。
func NewProgressBarTo(w io.Writer, interactive bool, total int64, label string) *ProgressBar {
	if total < 0 {
		total = 0
	}
	if w == nil {
		w = os.Stdout
	}
	return &ProgressBar{
		w:           w,
		model:       progress.New(progress.WithColors(lipgloss.Color("39"), lipgloss.Color("42")), progress.WithWidth(20), progress.WithoutPercentage()),
		total:       total,
		label:       label,
		interactive: interactive,
		startTime:   time.Now(),
	}
}

// NewByteProgressBarTo 构造用于字节流/文件传输的终端进度条，自动格式化字节容量与传输速率。
func NewByteProgressBarTo(w io.Writer, interactive bool, total int64, label string) *ProgressBar {
	b := NewProgressBarTo(w, interactive, total, label)
	b.isBytes = true
	return b
}

// Set 更新当前已完成数量并刷新进度条展示。
func (b *ProgressBar) Set(current int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.finished {
		return
	}

	if current > b.total && b.total > 0 {
		current = b.total
	}
	b.current = current

	now := time.Now()
	// 交互模式下节流渲染（至少间隔 50ms 刷新一次，除非已达到 100%），减少终端高频重绘与 IO 阻塞
	if b.interactive && b.total > 0 && current < b.total && now.Sub(b.lastRender) < 50*time.Millisecond {
		return
	}
	b.lastRender = now

	elapsed := max(now.Sub(b.startTime).Seconds(), 0.001)
	currStr, totalStr, rateStr := b.formatMetrics(float64(b.current) / elapsed)

	if b.interactive {
		b.renderInteractive(currStr, totalStr, rateStr)
		return
	}

	b.renderNonInteractive(currStr, totalStr, rateStr)
}

func (b *ProgressBar) formatMetrics(rate float64) (currStr, totalStr, rateStr string) {
	if b.isBytes {
		currStr = humanize.Bytes(uint64(max(0, b.current)))
		if b.total > 0 {
			totalStr = humanize.Bytes(uint64(b.total))
		}
		rateStr = humanize.Bytes(uint64(max(0, rate))) + "/s"
		return currStr, totalStr, rateStr
	}

	currStr = humanize.Comma(max(0, b.current))
	if b.total > 0 {
		totalStr = humanize.Comma(b.total)
	}
	rateStr = humanize.Comma(int64(max(0, rate))) + "/s"
	return currStr, totalStr, rateStr
}

func (b *ProgressBar) renderInteractive(currStr, totalStr, rateStr string) {
	termWidth := b.getTerminalWidth()
	if termWidth <= 0 {
		termWidth = 80
	}

	ratio := 0.0
	if b.total > 0 {
		ratio = float64(b.current) / float64(b.total)
	} else if b.current > 0 {
		// 未知总长度时，以已传输数据量生成周期性滑块动画，确保进度条始终可见
		ratio = float64((b.current/(128*1024))%10+1) / 10.0
	}

	// 宽终端 (>= 75 列)：展示宽幅进度条与完整指标
	if termWidth >= 75 {
		b.model.SetWidth(18)
		barStr := b.model.ViewAs(ratio)
		if b.total > 0 {
			pct := int(ratio * 100)
			_, _ = fmt.Fprintf(b.w, "\r\033[K  %-11s %s %3d%% (%s / %s) %s", b.label, barStr, pct, currStr, totalStr, rateStr)
		} else {
			_, _ = fmt.Fprintf(b.w, "\r\033[K  %-11s %s %s %s", b.label, barStr, currStr, rateStr)
		}
		return
	}

	// 窄终端 (< 75 列)：精确度量各组件可见列宽，自适应计算条宽，严禁对含 ANSI 码的字符串做切片截断
	metrics := b.formatCompactMetrics(ratio, currStr, totalStr, rateStr)
	metricsWidth := lipgloss.Width(metrics)

	// 终端列宽大于等于 60 时保留前缀标签，极窄终端时省略标签以保障进度条与数值完整展示
	prefix := "  "
	if termWidth >= 60 {
		prefix = fmt.Sprintf("  %-11s ", b.label)
	}
	prefixWidth := lipgloss.Width(prefix)

	// 动态计算进度条可见列宽（至少 8 列，至多 14 列，且整体必定小于 termWidth）
	availBar := termWidth - prefixWidth - metricsWidth - 2
	barWidth := max(8, min(14, availBar))
	b.model.SetWidth(barWidth)
	barStr := b.model.ViewAs(ratio)

	_, _ = fmt.Fprintf(b.w, "\r\033[K%s%s %s", prefix, barStr, metrics)
}

func (b *ProgressBar) formatCompactMetrics(ratio float64, currStr, totalStr, rateStr string) string {
	if b.total <= 0 {
		return fmt.Sprintf("%s %s", currStr, rateStr)
	}
	pct := int(ratio * 100)
	compactSize := strings.ReplaceAll(currStr, " ", "") + "/" + strings.ReplaceAll(totalStr, " ", "")
	return fmt.Sprintf("%3d%% %s %s", pct, compactSize, rateStr)
}

func (b *ProgressBar) renderNonInteractive(currStr, totalStr, rateStr string) {
	if b.total > 0 {
		pct := int((float64(b.current) / float64(b.total)) * 100)
		if pct >= b.lastMilestone+25 || (pct == 100 && b.lastMilestone < 100) {
			b.lastMilestone = (pct / 25) * 25
			_, _ = fmt.Fprintf(
				b.w, "  • %s: %d%% (%s / %s) %s\n",
				b.label,
				pct,
				currStr,
				totalStr,
				rateStr,
			)
		}
		return
	}

	if b.current >= int64(b.lastMilestone+5*1024*1024) {
		b.lastMilestone = int(b.current)
		_, _ = fmt.Fprintf(b.w, "  • %s: %s (%s)\n", b.label, currStr, rateStr)
	}
}

// Incr 增量更新已完成数量。
func (b *ProgressBar) Incr(delta int64) {
	b.Set(b.current + delta)
}

// Finish 标记进度条结束并清理输出行。
func (b *ProgressBar) Finish() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.finished {
		return
	}
	b.finished = true

	if b.interactive {
		// 清理动态行，使后续输出紧随其后干净打印
		_, _ = fmt.Fprint(b.w, "\r\033[K")
	}
}
