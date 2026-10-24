package console

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	bubbletable "charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	lipglosstable "charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"bgin/internal/bootstrap"
	"bgin/internal/config"
	"bgin/internal/console/core"
)

// RouteEntry 描述解析提取后的单条 HTTP 路由信息。
type RouteEntry struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Handler    string `json:"handler"`
	RawHandler string `json:"rawHandler"`
}

// RouteListOptions 封装 route:list 命令的参数与依赖容器 (SRP & DIP)。
type RouteListOptions struct {
	MethodFilter string
	PathFilter   string
	SortField    string
	Reverse      bool
	JSONOutput   bool
	Interactive  bool
	Factory      Factory
}

// NewRouteListOptions 构造 RouteListOptions 实例。
func NewRouteListOptions(f Factory) *RouteListOptions {
	return &RouteListOptions{
		SortField: "path",
		Factory:   f,
	}
}

// Complete 从命令行上下文解析并补全配置。
func (o *RouteListOptions) Complete(_ *cobra.Command, _ []string) error {
	return nil
}

// Validate 校验路由查询与排序选项。
func (o *RouteListOptions) Validate() error {
	sort := strings.ToLower(strings.TrimSpace(o.SortField))
	if sort != "" && sort != "path" && sort != "method" && sort != "handler" {
		return &ValidationError{Field: "sort", Message: "must be one of: path, method, handler"}
	}
	return nil
}

// Run 执行路由列表提取与表格渲染。
func (o *RouteListOptions) Run(_ context.Context) error {
	streams := o.Factory.IOStreams()

	cfg, cfgErr := o.Factory.Config()
	if cfgErr != nil || cfg == nil {
		_, _ = fmt.Fprintf(streams.ErrOut, "warning: load config failed, rendering routes with defaults: %v\n", cfgErr)
		cfg = config.Default()
	}

	engine, err := bootstrap.InspectEngine(cfg)
	if err != nil {
		return fmt.Errorf("failed to inspect engine routes: %w", err)
	}

	rawRoutes := engine.Routes()
	routes := make([]RouteEntry, 0, len(rawRoutes))
	for _, r := range rawRoutes {
		if o.MethodFilter != "" && !strings.EqualFold(r.Method, o.MethodFilter) {
			continue
		}
		if o.PathFilter != "" && !strings.Contains(strings.ToLower(r.Path), strings.ToLower(o.PathFilter)) {
			continue
		}

		routes = append(routes, RouteEntry{
			Method:     r.Method,
			Path:       r.Path,
			Handler:    formatHandlerName(r.Handler),
			RawHandler: r.Handler,
		})
	}

	slices.SortFunc(routes, func(a, b RouteEntry) int {
		var res int
		switch strings.ToLower(o.SortField) {
		case "method":
			res = cmp.Or(strings.Compare(a.Method, b.Method), strings.Compare(a.Path, b.Path))
		case "handler":
			res = cmp.Or(strings.Compare(a.Handler, b.Handler), strings.Compare(a.Path, b.Path))
		default: // "path"
			res = cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Method, b.Method))
		}
		if o.Reverse {
			return -res
		}
		return res
	})

	if o.JSONOutput {
		data, err := json.MarshalIndent(routes, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal routes to JSON: %w", err)
		}
		_, _ = fmt.Fprintln(streams.Out, string(data))
		return nil
	}

	if o.Interactive {
		return runInteractiveRouteTable(streams, routes)
	}

	renderLipglossTable(streams, routes)
	return nil
}

// newRouteListCmd 构造列出所有已注册路由的命令。
func newRouteListCmd(f Factory) *cobra.Command {
	opts := NewRouteListOptions(f)

	cmd := &cobra.Command{
		Use:   "route:list",
		Short: "List all registered HTTP routes with methods and handlers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Complete(cmd, args); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return opts.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVarP(&opts.MethodFilter, "method", "m", "", "Filter routes by HTTP method (GET, POST, etc.)")
	cmd.Flags().StringVarP(&opts.PathFilter, "path", "p", "", "Filter routes by URI path pattern")
	cmd.Flags().StringVarP(&opts.SortField, "sort", "s", "path", "Sort by column: path, method, handler")
	cmd.Flags().BoolVarP(&opts.Reverse, "reverse", "r", false, "Reverse route sorting order")
	cmd.Flags().BoolVar(&opts.JSONOutput, "json", false, "Output route list as formatted JSON")
	cmd.Flags().BoolVarP(&opts.Interactive, "interactive", "i", false, "Explore routes interactively using bubbles/table TUI")

	return cmd
}

// formatHandlerName 将冗长的运行时函数路径精简为易读的结构体方法名。
func formatHandlerName(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "-"
	}
	s = strings.TrimSuffix(s, "-fm")
	s = strings.TrimPrefix(s, "bgin/internal/features/")
	s = strings.TrimPrefix(s, "bgin/internal/")
	s = strings.TrimPrefix(s, "bgin/")
	s = strings.ReplaceAll(s, ".(*Handler).", ".Handler.")
	return s
}

