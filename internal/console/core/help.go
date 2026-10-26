package core

import (
	"regexp"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
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
	// colorEnabled 是进程级色彩开关，由 SetupColoredHelp 依据注入的 IOStreams 单点设定。
	colorEnabled bool
	// templateFuncsOnce 保证 cobra 模板函数全局只注册一次。
	templateFuncsOnce sync.Once
	flagLineRegex     = regexp.MustCompile(`^(\s+)(-[a-zA-Z0-9], --[a-zA-Z0-9_-]+|--[a-zA-Z0-9_-]+|-[a-zA-Z0-9])(?: ([^\s]+))?(\s{2,})(.*)$`)
)

// registerTemplateFuncs 幂等注入帮助模板所需的样式函数。
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

// SetColorEnabled 显式设定帮助输出的色彩开关。
func SetColorEnabled(enabled bool) {
	colorEnabled = enabled
}

// IsColorEnabled 返回帮助输出是否启用彩色高亮。
func IsColorEnabled() bool {
	return colorEnabled
}

// StyleHeader 为命令帮助的各个小节标题添加加粗暖黄高亮。
func StyleHeader(title string) string {
	if !IsColorEnabled() {
		return title
	}
	return headerStyle.Render(title)
}

// StyleCmd 为子命令名称添加亮绿高亮，并补齐右侧空格确保描述列严格对齐。
// ANSI 渲染后不能再按字节长度对齐，需按可见宽度手工补空格。
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

// SetupColoredHelp 配置带色彩与列对齐的帮助模板；色彩能力取自注入的 IOStreams，
// 未注入时回退为操作系统标准流探测。
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
