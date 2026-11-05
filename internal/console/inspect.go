package console

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	bubbletable "charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	lipglosstable "charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/term"
	"github.com/dustin/go-humanize"
	"github.com/samber/do/v2"
	"github.com/spf13/cobra"

	"cgin/internal/bootstrap"
	"cgin/internal/config"
	adminuser "cgin/internal/feature/admin/user"
	"cgin/pkg/search"
)

// RouteEntry 描述解析提取后的单条 HTTP 路由信息。
type RouteEntry struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Handler    string `json:"handler"`
	RawHandler string `json:"rawHandler"`
}

// newRouteListCmd 构造列出所有已注册路由的命令。
func newRouteListCmd(f Factory) *cobra.Command {
	var (
		methodFilter string
		pathFilter   string
		sortField    = "path"
		reverse      bool
		jsonOutput   bool
		interactive  bool
	)

	cmd := &cobra.Command{
		Use:   "route:list",
		Short: "List all registered HTTP routes with methods and handlers",
		Args:  maxArgs(0),
		RunE: runE(func(_ context.Context, _ []string) error {
			sortCol := strings.ToLower(strings.TrimSpace(sortField))
			if sortCol != "" && sortCol != "path" && sortCol != "method" && sortCol != "handler" {
				return &ValidationError{Field: "sort", Message: "must be one of: path, method, handler"}
			}

			streams := f.IOStreams()
			cfg, cfgErr := f.Config()
			if cfgErr != nil || cfg == nil {
				streams.WarnErr("warning: load config failed, rendering routes with defaults: %v", cfgErr)
				cfg = config.Default()
			}

			routes, err := extractEngineRoutes(cfg, methodFilter, pathFilter)
			if err != nil {
				return err
			}
			sortRoutes(routes, sortCol, reverse)
			if jsonOutput {
				data, err := json.MarshalIndent(routes, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal routes to JSON: %w", err)
				}
				_, _ = fmt.Fprintln(streams.Out, string(data))
				return nil
			}
			if interactive {
				return runInteractiveRouteTable(streams, routes)
			}
			renderLipglossTable(streams, routes)
			return nil
		}),
	}

	cmd.Flags().StringVarP(&methodFilter, "method", "m", "", "Filter routes by HTTP method (GET, POST, etc.)")
	cmd.Flags().StringVarP(&pathFilter, "path", "p", "", "Filter routes by URI path pattern")
	cmd.Flags().StringVarP(&sortField, "sort", "s", "path", "Sort by column: path, method, handler")
	cmd.Flags().BoolVarP(&reverse, "reverse", "r", false, "Reverse route sorting order")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output route list as formatted JSON")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Explore routes interactively using bubbles/table TUI")
	return cmd
}

func extractEngineRoutes(cfg *config.Config, methodFilter, pathFilter string) ([]RouteEntry, error) {
	engine, err := bootstrap.InspectEngine(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect engine routes: %w", err)
	}

	rawRoutes := engine.Routes()
	routes := make([]RouteEntry, 0, len(rawRoutes))
	for _, r := range rawRoutes {
		if methodFilter != "" && !strings.EqualFold(r.Method, methodFilter) {
			continue
		}
		if pathFilter != "" && !strings.Contains(strings.ToLower(r.Path), strings.ToLower(pathFilter)) {
			continue
		}

		routes = append(routes, RouteEntry{
			Method:     r.Method,
			Path:       r.Path,
			Handler:    formatHandlerName(r.Handler),
			RawHandler: r.Handler,
		})
	}
	return routes, nil
}

func sortRoutes(routes []RouteEntry, sortField string, reverse bool) {
	slices.SortFunc(routes, func(a, b RouteEntry) int {
		var res int
		switch sortField {
		case "method":
			res = cmp.Or(strings.Compare(a.Method, b.Method), strings.Compare(a.Path, b.Path))
		case "handler":
			res = cmp.Or(strings.Compare(a.Handler, b.Handler), strings.Compare(a.Path, b.Path))
		default:
			res = cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Method, b.Method))
		}
		if reverse {
			return -res
		}
		return res
	})
}

