package console

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"cgin/internal/console/scaffold"
)

const (
	scopeAdmin  = "admin"
	scopeClient = "client"
)

type makeCRUDRunner struct {
	f       Factory
	name    string
	client  bool
	force   bool
	dryRun  bool
	del     bool
	tag     string
	chinese string
}

// newMakeCRUDCmd 构建 CRUD 模块生成与删除回退命令 (cfiber make:crud <name> [-d])。
func newMakeCRUDCmd(f Factory) *cobra.Command {
	r := &makeCRUDRunner{f: f}

	cmd := &cobra.Command{
		Use:   "make:crud [name]",
		Short: "Generate a complete CRUD module across layers (or delete with -d)",
		Long: `Generate or delete a complete production-grade CRUD feature slice following the Layer-First architecture (route → handler → service → dto).

Generates / Removes (admin scope lands in each layer's admin/ subdirectory):
  - internal/handler/<module>.go
  - internal/routes/<module>.go
  - internal/service/<module>.go
  - internal/dto/<module>.go
  - database/schemas/<plural>.sql + database/queries/<plural>.sql

Deletion requires a compilable module: run 'just sqlc' after creating a module
before using -d, otherwise the CLI itself cannot be built from source.

Examples:
  cgin make:crud article
  cgin make:crud article_category --tag "分类管理" --chinese "文章分类"
  cgin make:crud post --client
  cgin make:crud product --force
  cgin make:crud order --dry-run
  cgin make:crud article -d
  cgin make:crud article --delete --force
  cgin make:crud order -d --dry-run`,
		Args: maxArgs(1),
		RunE: runE(func(_ context.Context, args []string) error {
			if name := firstArg(args); name != "" {
				r.name = name
			} else if f.IOStreams().CanPrompt() {
				if err := r.promptForm(); err != nil {
					return err
				}
			}

			if r.name == "" {
				if r.del {
					return &ValidationError{Field: "name", Message: "accepts 1 arg(s), received 0. Usage: cgin make:crud <name> -d"}
				}
				return &ValidationError{Field: "name", Message: "accepts 1 arg(s), received 0. Usage: cgin make:crud <name>"}
			}

			if r.del {
				return r.runDelete()
			}
			return r.runCreate()
		}),
	}

	cmd.Flags().BoolVar(&r.client, "client", false, "Generate or delete in client namespace instead of admin")
	cmd.Flags().BoolVarP(&r.force, FlagForce, "f", false, "Force overwrite existing files or delete without confirmation")
	cmd.Flags().BoolVar(&r.dryRun, "dry-run", false, "Simulate operation without writing or deleting files")
	cmd.Flags().BoolVarP(&r.del, "delete", "d", false, "Delete and clean up a generated CRUD feature slice instead of creating it")
	cmd.Flags().StringVar(&r.tag, "tag", "", "Custom Swagger tag name (creation only)")
	cmd.Flags().StringVar(&r.chinese, "chinese", "", "Custom Chinese business description (creation only)")
	return cmd
}

func (r *makeCRUDRunner) runDelete() error {
	if !r.dryRun {
		if err := confirmOrCancel(
			r.f,
			r.force,
			fmt.Sprintf("Are you sure you want to destroy CRUD module [%s]?", r.name),
			"All scaffolded files and the feature directory will be permanently deleted.",
			"Yes, destroy",
			"Destroy operation cancelled.",
		); err != nil {
			return err
		}
	}

	opts := scaffold.GeneratorOptions{
		Name:    r.name,
		IsAdmin: !r.client,
		Force:   r.force,
		DryRun:  r.dryRun,
	}

	streams := r.f.IOStreams()
	streams.Title("Destroying CRUD module: %s (Admin=%v, DryRun=%v)", r.name, opts.IsAdmin, opts.DryRun)

	results, err := scaffold.DestroyCRUD(opts)
	if err != nil {
		return err
	}

	var deleted, skipped, dryRuns int
	for _, res := range results {
		switch res.Action {
		case scaffold.ActionDeleted:
			deleted++
			streams.Warn("  - %-12s %s", res.Action, res.Path)
		case scaffold.ActionSkipped:
			skipped++
			streams.Info("  ~ %-12s %s (%s)", res.Action, res.Path, res.Message)
		case scaffold.ActionDryRun:
			dryRuns++
			streams.Info("  ? %-12s %s (%s)", res.Action, res.Path, res.Message)
		}
	}

	streams.Info("")
	if r.dryRun {
		streams.Success("Dry run completed: %d files would be deleted.", dryRuns)
		return nil
	}

	streams.Success("Scaffolding cleanup finished: %d deleted, %d skipped.", deleted, skipped)
	streams.Warn("\nReminder: If routes were registered in internal/bootstrap/engine.go, please remove them manually.")
	return nil
}

