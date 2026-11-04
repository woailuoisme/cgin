package console

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

// 通用 CLI 选项常量。
const (
	FlagForce      = "force"
	UsageForceFlag = "Skip interactive confirmation prompt"
)

// 控制台输出样式与帮助高亮样式。
var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	cmdNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	cmdPathStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	flagStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	argStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	typeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// IOStreams 统筹终端的标准输入、输出与错误流，并提供 TTY 能力判定、色彩控制及语义化输出。
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer

	stdinIsTTY   bool
	stdoutIsTTY  bool
	stderrIsTTY  bool
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
		colorEnabled: detectColor(stdoutTTY, stderrTTY),
	}
}

// TestIOStreams 创建完全基于内存缓冲区的测试用 IOStreams，便于捕获和断言命令输出。
func TestIOStreams() (*IOStreams, *bytes.Buffer, *bytes.Buffer, *bytes.Buffer) {
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return &IOStreams{
		In:     in,
		Out:    out,
		ErrOut: errOut,
	}, in, out, errOut
}

func detectColor(stdoutTTY, stderrTTY bool) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" || os.Getenv("CLICOLOR_FORCE") != "" {
		return true
	}
	return stdoutTTY || stderrTTY
}

func detectSystemColor() bool {
	return detectColor(term.IsTerminal(os.Stdout.Fd()), term.IsTerminal(os.Stderr.Fd()))
}

func orStdout(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}

// CanPrompt 返回当前环境是否具备完整的人机交互 TTY 能力。
func (s *IOStreams) CanPrompt() bool {
	return s != nil && s.stdinIsTTY && s.stdoutIsTTY
}

// SetColorEnabled 显式启用或禁用输出色彩。
func (s *IOStreams) SetColorEnabled(enabled bool) {
	if s != nil {
		s.colorEnabled = enabled
	}
}

// ColorEnabled 返回当前色彩是否启用。
func (s *IOStreams) ColorEnabled() bool {
	return s != nil && s.colorEnabled
}

func (s *IOStreams) outWriter() io.Writer {
	if s == nil {
		return os.Stdout
	}
	return orStdout(s.Out)
}

func (s *IOStreams) errWriter() io.Writer {
	if s == nil {
		return os.Stderr
	}
	if s.ErrOut != nil {
		return s.ErrOut
	}
	return orStdout(s.Out)
}

// Title 渲染并向标准输出打印一行小节标题。
func (s *IOStreams) Title(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintln(s.outWriter(), titleStyle.Render("• "+msg))
}

// Success 渲染并向标准输出打印一行成功提示。
func (s *IOStreams) Success(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintln(s.outWriter(), successStyle.Render("✓ "+msg))
}

// Warn 渲染并向标准输出打印一行告警提示。
func (s *IOStreams) Warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintln(s.outWriter(), warnStyle.Render("! "+msg))
}

// WarnErr 渲染并向标准错误流打印一行告警提示。
func (s *IOStreams) WarnErr(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintln(s.errWriter(), warnStyle.Render("! "+msg))
}

// Info 向标准输出打印一行常规信息。
func (s *IOStreams) Info(format string, args ...any) {
	_, _ = fmt.Fprintln(s.outWriter(), fmt.Sprintf(format, args...))
}

// Prompter 抽象控制台的人机交互提问契约。
type Prompter interface {
	Confirm(title, description string, affirmative ...string) (bool, error)
	Input(title, description, defaultValue string) (string, error)
	Select(title, description string, options []string, defaultOption string) (string, error)
}

// HuhPrompter 基于 charm.land/huh 的终端表单交互实现。
type HuhPrompter struct {
	canPrompt func() bool
}

// NewHuhPrompter 构造终端表单交互实现。
func NewHuhPrompter(canPrompt func() bool) *HuhPrompter {
	if canPrompt == nil {
		canPrompt = func() bool { return false }
	}
	return &HuhPrompter{canPrompt: canPrompt}
}

// Confirm 弹出交互式确认框。非交互环境下要求显式传入 --force 或返回错误。
func (p *HuhPrompter) Confirm(title, description string, affirmative ...string) (bool, error) {
	if !p.canPrompt() {
		return false, errors.New("confirmation required for destructive operation: please run interactively or specify --force")
	}

	aff := "Yes, continue"
	if len(affirmative) > 0 && affirmative[0] != "" {
		aff = affirmative[0]
	}

	var confirmed bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(title).
			Description(description).
			Affirmative(aff).
			Negative("Cancel").
			Value(&confirmed),
	))

	if err := form.Run(); err != nil {
		return false, fmt.Errorf("failed to read confirmation: %w", err)
	}
	return confirmed, nil
}

// Input 弹出文本输入框。
func (p *HuhPrompter) Input(title, description, defaultValue string) (string, error) {
	if !p.canPrompt() {
		return defaultValue, nil
	}

	val := defaultValue
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title(title).
			Description(description).
			Value(&val),
	))

	if err := form.Run(); err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	return val, nil
}

// Select 弹出单选列表。
func (p *HuhPrompter) Select(title, description string, options []string, defaultOption string) (string, error) {
	if !p.canPrompt() {
		return defaultOption, nil
	}

	huhOptions := make([]huh.Option[string], 0, len(options))
	for _, opt := range options {
		huhOptions = append(huhOptions, huh.NewOption(opt, opt))
	}

	selected := defaultOption
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title(title).
			Description(description).
			Options(huhOptions...).
			Value(&selected),
	))

	if err := form.Run(); err != nil {
		return "", fmt.Errorf("failed to read selection: %w", err)
	}
	return selected, nil
}

