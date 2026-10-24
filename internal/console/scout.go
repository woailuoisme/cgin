package console

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"github.com/uptrace/bun"

	"bgin/internal/console/core"
	"bgin/internal/platform/search"
	_ "bgin/internal/shared/indexes"
)

// ScoutImportOptions 封装 scout:import 命令的参数与依赖容器 (SRP & DIP)。
type ScoutImportOptions struct {
	Model     string
	All       bool
	ChunkSize int
	Factory   Factory
}

// NewScoutImportOptions 构造 ScoutImportOptions 实例。
func NewScoutImportOptions(f Factory) *ScoutImportOptions {
	return &ScoutImportOptions{
		ChunkSize: 100,
		Factory:   f,
	}
}

// Complete 从命令行上下文解析并补全配置。
func (o *ScoutImportOptions) Complete(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		o.Model = strings.TrimSpace(args[0])
	}
	return nil
}

const fieldModel = "model"

// Validate 校验导入参数合法性。
func (o *ScoutImportOptions) Validate() error {
	if !o.All {
		if o.Model == "" {
			return &ValidationError{Field: fieldModel, Message: "please specify a model name or use --all"}
		}
		if _, ok := search.GetModel(o.Model); !ok {
			return &ValidationError{Field: fieldModel, Message: fmt.Sprintf("model [%s] is not registered as searchable", o.Model)}
		}
	}
	if o.ChunkSize <= 0 {
		return &ValidationError{Field: "chunk", Message: "must be greater than 0"}
	}
	return nil
}

