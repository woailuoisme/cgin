package console

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/samber/do/v2"
	"github.com/spf13/cobra"

	"cgin/internal/config"
	"cgin/internal/console/core"
	"cgin/internal/database/seeders"
)

var (
	// sessionSecretRegexp 匹配 .env 中已存在的 SESSION_SECRET 配置行。
	sessionSecretRegexp = regexp.MustCompile(`(?m)^SESSION_SECRET=(.*)$`)
)

// AppInitOptions 封装 app:init 命令的参数与依赖容器。
type AppInitOptions struct {
	noopComplete

	Force          bool
	SkipDB         bool
	SkipMigrate    bool
	SkipSeed       bool
	SkipS3         bool
	EnvPath        string
	EnvExamplePath string
	Factory        Factory
}

// NewAppInitOptions 构造 AppInitOptions 实例。
func NewAppInitOptions(f Factory) *AppInitOptions {
	return &AppInitOptions{
		EnvPath:        ".env",
		EnvExamplePath: ".env.example",
		Factory:        f,
	}
}

// Validate 校验命令选项合法性。
func (o *AppInitOptions) Validate() error {
	if o.EnvPath == "" {
		return &ValidationError{Field: "envPath", Message: "cannot be empty"}
	}
	return nil
}

// Run 依次执行全量初始化流水线。
func (o *AppInitOptions) Run(ctx context.Context) error {
	streams := o.Factory.IOStreams()
	core.TitleTo(streams.Out, "Initializing cgin application environment...")

	// 1. 初始化 .env 与 JWT/Session 双密钥
	envMap, err := o.initEnv(streams)
	if err != nil {
		return err
	}

	// 2. 检查并创建 PostgreSQL 目标数据库
	if err := o.initDatabases(ctx, streams, envMap); err != nil {
		return err
	}

	// 3. 执行 Atlas 版本迁移
	if err := o.runMigrations(ctx, streams); err != nil {
		return err
	}

	// 4. 数据播种（初始管理员）
	if err := o.seedDatabase(ctx, streams); err != nil {
		return err
	}

	// 5. 初始化 S3 对象存储 Bucket
	if err := o.initS3(ctx, streams, envMap); err != nil {
		return err
	}

	core.SuccessTo(streams.Out, "Application initialization completed successfully.")
	core.InfoTo(streams.Out, "Run 'just dev' to start local development services.")
	return nil
}

// initEnv 确保 .env 存在并补齐或刷新 JWT_SECRET / SESSION_SECRET。
func (o *AppInitOptions) initEnv(streams *core.IOStreams) (map[string]string, error) {
	core.TitleTo(streams.Out, "Step 1/5: Checking .env and security secrets...")

	if err := ensureEnvFileExists(o.EnvPath, o.EnvExamplePath, streams); err != nil {
		return nil, err
	}

	contentBytes, err := os.ReadFile(o.EnvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", o.EnvPath, err)
	}
	content := string(contentBytes)

	// 更新或填充 JWT_SECRET / SESSION_SECRET
	var (
		jwtSecret, sessionSecret string
		hasUpdates               bool
	)
	secretSpecs := []struct {
		name string
		re   *regexp.Regexp
		dest *string
	}{
		{"JWT_SECRET", jwtSecretRegexp, &jwtSecret},
		{"SESSION_SECRET", sessionSecretRegexp, &sessionSecret},
	}

	for _, spec := range secretSpecs {
		var updated bool
		content, *spec.dest, updated, err = ensureSecretInContent(content, spec.re, spec.name, o.Force)
		if err != nil {
			return nil, fmt.Errorf("failed to generate %s: %w", spec.name, err)
		}
		if updated {
			core.SuccessTo(streams.Out, "Set %s in %s", spec.name, o.EnvPath)
			hasUpdates = true
		} else {
			core.InfoTo(streams.Out, "%s is already configured (preserved)", spec.name)
		}
	}

	if hasUpdates {
		if err := os.WriteFile(o.EnvPath, []byte(content), 0o600); err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", o.EnvPath, err)
		}
	}

	envMap := parseEnvContent(content)
	// 注入刚生成的密钥到 envMap
	if jwtSecret != "" {
		envMap["JWT_SECRET"] = jwtSecret
	}
	if sessionSecret != "" {
		envMap["SESSION_SECRET"] = sessionSecret
	}

	// 重新装载并在 Injector 中同步最新配置
	if freshCfg, err := config.Load(); err == nil && o.Factory != nil && o.Factory.Injector() != nil {
		do.OverrideValue(o.Factory.Injector(), freshCfg)
	}

	return envMap, nil
}

