package console

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"bgin/internal/console/core"
)

const (
	scopeAdmin  = "admin"
	scopeClient = "client"
)

// MakeCRUDOptions 封装 make:crud 命令的参数与依赖容器 (SRP & DIP)。
type MakeCRUDOptions struct {
	Name    string
	Client  bool
	Force   bool
	DryRun  bool
	Delete  bool
	Tag     string
	Chinese string
	Factory Factory
}

// NewMakeCRUDOptions 构造 MakeCRUDOptions 实例。
func NewMakeCRUDOptions(f Factory) *MakeCRUDOptions {
	return &MakeCRUDOptions{Factory: f}
}

// Complete 从命令行参数与交互式环境中解析并补全配置。
func (o *MakeCRUDOptions) Complete(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		o.Name = strings.TrimSpace(args[0])
		return nil
	}

	streams := o.Factory.IOStreams()
	if streams.CanPrompt() {
		if o.Delete {
			return o.promptDestroy()
		}
		return o.promptCreate()
	}

	return nil
}

// Validate 校验命令入参合法性。
func (o *MakeCRUDOptions) Validate() error {
	if o.Name == "" {
		if o.Delete {
			return &ValidationError{Field: "name", Message: "accepts 1 arg(s), received 0. Usage: bgin make:crud <name> -d"}
		}
		return &ValidationError{Field: "name", Message: "accepts 1 arg(s), received 0. Usage: bgin make:crud <name>"}
	}
	return nil
}

// Run 调度执行 CRUD 模块生成或回退清理逻辑。
func (o *MakeCRUDOptions) Run(_ context.Context) error {
	if o.Delete {
		return o.runDelete()
	}
	return o.runCreate()
}

func (o *MakeCRUDOptions) runDelete() error {
	streams := o.Factory.IOStreams()

	if !o.Force && !o.DryRun {
		confirmed, err := o.Factory.Prompter().Confirm(
			fmt.Sprintf("Are you sure you want to destroy CRUD module [%s]?", o.Name),
			"All scaffolded files and the feature directory will be permanently deleted.",
			"Yes, destroy",
		)
		if err != nil {
			return err
		}
		if !confirmed {
			core.WarnTo(streams.Out, "Destroy operation cancelled.")
			return core.ErrCanceled
		}
	}

	opts := core.GeneratorOptions{
		Name:    o.Name,
		IsAdmin: !o.Client,
		Force:   o.Force,
		DryRun:  o.DryRun,
	}

	core.TitleTo(streams.Out, fmt.Sprintf("Destroying CRUD module: %s (Admin=%v, DryRun=%v)", o.Name, opts.IsAdmin, opts.DryRun))

	results, err := core.DestroyCRUD(opts)
	if err != nil {
		return err
	}

	var deleted, skipped, dryRuns int
	for _, r := range results {
		switch r.Action {
		case core.ActionDeleted:
			deleted++
			core.WarnTo(streams.Out, "  - %-12s %s", r.Action, r.Path)
		case core.ActionSkipped:
			skipped++
			core.InfoTo(streams.Out, "  ~ %-12s %s (%s)", r.Action, r.Path, r.Message)
		case core.ActionDryRun:
			dryRuns++
			core.InfoTo(streams.Out, "  ? %-12s %s (%s)", r.Action, r.Path, r.Message)
		}
	}

	core.InfoTo(streams.Out, "")
	if o.DryRun {
		core.SuccessTo(streams.Out, "Dry run completed: %d files would be deleted.", dryRuns)
		return nil
	}

	core.SuccessTo(streams.Out, "Scaffolding cleanup finished: %d deleted, %d skipped.", deleted, skipped)
	core.WarnTo(streams.Out, "\nReminder: If routes were registered in internal/bootstrap/engine.go, please remove them manually.")
	return nil
}

