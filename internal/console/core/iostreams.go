package core

import (
	"bytes"
	"io"
	"os"

	"github.com/charmbracelet/x/term"
)

// IOStreams 统筹终端的标准输入、输出与错误流，并提供 TTY 能力判定与色彩控制 (ISP & LSP)。
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer

	stdinIsTTY  bool
	stdoutIsTTY bool
	stderrIsTTY bool

	colorEnabled bool
}

// SystemIOStreams 返回连接到操作系统标准流的生产环境 IOStreams。
func SystemIOStreams() *IOStreams {
	stdinTTY := term.IsTerminal(os.Stdin.Fd())
	stdoutTTY := term.IsTerminal(os.Stdout.Fd())
	stderrTTY := term.IsTerminal(os.Stderr.Fd())

	return &IOStreams{
		In:           os.Stdin,
		Out:          os.Stdout,
		ErrOut:       os.Stderr,
		stdinIsTTY:   stdinTTY,
		stdoutIsTTY:  stdoutTTY,
		stderrIsTTY:  stderrTTY,
		colorEnabled: stdoutTTY && os.Getenv("NO_COLOR") == "",
	}
}

// TestIOStreams 创建完全基于内存缓冲区的测试用 IOStreams，便于捕获和断言命令输出。
func TestIOStreams() (*IOStreams, *bytes.Buffer, *bytes.Buffer, *bytes.Buffer) {
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}

	streams := &IOStreams{
		In:           in,
		Out:          out,
		ErrOut:       errOut,
		stdinIsTTY:   false,
		stdoutIsTTY:  false,
		stderrIsTTY:  false,
		colorEnabled: false,
	}

	return streams, in, out, errOut
}

// CanPrompt 返回当前环境是否具备完整的人机交互 TTY 能力。
func (s *IOStreams) CanPrompt() bool {
	return s.stdinIsTTY && s.stdoutIsTTY
}

// SetColorEnabled 显式启用或禁用输出色彩。
func (s *IOStreams) SetColorEnabled(enabled bool) {
	s.colorEnabled = enabled
}

// ColorEnabled 返回当前色彩是否启用。
func (s *IOStreams) ColorEnabled() bool {
	return s.colorEnabled
}
