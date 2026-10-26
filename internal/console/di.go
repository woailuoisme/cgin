package console

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	lipglosstable "charm.land/lipgloss/v2/table"
	"github.com/samber/do/v2"
	"github.com/spf13/cobra"

	"cgin/internal/console/core"
)

// DITreeOptions 封装 di:tree 命令的参数与依赖容器。
type DITreeOptions struct {
	noopComplete
	noopValidate

	Factory Factory
}

// NewDITreeOptions 构造 DITreeOptions 实例。
func NewDITreeOptions(f Factory) *DITreeOptions {
	return &DITreeOptions{Factory: f}
}

// Run 执行依赖容器内省与状态打印。
func (o *DITreeOptions) Run(_ context.Context) error {
	injector := o.Factory.Injector()
	if injector == nil {
		return errors.New("injector is not available")
	}

	provided := injector.ListProvidedServices()
	invoked := injector.ListInvokedServices()

	invokedSet := make(map[string]struct{}, len(invoked))
	for _, s := range invoked {
		invokedSet[s.Service] = struct{}{}
	}

	out := o.Factory.IOStreams().Out
	core.TitleTo(out, fmt.Sprintf("Dependency Injection Container (Scope: %s)", injector.Name()))

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

	_, _ = fmt.Fprintln(out, t.Render())
	core.InfoTo(out, "\nTotal services registered: %d (%d invoked, %d pending lazy)",
		len(provided), len(invoked), len(provided)-len(invoked))

	return nil
}

// newDITreeCmd 注册 di:tree 依赖内省命令。
func newDITreeCmd(f Factory) *cobra.Command {
	opts := NewDITreeOptions(f)
	return &cobra.Command{
		Use:     "di:tree",
		Aliases: []string{"di:list"},
		Short:   "Display registered services and instantiation status in the DI container",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return opts.Run(cmd.Context())
		},
	}
}