func formatHandlerName(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "-"
	}
	s = strings.TrimSuffix(s, "-fm")
	s = strings.TrimPrefix(s, "cgin/")
	s = strings.TrimPrefix(s, "internal/")
	s = strings.ReplaceAll(s, ".(*", ".")
	s = strings.ReplaceAll(s, ").", ".")
	return s
}

func methodStyle(method string) lipgloss.Style {
	switch strings.ToUpper(method) {
	case "GET":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	case "POST":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	case "PUT":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	case "DELETE":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	case "PATCH":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("207"))
	case "HEAD", "OPTIONS":
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	default:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	}
}

func renderLipglossTable(streams *IOStreams, routes []RouteEntry) {
	if len(routes) == 0 {
		streams.Warn("No routes found matching current filter criteria.")
		return
	}

	streams.Title("HTTP Route List (%d routes registered)", len(routes))

	rows := make([][]string, len(routes))
	for i, r := range routes {
		rows[i] = []string{r.Method, r.Path, r.Handler}
	}

	headerCellStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	t := lipglosstable.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(borderStyle).
		Headers("METHOD", "URI", "HANDLER").
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == lipglosstable.HeaderRow {
				return headerCellStyle
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
	streams.Info("Total: %d routes", len(routes))
}

type routeTUIModel struct {
	table  bubbletable.Model
	routes []RouteEntry
}

func (m routeTUIModel) Init() tea.Cmd { return nil }

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

func runInteractiveRouteTable(streams *IOStreams, routes []RouteEntry) error {
	if !streams.CanPrompt() {
		streams.Warn("Interactive mode requires a TTY terminal, falling back to standard table.")
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

	p := tea.NewProgram(routeTUIModel{table: t, routes: routes}, tea.WithOutput(streams.Out))
	_, err := p.Run()
	return err
}

// newDITreeCmd 注册 di:tree 依赖内省命令。
func newDITreeCmd(f Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "di:tree",
		Aliases: []string{"di:list"},
		Short:   "Display registered services and instantiation status in the DI container",
		RunE: runE(func(_ context.Context, _ []string) error {
			injector := f.Injector()
			if injector == nil {
				return errors.New("injector is not available")
			}

			provided := injector.ListProvidedServices()
			invoked := injector.ListInvokedServices()

			invokedSet := make(map[string]struct{}, len(invoked))
			for _, s := range invoked {
				invokedSet[s.Service] = struct{}{}
			}

			streams := f.IOStreams()
			streams.Title("Dependency Injection Container (Scope: %s)", injector.Name())

			slices.SortFunc(provided, func(a, b do.ServiceDescription) int {
				return strings.Compare(a.Service, b.Service)
			})

			t := lipglosstable.New().
				Headers("Service", "Scope", "Status").
				BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("240")))

			for _, p := range provided {
				status := "lazy"
				statusStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
				if _, ok := invokedSet[p.Service]; ok {
					status = "invoked"
					statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
				}
				t.Row(p.Service, p.ScopeName, statusStyle.Render(status))
			}

			_, _ = fmt.Fprintln(streams.Out, t.Render())
			streams.Info("\nTotal services registered: %d (%d invoked, %d pending lazy)",
				len(provided), len(invoked), len(provided)-len(invoked))
			return nil
		}),
	}
}

func openSearchClient(f Factory) (search.Client, error) {
	cfg, err := f.Config()
	if err != nil {
		return nil, err
	}
	if !cfg.Search.Enabled() {
		return nil, errors.New("search engine is disabled (check SCOUT_DRIVER and MEILISEARCH_HOST)")
	}
	client, err := search.New(cfg.Search)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize search client: %w", err)
	}
	return client, nil
}

