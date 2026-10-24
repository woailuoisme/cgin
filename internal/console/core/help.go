package core

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

// 控制台帮助与用法信息的高亮样式。
var (
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")) // 暖黄加粗小节标题
	cmdNameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))             // 亮绿命令名
	cmdPathStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))  // 亮绿加粗命令路径
	flagStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))             // 亮绿选项标志
	argStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))             // 亮青参数占位符
	typeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))            // 暗灰参数类型
)

var (
	colorOverride *bool
	flagLineRegex = regexp.MustCompile(`^(\s+)(-[a-zA-Z0-9], --[a-zA-Z0-9_-]+|--[a-zA-Z0-9_-]+|-[a-zA-Z0-9])(?: ([^\s]+))?(\s{2,})(.*)$`)
)

func init() {
	registerTemplateFuncs()
}

func registerTemplateFuncs() {
	cobra.AddTemplateFunc("styleHeader", StyleHeader)
	cobra.AddTemplateFunc("styleCmd", StyleCmd)
	cobra.AddTemplateFunc("styleUseLine", StyleUseLine)
	cobra.AddTemplateFunc("styleFlags", StyleFlags)
	cobra.AddTemplateFunc("styleAliases", StyleAliases)
	cobra.AddTemplateFunc("styleExample", StyleExample)
}

// SetColorEnabled 供测试或特殊场景显式覆盖颜色开关。
func SetColorEnabled(enabled bool) {
	colorOverride = &enabled
}

// ResetColorOverride 重置颜色覆盖状态。
func ResetColorOverride() {
	colorOverride = nil
}

// IsColorEnabled 检测当前控制台环境是否应启用彩色高亮输出。
func IsColorEnabled() bool {
	if colorOverride != nil {
		return *colorOverride
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" || os.Getenv("CLICOLOR_FORCE") != "" {
		return true
	}
	return term.IsTerminal(os.Stdout.Fd()) || term.IsTerminal(os.Stderr.Fd())
}

// StyleHeader 为命令帮助的各个小节标题添加加粗暖黄高亮。
func StyleHeader(title string) string {
	if !IsColorEnabled() {
		return title
	}
	return headerStyle.Render(title)
}

// StyleCmd 为子命令名称添加亮绿高亮，并补齐右侧空格以确保描述列严格对齐。
func StyleCmd(name string, pad int) string {
	if !IsColorEnabled() {
		return rpad(name, pad)
	}
	styled := cmdNameStyle.Render(name)
	w := lipgloss.Width(name)
	if pad > w {
		return styled + strings.Repeat(" ", pad-w)
	}
	return styled
}

func rpad(s string, padding int) string {
	template := fmt.Sprintf("%%-%ds", padding)
	return fmt.Sprintf(template, s)
}

// StyleUseLine 为命令 Usage 行添加语法高亮（程序与子命令路径、参数占位符、选项开关）。
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
		padding := matches[4]
		desc := matches[5]

		styledLines = append(styledLines, leading+flagPart+typePart+padding+desc)
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

// SetupColoredHelp 为目标命令（通常是根命令）配置色彩丰富且对齐完美的终端帮助模板。
func SetupColoredHelp(cmd *cobra.Command) {
	if cmd == nil {
		return
	}
	registerTemplateFuncs()
	cmd.SetUsageTemplate(coloredUsageTemplate)
}
