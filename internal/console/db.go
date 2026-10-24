package console

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/uptrace/bun"

	"bgin/internal/console/core"
	"bgin/internal/database/seeders"
)

// DBMigrateOptions 封装 db:migrate 命令的参数与依赖容器 (SRP & DIP)。
type DBMigrateOptions struct {
	Env     string
	Factory Factory
}

// NewDBMigrateOptions 构造 DBMigrateOptions 实例。
func NewDBMigrateOptions(f Factory) *DBMigrateOptions {
	return &DBMigrateOptions{
		Env:     "app",
		Factory: f,
	}
}

// Complete 从命令行上下文解析并补全配置。
func (o *DBMigrateOptions) Complete(_ *cobra.Command, _ []string) error {
	return nil
}

// Validate 校验迁移命令参数。
func (o *DBMigrateOptions) Validate() error {
	if o.Env == "" {
		return &ValidationError{Field: "env", Message: "environment cannot be empty"}
	}
	return nil
}

// Run 执行数据库版本迁移。
func (o *DBMigrateOptions) Run(ctx context.Context) error {
	return runAtlas(ctx, o.Factory.IOStreams(), "migrate", "apply", "--env", o.Env)
}

// newDBMigrateCmd 委托 atlas 应用版本式迁移。
func newDBMigrateCmd(f Factory) *cobra.Command {
	opts := NewDBMigrateOptions(f)

	cmd := &cobra.Command{
		Use:   "db:migrate",
		Short: "Apply database migrations via Atlas (atlas migrate apply)",
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

	cmd.Flags().StringVar(&opts.Env, "env", "app", "Atlas environment name to apply")

	return cmd
}

// DBSeedOptions 封装 db:seed 命令的参数与依赖容器 (SRP & DIP)。
type DBSeedOptions struct {
	ClassName string
	Factory   Factory
}

// NewDBSeedOptions 构造 DBSeedOptions 实例。
func NewDBSeedOptions(f Factory) *DBSeedOptions {
	return &DBSeedOptions{
		ClassName: "DatabaseSeeder",
		Factory:   f,
	}
}

// Complete 从命令行上下文解析并补全配置。
func (o *DBSeedOptions) Complete(_ *cobra.Command, _ []string) error {
	if o.ClassName == "" {
		o.ClassName = "DatabaseSeeder"
	}
	return nil
}

// Validate 校验指定的 Seeder 类名是否合法存在。
func (o *DBSeedOptions) Validate() error {
	if _, ok := seeders.Get(o.ClassName); !ok {
		available := seeders.List()
		return &ValidationError{
			Field:   "class",
			Message: fmt.Sprintf("seeder [%s] not found (available: %v)", o.ClassName, available),
		}
	}
	return nil
}

// Run 调度运行数据播种。
func (o *DBSeedOptions) Run(ctx context.Context) error {
	db, err := o.Factory.DB(ctx)
	if err != nil {
		return err
	}

	targetSeeder, ok := seeders.Get(o.ClassName)
	if !ok {
		available := seeders.List()
		return fmt.Errorf("seeder [%s] not found (available: %v)", o.ClassName, available)
	}

	streams := o.Factory.IOStreams()
	core.TitleTo(streams.Out, fmt.Sprintf("Seeding [%s]...", o.ClassName))
	if err := runSeederWithProgress(ctx, streams, db, targetSeeder); err != nil {
		return err
	}

	core.SuccessTo(streams.Out, "Database seeding completed successfully.")
	return nil
}

// newDBSeedCmd 调度运行数据播种器（支持通过 --class 指定单个 Seeder，缺省为 DatabaseSeeder）。
func newDBSeedCmd(f Factory) *cobra.Command {
	opts := NewDBSeedOptions(f)

	cmd := &cobra.Command{
		Use:   "db:seed",
		Short: "Seed database with records (default: DatabaseSeeder)",
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

	cmd.Flags().StringVarP(&opts.ClassName, "class", "c", "DatabaseSeeder", "The class name of the seeder to run")
	_ = cmd.RegisterFlagCompletionFunc("class", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return seeders.List(), cobra.ShellCompDirectiveNoFileComp
	})

	return cmd
}

// DBFreshOptions 封装 db:fresh 命令的参数与依赖容器 (SRP & DIP)。
type DBFreshOptions struct {
	Force   bool
	Factory Factory
}

// NewDBFreshOptions 构造 DBFreshOptions 实例。
func NewDBFreshOptions(f Factory) *DBFreshOptions {
	return &DBFreshOptions{Factory: f}
}

// Complete 从命令行上下文解析并补全配置。
func (o *DBFreshOptions) Complete(_ *cobra.Command, _ []string) error {
	return nil
}

// Validate 校验重置命令选项合法性。
func (o *DBFreshOptions) Validate() error {
	return nil
}

// Run 清空数据库后重放迁移并写入种子数据。
func (o *DBFreshOptions) Run(ctx context.Context) error {
	cfg, err := o.Factory.Config()
	if err != nil {
		return err
	}

	streams := o.Factory.IOStreams()
	if !o.Force {
		confirmed, err := o.Factory.Prompter().Confirm(
			"Are you sure you want to fresh the database?",
			fmt.Sprintf("All data in %s will be permanently dropped and migrations will be re-run.", cfg.App.Name),
			"Yes, drop and re-migrate",
		)
		if err != nil {
			return err
		}
		if !confirmed {
			core.WarnTo(streams.Out, "Database fresh operation cancelled.")
			return core.ErrCanceled
		}
	}

	db, err := o.Factory.DB(ctx)
	if err != nil {
		return err
	}

	core.TitleTo(streams.Out, "Rebuilding public schema...")
	if _, err := db.ExecContext(ctx, "DROP SCHEMA IF EXISTS public CASCADE"); err != nil {
		return fmt.Errorf("failed to drop public schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA public"); err != nil {
		return fmt.Errorf("failed to recreate public schema: %w", err)
	}

	core.TitleTo(streams.Out, "Applying versioned migrations...")
	if err := runAtlas(ctx, streams, "migrate", "apply", "--env", "app"); err != nil {
		return err
	}

	core.TitleTo(streams.Out, "Seeding initial data via DatabaseSeeder...")
	if err := runSeederWithProgress(ctx, streams, db, &seeders.DatabaseSeeder{}); err != nil {
		return err
	}

	core.SuccessTo(streams.Out, "Database refreshed and seeded successfully.")
	return nil
}

// newDBFreshCmd 清空数据库后重放迁移并写入种子数据。破坏性操作默认需要交互确认。
func newDBFreshCmd(f Factory) *cobra.Command {
	opts := NewDBFreshOptions(f)

	cmd := &cobra.Command{
		Use:   "db:fresh",
		Short: "Drop all tables, re-run migrations, and seed database (destructive)",
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

	cmd.Flags().BoolVarP(&opts.Force, core.FlagForce, "f", false, core.UsageForceFlag)

	return cmd
}

// runAtlas 以当前工作目录为基准调用 atlas 命令行工具，输入输出绑定到 IOStreams。
func runAtlas(ctx context.Context, streams *core.IOStreams, args ...string) error {
	command := exec.CommandContext(ctx, "atlas", args...)
	command.Stdout = streams.Out
	command.Stderr = streams.ErrOut
	command.Stdin = streams.In

	if err := command.Run(); err != nil {
		return fmt.Errorf("failed to execute atlas %s (ensure atlas is installed and .env configured): %w", strings.Join(args, " "), err)
	}
	return nil
}

// runSeederWithProgress 带有控制台步骤展示与细粒度进度条调度的 Seeder 执行函数。
func runSeederWithProgress(ctx context.Context, streams *core.IOStreams, db *bun.DB, targetSeeder seeders.Seeder) error {
	var subBar *core.ProgressBar

	stepHook := func(name string, index, total int, duration time.Duration, done bool) {
		if !done {
			core.InfoTo(streams.Out, "• [%d/%d] Seeding %s...", index, total, name)
		} else {
			if subBar != nil {
				subBar.Finish()
				subBar = nil
			}
			core.SuccessTo(streams.Out, "✓ [%d/%d] %s completed (%s)", index, total, name, duration.Round(time.Millisecond))
		}
	}

	subHook := func(current, total int64) {
		if total <= 0 {
			return
		}
		if subBar == nil {
			subBar = core.NewProgressBarTo(streams.Out, streams.CanPrompt(), total, "Records")
		}
		subBar.Set(current)
	}

	runCtx := seeders.WithProgressHook(ctx, stepHook)
	runCtx = seeders.WithSubProgressHook(runCtx, subHook)

	runner := seeders.NewRunner(db).WithStepHook(stepHook)
	err := runner.Call(runCtx, targetSeeder)
	if subBar != nil {
		subBar.Finish()
	}
	return err
}