var (
	colorEnabled      atomic.Bool
	templateFuncsOnce sync.Once
	flagLineRegex     = regexp.MustCompile(`^(\s+)(-[a-zA-Z0-9], --[a-zA-Z0-9_-]+|--[a-zA-Z0-9_-]+|-[a-zA-Z0-9])(?: ([^\s]+))?(\s{2,})(.*)$`)
)

func registerTemplateFuncs() {
	templateFuncsOnce.Do(func() {
		cobra.AddTemplateFunc("styleHeader", StyleHeader)
		cobra.AddTemplateFunc("styleCmd", StyleCmd)
		cobra.AddTemplateFunc("styleUseLine", StyleUseLine)
		cobra.AddTemplateFunc("styleFlags", StyleFlags)
		cobra.AddTemplateFunc("styleAliases", StyleAliases)
		cobra.AddTemplateFunc("styleExample", StyleExample)
	})
}

// SetColorEnabled 显式设定帮助输出的色彩开关（并发安全）。
func SetColorEnabled(enabled bool) { colorEnabled.Store(enabled) }

// IsColorEnabled 返回帮助输出是否启用彩色高亮。
func IsColorEnabled() bool { return colorEnabled.Load() }

// StyleHeader 为命令帮助的各个小节标题添加加粗暖黄高亮。
func StyleHeader(title string) string {
	if !IsColorEnabled() {
		return title
	}
	return headerStyle.Render(title)
}

// StyleCmd 为子命令名称添加亮绿高亮，并补齐右侧空格确保描述列严格对齐。
func StyleCmd(name string, pad int) string {
	text := name
	if IsColorEnabled() {
		text = cmdNameStyle.Render(name)
	}
	if extra := pad - lipgloss.Width(name); extra > 0 {
		return text + strings.Repeat(" ", extra)
	}
	return text
}

// StyleUseLine 为命令 Usage 行添加语法高亮。
func StyleUseLine(line string) string {
	if !IsColorEnabled() || strings.TrimSpace(line) == "" {
		return line
	}
	parts := strings.Fields(line)
	styledParts := make([]string, 0, len(parts))
	for _, part := range parts {
		switch {
		case strings.HasPrefix(part, "[") || strings.HasPrefix(part, "<"):
			styledParts = append(styledParts, argStyle.Render(part))
		case strings.HasPrefix(part, "-"):
			styledParts = append(styledParts, flagStyle.Render(part))
		default:
			styledParts = append(styledParts, cmdPathStyle.Render(part))
		}
	}
	return strings.Join(styledParts, " ")
}

// StyleFlags 为 pflag FlagUsages 文本中的选项开关与参数类型添加彩色高亮，同时保持绝对列对齐。
func StyleFlags(usages string) string {
	if !IsColorEnabled() || strings.TrimSpace(usages) == "" {
		return usages
	}

	lines := strings.Split(usages, "\n")
	styledLines := make([]string, 0, len(lines))
	for _, line := range lines {
		matches := flagLineRegex.FindStringSubmatch(line)
		if len(matches) != 6 {
			styledLines = append(styledLines, line)
			continue
		}
		leading := matches[1]
		flagPart := flagStyle.Render(matches[2])
		typePart := ""
		if matches[3] != "" {
			typePart = " " + typeStyle.Render(matches[3])
		}
		styledLines = append(styledLines, leading+flagPart+typePart+matches[4]+matches[5])
	}
	return strings.Join(styledLines, "\n")
}

// StyleAliases 为别名列表中的命令名添加彩色高亮。
func StyleAliases(aliases string) string {
	if !IsColorEnabled() || aliases == "" {
		return aliases
	}
	parts := strings.Split(aliases, ", ")
	styled := make([]string, 0, len(parts))
	for _, p := range parts {
		styled = append(styled, cmdNameStyle.Render(p))
	}
	return strings.Join(styled, ", ")
}

// StyleExample 为示例代码行添加语法高亮。
func StyleExample(example string) string {
	if !IsColorEnabled() || strings.TrimSpace(example) == "" {
		return example
	}
	lines := strings.Split(example, "\n")
	styledLines := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		indent := line[:len(line)-len(trimmed)]
		if trimmed == "" {
			styledLines = append(styledLines, line)
			continue
		}
		styledLines = append(styledLines, indent+StyleUseLine(trimmed))
	}
	return strings.Join(styledLines, "\n")
}

const coloredUsageTemplate = `{{styleHeader "Usage:"}}{{if .Runnable}}
  {{styleUseLine .UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{styleUseLine (printf "%s [command]" .CommandPath)}}{{end}}{{if gt (len .Aliases) 0}}

{{styleHeader "Aliases:"}}
  {{styleAliases .NameAndAliases}}{{end}}{{if .HasExample}}

{{styleHeader "Examples:"}}
{{styleExample .Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

{{styleHeader "Available commands:"}}{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{styleCmd .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}

{{styleHeader "Available commands:"}}{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{styleCmd .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{range $group := .Groups}}

{{styleHeader .Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{styleCmd .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

{{styleHeader "Flags:"}}
{{.LocalFlags.FlagUsages | styleFlags | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

{{styleHeader "Global Flags:"}}
{{.InheritedFlags.FlagUsages | styleFlags | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

{{styleHeader "Additional help topics:"}}{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{styleCmd .CommandPath .CommandPathPadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{styleUseLine (printf "%s [command] --help" .CommandPath)}}" for more information about a command.{{end}}
`

// SetupColoredHelp 配置带色彩与列对齐的帮助模板。
func SetupColoredHelp(cmd *cobra.Command, streams *IOStreams) {
	if cmd == nil {
		return
	}
	registerTemplateFuncs()
	if streams != nil {
		SetColorEnabled(streams.ColorEnabled())
	} else {
		SetColorEnabled(detectSystemColor())
	}
	cmd.SetUsageTemplate(coloredUsageTemplate)
}