// methodStyle 根据 HTTP Method 返回带有语义化强调色的 Lipgloss Style。
func methodStyle(method string) lipgloss.Style {
	switch strings.ToUpper(method) {
	case "GET":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")) // Green
	case "POST":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")) // Yellow
	case "PUT":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")) // Blue/Cyan
	case "DELETE":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")) // Red
	case "PATCH":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("207")) // Magenta
	case "HEAD", "OPTIONS":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244")) // Grey
	default:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	}
}

// renderLipglossTable 采用 lipgloss/table 渲染标准 ANSI 静态彩色表格。
func renderLipglossTable(streams *core.IOStreams, routes []RouteEntry) {
	if len(routes) == 0 {
		core.WarnTo(streams.Out, "No routes found matching current filter criteria.")
		return
	}

	core.TitleTo(streams.Out, fmt.Sprintf("HTTP Route List (%d routes registered)", len(routes)))

	rows := make([][]string, len(routes))
	for i, r := range routes {
		rows[i] = []string{r.Method, r.Path, r.Handler}
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("39"))

	borderStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))

	t := lipglosstable.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		Headers("METHOD", "URI", "HANDLER").
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == lipglosstable.HeaderRow {
				return headerStyle
			}
			if col == 0 {
				return methodStyle(rows[row][0])
			}
			return lipgloss.NewStyle()
		})

	termWidth := 120
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 40 {
		termWidth = w
	}
	t.Width(termWidth)

	_, _ = fmt.Fprintln(streams.Out, t.Render())
	core.InfoTo(streams.Out, "Total: %d routes", len(routes))
}

// routeTUIModel 是基于 bubbles/table 的全屏交互式 Bubble Tea 模型。
type routeTUIModel struct {
	table  bubbletable.Model
	routes []RouteEntry
}

func (m routeTUIModel) Init() tea.Cmd {
	return nil
}

func (m routeTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.table.SetWidth(msg.Width)
		m.table.SetHeight(max(10, msg.Height-5))
	}
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m routeTUIModel) View() tea.View {
	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	help := helpStyle.Render(fmt.Sprintf("  [Total: %d]  ↑/k: Up • ↓/j: Down • q/esc: Quit", len(m.routes)))
	v := tea.NewView(m.table.View() + "\n" + help)
	v.AltScreen = true
	return v
}

// runInteractiveRouteTable 唤起全屏 bubbles/table 交互式浏览窗口。
func runInteractiveRouteTable(streams *core.IOStreams, routes []RouteEntry) error {
	if !streams.CanPrompt() {
		core.WarnTo(streams.Out, "Interactive mode requires a TTY terminal, falling back to standard table.")
		renderLipglossTable(streams, routes)
		return nil
	}

	columns := []bubbletable.Column{
		{Title: "METHOD", Width: 10},
		{Title: "URI", Width: 35},
		{Title: "HANDLER", Width: 45},
	}

	rows := make([]bubbletable.Row, len(routes))
	for i, r := range routes {
		rows[i] = bubbletable.Row{r.Method, r.Path, r.Handler}
	}

	styles := bubbletable.DefaultStyles()
	styles.Header = styles.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(true).
		Foreground(lipgloss.Color("39"))
	styles.Selected = styles.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(false)

	t := bubbletable.New(
		bubbletable.WithColumns(columns),
		bubbletable.WithRows(rows),
		bubbletable.WithFocused(true),
		bubbletable.WithHeight(20),
		bubbletable.WithStyles(styles),
	)

	m := routeTUIModel{table: t, routes: routes}
	p := tea.NewProgram(m, tea.WithOutput(streams.Out))
	_, err := p.Run()
	return err
}