func (o *MakeCRUDOptions) runCreate() error {
	streams := o.Factory.IOStreams()

	opts := core.GeneratorOptions{
		Name:        o.Name,
		IsAdmin:     !o.Client,
		TagName:     o.Tag,
		ChineseName: o.Chinese,
		Force:       o.Force,
		DryRun:      o.DryRun,
	}

	core.TitleTo(streams.Out, fmt.Sprintf("Scaffolding CRUD module: %s (Admin=%v, DryRun=%v)", o.Name, opts.IsAdmin, opts.DryRun))

	results, err := core.GenerateCRUD(opts)
	if err != nil {
		return err
	}

	var created, overwritten, skipped, dryRuns int
	for _, r := range results {
		switch r.Action {
		case core.ActionCreated:
			created++
			core.SuccessTo(streams.Out, "  + %-12s %s", r.Action, r.Path)
		case core.ActionOverwritten:
			overwritten++
			core.WarnTo(streams.Out, "  ! %-12s %s", r.Action, r.Path)
		case core.ActionSkipped:
			skipped++
			core.InfoTo(streams.Out, "  - %-12s %s (%s)", r.Action, r.Path, r.Message)
		case core.ActionDryRun:
			dryRuns++
			core.InfoTo(streams.Out, "  ? %-12s %s (%s)", r.Action, r.Path, r.Message)
		}
	}

	core.InfoTo(streams.Out, "")
	if o.DryRun {
		core.SuccessTo(streams.Out, "Dry run completed: %d files planned.", dryRuns)
		return nil
	}

	core.SuccessTo(streams.Out, "Scaffolding finished: %d created, %d overwritten, %d skipped.", created, overwritten, skipped)

	core.InfoTo(streams.Out, "\nNext steps:")
	registerFn := "RegisterRoutes"
	if opts.IsAdmin {
		registerFn = "RegisterAdminRoutes"
	}
	core.InfoTo(streams.Out, "  1. Register routes in internal/bootstrap/engine.go:")
	data := core.BuildTemplateData(opts)
	core.InfoTo(streams.Out, "     %s.%s(v1, %sHandler, mw)", data.PackageName, registerFn, data.CamelName)
	core.InfoTo(streams.Out, "  2. Verify and format code:")
	core.InfoTo(streams.Out, "     rtk just check")
	core.InfoTo(streams.Out, "  3. Regenerate Swagger/OpenAPI documentation:")
	core.InfoTo(streams.Out, "     rtk just docs")

	return nil
}

func (o *MakeCRUDOptions) promptCreate() error {
	return o.promptForm(false)
}

func (o *MakeCRUDOptions) promptDestroy() error {
	return o.promptForm(true)
}

func (o *MakeCRUDOptions) promptForm(isDestroy bool) error {
	scope := scopeAdmin
	if o.Client {
		scope = scopeClient
	}

	nameTitle := "Module Name"
	nameDesc := "Singular or plural identifier (e.g. article, order)"
	if isDestroy {
		nameTitle = "Module Name to Destroy"
		nameDesc = "Singular or plural identifier to delete (e.g. article, order)"
	}

	fields := []huh.Field{
		huh.NewInput().
			Title(nameTitle).
			Description(nameDesc).
			Value(&o.Name).
			Validate(func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("module name cannot be empty")
				}
				return nil
			}),
		huh.NewSelect[string]().
			Title("Module Scope").
			Description("Target domain partition").
			Options(
				huh.NewOption("Admin (Management API)", scopeAdmin),
				huh.NewOption("Client (User API)", scopeClient),
			).
			Value(&scope),
	}

	if !isDestroy {
		fields = append(fields,
			huh.NewInput().
				Title("Chinese Name (Optional)").
				Description("Business entity name (e.g. 文章, 订单)").
				Value(&o.Chinese),
			huh.NewInput().
				Title("OpenAPI Tag Name (Optional)").
				Description("Documentation tag (e.g. 文章管理, 订单中心)").
				Value(&o.Tag),
		)
	}

	if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
		return err
	}

	if scope == scopeClient {
		o.Client = true
	}
	return nil
}

// newMakeCRUDCmd 构建 CRUD 模块生成与删除回退命令 (bgin make:crud <name> [-d])。
func newMakeCRUDCmd(f Factory) *cobra.Command {
	opts := NewMakeCRUDOptions(f)

	cmd := &cobra.Command{
		Use:   "make:crud [name]",
		Short: "Generate a complete CRUD feature slice (or delete with -d)",
		Long: `Generate or delete a complete production-grade CRUD feature slice following the Domain-First architecture.

Generates / Removes:
  - internal/features/[admin|client]/<name>/handler.go
  - internal/features/[admin|client]/<name>/routes.go
  - internal/features/[admin|client]/<name>/service.go
  - internal/features/[admin|client]/<name>/dto.go
  - internal/database/models/<name>.go
  - internal/features/[admin|client]/<name>/ (if directory is empty on delete)

Examples:
  bgin make:crud article
  bgin make:crud article_category --tag "分类管理" --chinese "文章分类"
  bgin make:crud post --client
  bgin make:crud product --force
  bgin make:crud order --dry-run
  bgin make:crud article -d
  bgin make:crud article --delete --force
  bgin make:crud order -d --dry-run`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("accepts at most 1 arg(s), received %d", len(args))
			}
			return nil
		},
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

	cmd.Flags().BoolVar(&opts.Client, "client", false, "Generate or delete in client namespace instead of admin")
	cmd.Flags().BoolVarP(&opts.Force, core.FlagForce, "f", false, "Force overwrite existing files or delete without confirmation")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Simulate operation without writing or deleting files")
	cmd.Flags().BoolVarP(&opts.Delete, "delete", "d", false, "Delete and clean up a generated CRUD feature slice instead of creating it")
	cmd.Flags().StringVar(&opts.Tag, "tag", "", "Custom Swagger tag name (creation only)")
	cmd.Flags().StringVar(&opts.Chinese, "chinese", "", "Custom Chinese business description (creation only)")

	return cmd
}