// Run 执行搜索引擎文档批量导入。
func (o *ScoutImportOptions) Run(ctx context.Context) error {
	cfg, err := o.Factory.Config()
	if err != nil {
		return err
	}

	if !cfg.Search.Enabled() {
		return errors.New("search engine is disabled (check SCOUT_DRIVER and MEILISEARCH_HOST)")
	}

	searchClient, err := search.New(cfg.Search)
	if err != nil {
		return fmt.Errorf("failed to initialize search client: %w", err)
	}

	db, err := o.Factory.DB(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	streams := o.Factory.IOStreams()
	var targets []search.ModelDescriptor
	if o.All {
		targets = search.RegisteredModels()
		if len(targets) == 0 {
			core.InfoTo(streams.Out, "No searchable models registered.")
			return nil
		}
	} else {
		desc, ok := search.GetModel(o.Model)
		if !ok {
			return fmt.Errorf("model [%s] is not registered as searchable", o.Model)
		}
		targets = []search.ModelDescriptor{desc}
	}

	for _, target := range targets {
		if err := importSearchTarget(ctx, streams, db, searchClient, target, o.ChunkSize); err != nil {
			return err
		}
	}

	return nil
}

// newScoutImportCmd 将指定模型或全部可搜索模型的数据批量导入搜索引擎。
func newScoutImportCmd(f Factory) *cobra.Command {
	opts := NewScoutImportOptions(f)

	cmd := &cobra.Command{
		Use:   "scout:import [model]",
		Short: "Import model records into the search index",
		Args: func(_ *cobra.Command, args []string) error {
			if !opts.All && (len(args) == 0 || strings.TrimSpace(args[0]) == "") {
				return errors.New("please specify a model name or use --all to import all models")
			}
			return nil
		},
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return getSearchableModelNames(), cobra.ShellCompDirectiveNoFileComp
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

	cmd.Flags().BoolVarP(&opts.All, "all", "a", false, "Import all registered searchable models")
	cmd.Flags().IntVarP(&opts.ChunkSize, "chunk", "c", 100, "Number of records to process per batch")

	return cmd
}

// ScoutFlushOptions 封装 scout:flush 命令的参数与依赖容器 (SRP & DIP)。
type ScoutFlushOptions struct {
	Model   string
	Force   bool
	Factory Factory
}

// NewScoutFlushOptions 构造 ScoutFlushOptions 实例。
func NewScoutFlushOptions(f Factory) *ScoutFlushOptions {
	return &ScoutFlushOptions{Factory: f}
}

// Complete 从命令行上下文解析并补全配置。
func (o *ScoutFlushOptions) Complete(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		o.Model = strings.TrimSpace(args[0])
	}
	return nil
}

// Validate 校验清空参数合法性。
func (o *ScoutFlushOptions) Validate() error {
	if o.Model == "" {
		return &ValidationError{Field: fieldModel, Message: "please provide a model name to flush"}
	}
	if _, ok := search.GetModel(o.Model); !ok {
		return &ValidationError{Field: fieldModel, Message: fmt.Sprintf("model [%s] is not registered as searchable", o.Model)}
	}
	return nil
}

// Run 清空指定可搜索模型的全文检索索引文档。
func (o *ScoutFlushOptions) Run(ctx context.Context) error {
	cfg, err := o.Factory.Config()
	if err != nil {
		return err
	}

	if !cfg.Search.Enabled() {
		return errors.New("search engine is disabled (check SCOUT_DRIVER and MEILISEARCH_HOST)")
	}

	desc, ok := search.GetModel(o.Model)
	if !ok {
		return fmt.Errorf("model [%s] is not registered as searchable", o.Model)
	}

	streams := o.Factory.IOStreams()
	if !o.Force {
		confirmed, err := o.Factory.Prompter().Confirm(
			fmt.Sprintf("Are you sure you want to flush all records from index [%s]?", desc.IndexName),
			"All documents in this search index will be deleted.",
			"Yes, flush",
		)
		if err != nil {
			return err
		}
		if !confirmed {
			core.WarnTo(streams.Out, "Scout flush operation cancelled.")
			return core.ErrCanceled
		}
	}

	searchClient, err := search.New(cfg.Search)
	if err != nil {
		return fmt.Errorf("failed to initialize search client: %w", err)
	}

	idx := searchClient.Index(desc.IndexName)
	if err := idx.DeleteAllDocuments(ctx); err != nil {
		return fmt.Errorf("failed to flush records from index [%s]: %w", desc.IndexName, err)
	}

	core.SuccessTo(streams.Out, "All records for model [%s] flushed from index [%s].", desc.Name, desc.IndexName)
	return nil
}

// newScoutFlushCmd 清空指定可搜索模型的全文检索索引文档。
func newScoutFlushCmd(f Factory) *cobra.Command {
	opts := NewScoutFlushOptions(f)

	cmd := &cobra.Command{
		Use:   "scout:flush <model>",
		Short: "Flush all of the model's records from the search index",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
				return errors.New("please provide a model name to flush")
			}
			return nil
		},
		ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return getSearchableModelNames(), cobra.ShellCompDirectiveNoFileComp
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

	cmd.Flags().BoolVarP(&opts.Force, core.FlagForce, "f", false, core.UsageForceFlag)

	return cmd
}

// getSearchableModelNames 获取所有已注册的唯一可搜索模型名称列表。
func getSearchableModelNames() []string {
	return lo.Map(search.RegisteredModels(), func(d search.ModelDescriptor, _ int) string {
		return d.Name
	})
}

// importSearchTarget 导入单个可搜索模型的全量数据并驱动进度条展示。
func importSearchTarget(ctx context.Context, streams *core.IOStreams, db *bun.DB, searchClient search.Client, target search.ModelDescriptor, chunkSize int) error {
	core.TitleTo(streams.Out, fmt.Sprintf("Importing [%s] records into search index [%s]...", target.Name, target.IndexName))
	idx := searchClient.Index(target.IndexName)

	var bar *core.ProgressBar
	if target.Counter != nil {
		if total, err := target.Counter(ctx, db); err == nil && total > 0 {
			bar = core.NewProgressBarTo(streams.Out, streams.CanPrompt(), total, target.Name)
		}
	}

	count, err := target.Importer(ctx, db, chunkSize, func(docs []map[string]any) error {
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

	core.SuccessTo(streams.Out, "Successfully imported [%s] records for model [%s].", humanize.Comma(count), target.Name)
	return nil
}