// newScoutImportCmd 将指定模型或全部可搜索模型的数据批量导入搜索引擎。
func newScoutImportCmd(f Factory) *cobra.Command {
	var (
		all       bool
		chunkSize = 100
	)

	cmd := &cobra.Command{
		Use:   "scout:import [model]",
		Short: "Import model records into the search index",
		Args:  maxArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return adminuser.Names(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: runE(func(ctx context.Context, args []string) error {
			model := firstArg(args)
			if !all && model == "" {
				return &ValidationError{Field: "model", Message: "please specify a model name or use --all"}
			}
			if chunkSize <= 0 {
				return &ValidationError{Field: "chunk", Message: "must be greater than 0"}
			}

			searchClient, err := openSearchClient(f)
			if err != nil {
				return err
			}

			db, err := f.DB(ctx)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			streams := f.IOStreams()
			catalog := adminuser.Catalog(db)

			var targets []search.ModelDescriptor
			if all {
				targets = catalog.All()
				if len(targets) == 0 {
					streams.Info("No searchable models registered.")
					return nil
				}
			} else {
				desc, ok := catalog.Lookup(model)
				if !ok {
					return fmt.Errorf("model [%s] is not registered as searchable (available: %s)", model, strings.Join(adminuser.Names(), ", "))
				}
				targets = []search.ModelDescriptor{desc}
			}

			for _, target := range targets {
				if err := importSearchTarget(ctx, streams, searchClient, target, chunkSize); err != nil {
					return err
				}
			}
			return nil
		}),
	}

	cmd.Flags().BoolVarP(&all, "all", "a", false, "Import all registered searchable models")
	cmd.Flags().IntVarP(&chunkSize, "chunk", "c", 100, "Number of records to process per batch")
	return cmd
}

// newScoutFlushCmd 清空指定可搜索模型的全文检索索引文档。
func newScoutFlushCmd(f Factory) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "scout:flush <model>",
		Short: "Flush all of the model's records from the search index",
		Args:  maxArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return adminuser.Names(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: runE(func(ctx context.Context, args []string) error {
			model := firstArg(args)
			if model == "" {
				return &ValidationError{Field: "model", Message: "please provide a model name to flush"}
			}

			searchClient, err := openSearchClient(f)
			if err != nil {
				return err
			}

			desc, ok := adminuser.Catalog(nil).Lookup(model)
			if !ok {
				return fmt.Errorf("model [%s] is not registered as searchable (available: %s)", model, strings.Join(adminuser.Names(), ", "))
			}

			if err := confirmOrCancel(
				f,
				force,
				fmt.Sprintf("Are you sure you want to flush all records from index [%s]?", desc.IndexName),
				"All documents in this search index will be deleted.",
				"Yes, flush",
				"Scout flush operation cancelled.",
			); err != nil {
				return err
			}

			if err := searchClient.Index(desc.IndexName).DeleteAllDocuments(ctx); err != nil {
				return fmt.Errorf("failed to flush records from index [%s]: %w", desc.IndexName, err)
			}

			f.IOStreams().Success("All records for model [%s] flushed from index [%s].", desc.Name, desc.IndexName)
			return nil
		}),
	}

	cmd.Flags().BoolVarP(&force, FlagForce, "f", false, UsageForceFlag)
	return cmd
}

func importSearchTarget(ctx context.Context, streams *IOStreams, searchClient search.Client, target search.ModelDescriptor, chunkSize int) error {
	streams.Title("Importing [%s] records into search index [%s]...", target.Name, target.IndexName)
	idx := searchClient.Index(target.IndexName)

	var bar *ProgressBar
	if target.Counter != nil {
		if total, err := target.Counter(ctx); err == nil && total > 0 {
			bar = NewProgressBarTo(streams.Out, streams.CanPrompt(), total, target.Name)
		}
	}

	count, err := target.Importer(ctx, chunkSize, func(docs []map[string]any) error {
		if err := idx.AddDocuments(ctx, docs); err != nil {
			return err
		}
		if bar != nil {
			bar.Incr(int64(len(docs)))
		}
		return nil
	})
	if bar != nil {
		bar.Finish()
	}
	if err != nil {
		return fmt.Errorf("failed to import model [%s]: %w", target.Name, err)
	}

	streams.Success("Successfully imported [%s] records for model [%s].", humanize.Comma(count), target.Name)
	return nil
}