// initDatabases 探查并自动创建所需的目标 PostgreSQL 数据库（cgin, atlas_dev, cgin_test）。
func (o *AppInitOptions) initDatabases(ctx context.Context, streams *core.IOStreams, envMap map[string]string) error {
	if o.SkipDB {
		core.InfoTo(streams.Out, "Step 2/5: Skipping PostgreSQL database creation (--skip-db)")
		return nil
	}

	core.TitleTo(streams.Out, "Step 2/5: Checking and creating PostgreSQL databases...")

	mainDBURL := envMap["DB_URL"]
	if mainDBURL == "" {
		core.WarnTo(streams.Out, "DB_URL not found in %s, skipping database creation.", o.EnvPath)
		return nil
	}

	mainCfg, err := pgx.ParseConfig(mainDBURL)
	if err != nil {
		return fmt.Errorf("failed to parse DB_URL: %w", err)
	}

	targetDBs := collectTargetDatabases(envMap)
	if len(targetDBs) == 0 {
		return nil
	}

	// 优先连接 postgres 维护库，回退到 template1
	maintenanceCfg := mainCfg.Copy()
	maintenanceCfg.Database = "postgres"

	conn, err := connectMaintenance(ctx, maintenanceCfg)
	if err != nil {
		core.WarnTo(streams.Out, "Failed to connect to PostgreSQL maintenance database (%s): %v", maintenanceCfg.Host, err)
		core.InfoTo(streams.Out, "Ensure PostgreSQL server is running (e.g. 'docker compose up -d postgres').")
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	for _, dbName := range targetDBs {
		created, err := ensureSingleDatabase(ctx, conn, dbName)
		if err != nil {
			return err
		}
		if created {
			core.SuccessTo(streams.Out, "Database [%s] created.", dbName)
		} else {
			core.InfoTo(streams.Out, "Database [%s] already exists.", dbName)
		}
	}

	return nil
}

// runMigrations 执行 Atlas 版本迁移。
func (o *AppInitOptions) runMigrations(ctx context.Context, streams *core.IOStreams) error {
	if o.SkipMigrate {
		core.InfoTo(streams.Out, "Step 3/5: Skipping database migrations (--skip-migrate)")
		return nil
	}

	core.TitleTo(streams.Out, "Step 3/5: Applying database migrations via Atlas...")
	if err := runAtlas(ctx, streams, "migrate", "apply", "--env", "app"); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}
	core.SuccessTo(streams.Out, "Database migrations applied.")
	return nil
}

// seedDatabase 执行数据播种，幂等注入首个管理员。
func (o *AppInitOptions) seedDatabase(ctx context.Context, streams *core.IOStreams) error {
	if o.SkipSeed {
		core.InfoTo(streams.Out, "Step 4/5: Skipping database seeding (--skip-seed)")
		return nil
	}

	core.TitleTo(streams.Out, "Step 4/5: Seeding initial database data...")
	db, err := o.Factory.DB(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire database handle for seeding: %w", err)
	}

	if err := runSeederWithProgress(ctx, streams, db, &seeders.DatabaseSeeder{}); err != nil {
		return fmt.Errorf("database seeding failed: %w", err)
	}
	core.SuccessTo(streams.Out, "Initial data seeded.")
	return nil
}

// initS3 检查并初始化 S3 对象存储 Bucket。
func (o *AppInitOptions) initS3(ctx context.Context, streams *core.IOStreams, envMap map[string]string) error {
	if o.SkipS3 {
		core.InfoTo(streams.Out, "Step 5/5: Skipping S3 bucket initialization (--skip-s3)")
		return nil
	}

	core.TitleTo(streams.Out, "Step 5/5: Initializing S3 storage bucket...")

	disk := envMap["FILESYSTEM_DISK"]
	if disk != "" && disk != "s3" {
		core.InfoTo(streams.Out, "Storage disk is configured as [%s] (non-s3). Ensuring local storage dir...", disk)
		storagePath := "storage/app"
		if err := os.MkdirAll(storagePath, 0o750); err != nil {
			return fmt.Errorf("failed to create local storage directory: %w", err)
		}
		core.SuccessTo(streams.Out, "Local storage directory [%s] ready.", storagePath)
		return nil
	}

	endpoint := envMap["S3_ENDPOINT"]
	bucket := envMap["S3_BUCKET"]
	accessKeyID := envMap["S3_ACCESS_KEY"]
	secretAccessKey := envMap["S3_SECRET_KEY"]
	region := envMap["S3_REGION"]
	useSSL := envMap["S3_USE_SSL"] == "true"

	if endpoint == "" || bucket == "" || accessKeyID == "" || secretAccessKey == "" {
		core.WarnTo(streams.Out, "Incomplete S3 credentials in %s (skipping S3 bucket creation).", o.EnvPath)
		return nil
	}

	if region == "" {
		region = "garage"
	}

	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize S3 client: %w", err)
	}

	exists, err := minioClient.BucketExists(ctx, bucket)
	if err != nil {
		core.WarnTo(streams.Out, "Could not reach S3 endpoint [%s]: %v (ensure Garage/MinIO is running)", endpoint, err)
		return nil
	}

	if !exists {
		if err := minioClient.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: region}); err != nil {
			return fmt.Errorf("failed to create S3 bucket [%s]: %w", bucket, err)
		}
		core.SuccessTo(streams.Out, "S3 Bucket [%s] created successfully.", bucket)
	} else {
		core.InfoTo(streams.Out, "S3 Bucket [%s] already exists.", bucket)
	}
	return nil
}

