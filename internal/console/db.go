package console

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"cgin/internal/database"
	"cgin/internal/database/seeders"
)

// newDBMigrateCmd 委托 atlas 应用版本式迁移。
func newDBMigrateCmd(f Factory) *cobra.Command {
	env := "app"

	cmd := &cobra.Command{
		Use:   "db:migrate",
		Short: "Apply database migrations via Atlas (atlas migrate apply)",
		Args:  maxArgs(0),
		RunE: runE(func(ctx context.Context, _ []string) error {
			if strings.TrimSpace(env) == "" {
				return &ValidationError{Field: "env", Message: "environment cannot be empty"}
			}
			return runAtlas(ctx, f.IOStreams(), "migrate", "apply", "--env", env)
		}),
	}

	cmd.Flags().StringVar(&env, "env", "app", "Atlas environment name to apply")
	return cmd
}

// newDBSeedCmd 调度运行数据播种器。
func newDBSeedCmd(f Factory) *cobra.Command {
	var (
		className = "DatabaseSeeder"
		fresh     bool
		force     bool
	)

	cmd := &cobra.Command{
		Use:   "db:seed",
		Short: "Seed database with records (default: DatabaseSeeder)",
		Args:  maxArgs(0),
		RunE: runE(func(ctx context.Context, _ []string) error {
			targetSeeder, ok := seeders.Get(className)
			if !ok {
				return &ValidationError{
					Field:   "class",
					Message: fmt.Sprintf("seeder [%s] not found (available: %v)", className, seeders.List()),
				}
			}

			streams := f.IOStreams()
			if fresh && !force {
				appName := defaultAppName
				if cfg, err := f.Config(); err == nil && cfg != nil && cfg.App.Name != "" {
					appName = cfg.App.Name
				} else if err != nil {
					return err
				}

				if err := confirmOrCancel(
					f,
					false,
					"Are you sure you want to fresh the database before seeding?",
					fmt.Sprintf("All table data in %s will be permanently deleted.", appName),
					"Yes, wipe all data",
					"Database seed operation cancelled.",
				); err != nil {
					return err
				}
			}

			db, err := f.DB(ctx)
			if err != nil {
				return err
			}

			if fresh {
				streams.Title("Wiping all table data...")
				if err := truncateAllTables(ctx, db); err != nil {
					return err
				}
				streams.Success("All table data cleared successfully.")
			}

			streams.Title("Seeding [%s]...", className)
			if err := runSeederWithProgress(ctx, streams, db, targetSeeder); err != nil {
				return err
			}

			streams.Success("Database seeding completed successfully.")
			return nil
		}),
	}

	cmd.Flags().StringVarP(&className, "class", "c", "DatabaseSeeder", "The class name of the seeder to run")
	cmd.Flags().BoolVar(&fresh, "fresh", false, "Wipe all table data before seeding")
	cmd.Flags().BoolVarP(&force, FlagForce, "f", false, UsageForceFlag)
	_ = cmd.RegisterFlagCompletionFunc("class", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return seeders.List(), cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// newDBFreshCmd 清空数据库后重放迁移并写入种子数据。
func newDBFreshCmd(f Factory) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "db:fresh",
		Short: "Drop all tables, re-run migrations, and seed database (destructive)",
		Args:  maxArgs(0),
		RunE: runE(func(ctx context.Context, _ []string) error {
			cfg, err := f.Config()
			if err != nil {
				return err
			}

			appName := defaultAppName
			if cfg != nil && cfg.App.Name != "" {
				appName = cfg.App.Name
			}

			if err := confirmOrCancel(
				f,
				force,
				"Are you sure you want to fresh the database?",
				fmt.Sprintf("All data in %s will be permanently dropped and migrations will be re-run.", appName),
				"Yes, drop and re-migrate",
				"Database fresh operation cancelled.",
			); err != nil {
				return err
			}

			db, err := f.DB(ctx)
			if err != nil {
				return err
			}

			streams := f.IOStreams()
			streams.Title("Rebuilding public schema...")
			if _, err := db.Pool().Exec(ctx, "DROP SCHEMA IF EXISTS public CASCADE"); err != nil {
				return fmt.Errorf("failed to drop public schema: %w", err)
			}
			if _, err := db.Pool().Exec(ctx, "CREATE SCHEMA public"); err != nil {
				return fmt.Errorf("failed to recreate public schema: %w", err)
			}

			streams.Title("Applying versioned migrations...")
			if err := runAtlas(ctx, streams, "migrate", "apply", "--env", "app"); err != nil {
				return err
			}

			streams.Title("Seeding initial data via DatabaseSeeder...")
			if err := runSeederWithProgress(ctx, streams, db, &seeders.DatabaseSeeder{}); err != nil {
				return err
			}

			streams.Success("Database refreshed and seeded successfully.")
			return nil
		}),
	}

	cmd.Flags().BoolVarP(&force, FlagForce, "f", false, UsageForceFlag)
	return cmd
}

func truncateAllTables(ctx context.Context, db *database.DB) error {
	if db == nil || db.Pool() == nil {
		return errors.New("database connection pool is uninitialized")
	}

	rows, err := db.Pool().Query(ctx, `
		SELECT quote_ident('public') || '.' || quote_ident(tablename) 
		FROM pg_tables 
		WHERE schemaname = 'public' 
		  AND tablename NOT LIKE 'atlas_%'
		ORDER BY tablename
	`)
	if err != nil {
		return fmt.Errorf("failed to list tables for truncation: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("failed to scan table name: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate table names: %w", err)
	}
	if len(tables) == 0 {
		return nil
	}

	truncateSQL := fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", strings.Join(tables, ", "))
	if _, err := db.Pool().Exec(ctx, truncateSQL); err != nil {
		return fmt.Errorf("failed to truncate tables: %w", err)
	}
	return nil
}

func runAtlas(ctx context.Context, streams *IOStreams, args ...string) error {
	command := exec.CommandContext(ctx, "atlas", args...)
	command.Stdout = streams.Out
	command.Stderr = streams.ErrOut
	command.Stdin = streams.In

	if err := command.Run(); err != nil {
		return fmt.Errorf("failed to execute atlas %s (ensure atlas is installed and .env configured): %w", strings.Join(args, " "), err)
	}
	return nil
}

func runSeederWithProgress(ctx context.Context, streams *IOStreams, db *database.DB, targetSeeder seeders.Seeder) error {
	var subBar *ProgressBar

	stepHook := func(name string, index, total int, duration time.Duration, done bool) {
		if !done {
			streams.Info("• [%d/%d] Seeding %s...", index, total, name)
		} else {
			if subBar != nil {
				subBar.Finish()
				subBar = nil
			}
			streams.Success("✓ [%d/%d] %s completed (%s)", index, total, name, duration.Round(time.Millisecond))
		}
	}

	subHook := func(current, total int64) {
		if total <= 0 {
			return
		}
		if subBar == nil {
			subBar = NewProgressBarTo(streams.Out, streams.CanPrompt(), total, "Records")
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