func (r *makeCRUDRunner) runCreate() error {
	opts := scaffold.GeneratorOptions{
		Name:        r.name,
		IsAdmin:     !r.client,
		TagName:     r.tag,
		ChineseName: r.chinese,
		Force:       r.force,
		DryRun:      r.dryRun,
	}

	streams := r.f.IOStreams()
	streams.Title("Scaffolding CRUD module: %s (Admin=%v, DryRun=%v)", r.name, opts.IsAdmin, opts.DryRun)

	results, err := scaffold.GenerateCRUD(opts)
	if err != nil {
		return err
	}

	var created, overwritten, skipped, dryRuns int
	for _, res := range results {
		switch res.Action {
		case scaffold.ActionCreated:
			created++
			streams.Success("  + %-12s %s", res.Action, res.Path)
		case scaffold.ActionOverwritten:
			overwritten++
			streams.Warn("  ! %-12s %s", res.Action, res.Path)
		case scaffold.ActionSkipped:
			skipped++
			streams.Info("  - %-12s %s (%s)", res.Action, res.Path, res.Message)
		case scaffold.ActionDryRun:
			dryRuns++
			streams.Info("  ? %-12s %s (%s)", res.Action, res.Path, res.Message)
		}
	}

	streams.Info("")
	if r.dryRun {
		streams.Success("Dry run completed: %d files planned.", dryRuns)
		return nil
	}

	streams.Success("Scaffolding finished: %d created, %d overwritten, %d skipped.", created, overwritten, skipped)

	data := scaffold.BuildTemplateData(opts)
	streams.Info("\nNext steps:")
	streams.Info("  1. Generate the query set first (the module cannot compile before this):")
	streams.Info("     rtk just sqlc")
	streams.Info("  2. Register the slice in internal/bootstrap/engine.go (three places):")
	streams.Info("     - routeHandlers:         %s *%s.%sHandler", data.CamelName, data.HandlerPackage, data.PascalName)
	streams.Info("     - zeroHandlers():        %s: &%s.%sHandler{},", data.CamelName, data.HandlerPackage, data.PascalName)
	streams.Info("     - routeHandlers.mount(): %s.%s(v1, h.%s, mw)", data.RoutesPackage, data.RoutesFunc, data.CamelName)
	streams.Info("  3. Verify and format code:")
	streams.Info("     rtk just check")
	streams.Info("  4. Regenerate Swagger/OpenAPI documentation:")
	streams.Info("     rtk just docs")
	streams.Info("\nNote: `-d` runs the CLI from source, so the module must compile first.")
	streams.Info("      Run `rtk just sqlc` first, or revert the new files manually (git checkout / git clean).")
	return nil
}

func (r *makeCRUDRunner) promptForm() error {
	scope := scopeAdmin
	if r.client {
		scope = scopeClient
	}

	nameTitle := "Module Name"
	nameDesc := "Singular or plural identifier (e.g. article, order)"
	if r.del {
		nameTitle = "Module Name to Destroy"
		nameDesc = "Singular or plural identifier to delete (e.g. article, order)"
	}

	fields := []huh.Field{
		huh.NewInput().
			Title(nameTitle).
			Description(nameDesc).
			Value(&r.name).
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

	if !r.del {
		fields = append(fields,
			huh.NewInput().
				Title("Chinese Name (Optional)").
				Description("Business entity name (e.g. 文章, 订单)").
				Value(&r.chinese),
			huh.NewInput().
				Title("OpenAPI Tag Name (Optional)").
				Description("Documentation tag (e.g. 文章管理, 订单中心)").
				Value(&r.tag),
		)
	}

	if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
		return err
	}
	if scope == scopeClient {
		r.client = true
	}
	return nil
}