// newAppInitCmd 组装 app:init 命令行入口。
func newAppInitCmd(f Factory) *cobra.Command {
	opts := NewAppInitOptions(f)

	cmd := &cobra.Command{
		Use:   "app:init",
		Short: "Initialize application environment (.env, secrets, databases, migrations, seed, and s3 bucket)",
		Args:  maxArgs(0),
		RunE:  runCommand(opts),
	}

	cmd.Flags().StringVar(&opts.EnvPath, "env", ".env", "Path to target .env file")
	cmd.Flags().StringVar(&opts.EnvExamplePath, "env-example", ".env.example", "Path to template .env.example file")
	cmd.Flags().BoolVarP(&opts.Force, core.FlagForce, "f", false, "Force overwrite existing JWT_SECRET and SESSION_SECRET in .env")
	cmd.Flags().BoolVar(&opts.SkipDB, "skip-db", false, "Skip PostgreSQL database creation")
	cmd.Flags().BoolVar(&opts.SkipMigrate, "skip-migrate", false, "Skip running database migrations via Atlas")
	cmd.Flags().BoolVar(&opts.SkipSeed, "skip-seed", false, "Skip running initial database seeder")
	cmd.Flags().BoolVar(&opts.SkipS3, "skip-s3", false, "Skip S3 object storage bucket initialization")

	return cmd
}

// ensureSecretInContent 检查并填充或覆盖指定密钥配置行。
func ensureSecretInContent(content string, secretRegex *regexp.Regexp, keyName string, force bool) (string, string, bool, error) {
	matches := secretRegex.FindStringSubmatch(content)
	if len(matches) > 1 && strings.TrimSpace(matches[1]) != "" && !force {
		return content, strings.TrimSpace(matches[1]), false, nil
	}

	secret, err := GenerateRandomSecret(32)
	if err != nil {
		return "", "", false, err
	}

	var newContent string
	if secretRegex.MatchString(content) {
		newContent = secretRegex.ReplaceAllString(content, keyName+"="+secret)
	} else {
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		newContent = content + keyName + "=" + secret + "\n"
	}

	return newContent, secret, true, nil
}

// ensureEnvFileExists 确保目标环境文件存在，若缺失优先从 example 模板复制。
func ensureEnvFileExists(envPath, examplePath string, streams *core.IOStreams) error {
	if _, err := os.Stat(envPath); err == nil {
		core.InfoTo(streams.Out, "Found existing %s file.", envPath)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to stat %s: %w", envPath, err)
	}

	if err := copyFile(examplePath, envPath); err == nil {
		core.SuccessTo(streams.Out, "Created %s from %s.", envPath, examplePath)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to copy %s to %s: %w", examplePath, envPath, err)
	}

	if err := os.WriteFile(envPath, []byte(""), 0o600); err != nil {
		return fmt.Errorf("failed to create %s: %w", envPath, err)
	}
	core.InfoTo(streams.Out, "Created fresh %s file.", envPath)
	return nil
}

// copyFile 复制源文件至目标路径。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// parseEnvContent 简易解析环境变量文件内容为 map 字典。
func parseEnvContent(content string) map[string]string {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		result[key] = val
	}
	return result
}

// collectTargetDatabases 从配置字典中提取需要创建的全部数据库名称。
func collectTargetDatabases(envMap map[string]string) []string {
	seen := make(map[string]struct{})
	var targets []string

	for _, key := range []string{"DB_URL", "ATLAS_DEV_URL", "TEST_DATABASE_URL"} {
		raw := envMap[key]
		if raw == "" {
			continue
		}
		parsed, err := pgx.ParseConfig(raw)
		if err != nil || parsed.Database == "" {
			continue
		}
		dbName := parsed.Database
		if _, exists := seen[dbName]; !exists {
			seen[dbName] = struct{}{}
			targets = append(targets, dbName)
		}
	}
	return targets
}

// connectMaintenance 尝试连接维护数据库（先尝试 postgres，失败尝试 template1）。
func connectMaintenance(ctx context.Context, cfg *pgx.ConnConfig) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err == nil {
		return conn, nil
	}

	// 降级尝试 template1
	fallbackCfg := cfg.Copy()
	fallbackCfg.Database = "template1"
	fallbackConn, fallbackErr := pgx.ConnectConfig(ctx, fallbackCfg)
	if fallbackErr == nil {
		return fallbackConn, nil
	}

	return nil, err
}

// ensureSingleDatabase 探查并创建单个数据库。
func ensureSingleDatabase(ctx context.Context, conn *pgx.Conn, dbName string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", dbName).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check existence of database [%s]: %w", dbName, err)
	}
	if exists {
		return false, nil
	}

	sanitized := strings.ReplaceAll(dbName, `"`, `""`)
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, sanitized)); err != nil {
		return false, fmt.Errorf("failed to create database [%s]: %w", dbName, err)
	}
	return true, nil
}
