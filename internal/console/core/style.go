package core

import (
	"errors"
	"fmt"
	"io"
	"os"

	"charm.land/lipgloss/v2"
)

// 通用 CLI 选项常量。
const (
	FlagForce      = "force"
	UsageForceFlag = "Skip interactive confirmation prompt"
)

// 控制台输出样式与语义化 Badge。
var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// orStdout 返回非空输出流，nil 时回退标准输出。
func orStdout(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}

// TitleTo 渲染并打印一行标题。
func TitleTo(w io.Writer, text string) {
	_, _ = fmt.Fprintln(orStdout(w), titleStyle.Render("• "+text))
}

// SuccessTo 渲染一行成功提示。
func SuccessTo(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintln(orStdout(w), successStyle.Render("✓ "+fmt.Sprintf(format, args...)))
}

// WarnTo 渲染一行告警提示。
func WarnTo(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintln(orStdout(w), warnStyle.Render("! "+fmt.Sprintf(format, args...)))
}

// InfoTo 渲染一行常规信息。
func InfoTo(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintln(orStdout(w), fmt.Sprintf(format, args...))
}

// ErrCanceled 表示用户在交互式确认中主动放弃了操作。
//
// 它必须是一个可判定的错误而不是 nil：否则脚本无法区分"已取消"与"已执行"，
// 一次误按取消会被当成成功退出，掩盖后续流水线应当中断的事实。
var ErrCanceled = errors.New("operation canceled by user")
