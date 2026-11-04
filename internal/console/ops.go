package console

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/samber/do/v2"
	"github.com/spf13/cobra"

	"cgin/internal/config"
	"cgin/internal/database/seeders"
	"cgin/pkg/resilience"
)

var (
	jwtSecretRegexp = regexp.MustCompile(`(?m)^JWT_SECRET=(.*)$`)

	defaultGeoIPDownloadURLs = []string{
		"https://fastly.jsdelivr.net/gh/lionsoul2014/ip2region@master/data/ip2region_v4.xdb",
		"https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v4.xdb",
		"https://ghproxy.net/https://raw.githubusercontent.com/lionsoul2014/ip2region/master/data/ip2region_v4.xdb",
	}
)

const defaultGeoIPDBPath = "storage/app/geoip/ip2region_v4.xdb"

// GenerateRandomSecret 生成指定字节长度的高强度随机密钥，以标准 base64 字符串形式返回。
func GenerateRandomSecret(byteLength int) (string, error) {
	b := make([]byte, byteLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// newJWTSecretCmd 创建或更新 .env 中的 JWT_SECRET 签名密钥。
func newJWTSecretCmd(f Factory) *cobra.Command {
	var (
		show    bool
		force   bool
		envPath = ".env"
	)

	cmd := &cobra.Command{
		Use:   "jwt:secret",
		Short: "Set the JWT secret key in .env file",
		Args:  maxArgs(0),
		RunE: runE(func(_ context.Context, _ []string) error {
			if strings.TrimSpace(envPath) == "" {
				return &ValidationError{Field: "env", Message: "cannot be empty"}
			}

			streams := f.IOStreams()
			if show {
				secret, err := GenerateRandomSecret(32)
				if err != nil {
					return fmt.Errorf("failed to generate secret: %w", err)
				}
				_, _ = fmt.Fprintln(streams.Out, secret)
				return nil
			}

			if err := ensureEnvFileExists(envPath, ".env.example", streams); err != nil {
				return err
			}

			rawBytes, err := os.ReadFile(envPath)
			if err != nil {
				return fmt.Errorf("failed to read %s: %w", envPath, err)
			}
			content := string(rawBytes)

			if hasConfiguredSecret(content, jwtSecretRegexp) && !force {
				if err := confirmOrCancel(
					f,
					false,
					"A valid JWT_SECRET already exists in .env. Overwrite it?",
					"This will invalidate all previously issued tokens.",
					"Yes, overwrite",
					"JWT secret update cancelled.",
				); err != nil {
					return err
				}
			}

			newContent, secret, _, err := upsertEnvSecret(content, jwtSecretRegexp, "JWT_SECRET", true)
			if err != nil {
				return fmt.Errorf("failed to generate secret: %w", err)
			}
			if err := os.WriteFile(envPath, []byte(newContent), 0o600); err != nil {
				return fmt.Errorf("failed to write %s: %w", envPath, err)
			}

			streams.Success("JWT secret [%s] set successfully.", secret)
			return nil
		}),
	}

	cmd.Flags().BoolVarP(&show, "show", "s", false, "Display the key instead of modifying files")
	cmd.Flags().BoolVarP(&force, FlagForce, "f", false, "Skip confirmation prompt when overwriting an existing key")
	cmd.Flags().StringVar(&envPath, "env", ".env", "Path to target .env file")
	return cmd
}

type appInitRunner struct {
	f              Factory
	streams        *IOStreams
	envPath        string
	envExamplePath string
	force          bool
	skipDB         bool
	skipMigrate    bool
	skipSeed       bool
	skipS3         bool
}

// newAppInitCmd 组装 app:init 命令行入口。
func newAppInitCmd(f Factory) *cobra.Command {
	r := &appInitRunner{
		f:              f,
		envPath:        ".env",
		envExamplePath: ".env.example",
	}

	cmd := &cobra.Command{
		Use:   "app:init",
		Short: "Initialize application environment (.env, secrets, databases, migrations, seed, and s3 bucket)",
		Args:  maxArgs(0),
		RunE: runE(func(ctx context.Context, _ []string) error {
			if strings.TrimSpace(r.envPath) == "" {
				return &ValidationError{Field: "env", Message: "cannot be empty"}
			}
			r.streams = f.IOStreams()
			return r.run(ctx)
		}),
	}

	cmd.Flags().StringVar(&r.envPath, "env", ".env", "Path to target .env file")
	cmd.Flags().StringVar(&r.envExamplePath, "env-example", ".env.example", "Path to template .env.example file")
	cmd.Flags().BoolVarP(&r.force, FlagForce, "f", false, "Force overwrite existing JWT_SECRET in .env")
	cmd.Flags().BoolVar(&r.skipDB, "skip-db", false, "Skip PostgreSQL database creation")
	cmd.Flags().BoolVar(&r.skipMigrate, "skip-migrate", false, "Skip running database migrations via Atlas")
	cmd.Flags().BoolVar(&r.skipSeed, "skip-seed", false, "Skip running initial database seeder")
	cmd.Flags().BoolVar(&r.skipS3, "skip-s3", false, "Skip S3 object storage bucket initialization")
	return cmd
}

func (r *appInitRunner) run(ctx context.Context) error {
	r.streams.Title("Initializing cgin application environment...")

	envMap, err := r.initEnv()
	if err != nil {
		return err
	}
	if err := r.initDatabases(ctx, envMap); err != nil {
		return err
	}
	if err := r.runMigrations(ctx); err != nil {
		return err
	}
	if err := r.seedDatabase(ctx); err != nil {
		return err
	}
	if err := r.initS3(ctx, envMap); err != nil {
		return err
	}

	r.streams.Success("Application initialization completed successfully.")
	r.streams.Info("Run 'just dev' to start local development services.")
	return nil
}

func (r *appInitRunner) initEnv() (map[string]string, error) {
	r.streams.Title("Step 1/5: Checking .env and security secrets...")

	if err := ensureEnvFileExists(r.envPath, r.envExamplePath, r.streams); err != nil {
		return nil, err
	}

	contentBytes, err := os.ReadFile(r.envPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", r.envPath, err)
	}

	content, jwtSecret, updated, err := upsertEnvSecret(string(contentBytes), jwtSecretRegexp, "JWT_SECRET", r.force)
	if err != nil {
		return nil, fmt.Errorf("failed to generate JWT_SECRET: %w", err)
	}
	if updated {
		if err := os.WriteFile(r.envPath, []byte(content), 0o600); err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", r.envPath, err)
		}
		r.streams.Success("Set JWT_SECRET in %s", r.envPath)
	} else {
		r.streams.Info("JWT_SECRET is already configured (preserved)")
	}

	envMap := parseEnvContent(content)
	if jwtSecret != "" {
		envMap["JWT_SECRET"] = jwtSecret
	}

	if freshCfg, err := config.Load(); err == nil && r.f != nil && r.f.Injector() != nil {
		do.OverrideValue(r.f.Injector(), freshCfg)
	}
	return envMap, nil
}

func (r *appInitRunner) initDatabases(ctx context.Context, envMap map[string]string) error {
	if r.skipDB {
		r.streams.Info("Step 2/5: Skipping PostgreSQL database creation (--skip-db)")
		return nil
	}

	r.streams.Title("Step 2/5: Checking and creating PostgreSQL databases...")

	mainDBURL := envMap["DB_URL"]
	if mainDBURL == "" {
		r.streams.Warn("DB_URL not found in %s, skipping database creation.", r.envPath)
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

	maintenanceCfg := mainCfg.Copy()
	maintenanceCfg.Database = "postgres"

	conn, err := connectMaintenance(ctx, maintenanceCfg)
	if err != nil {
		r.streams.Warn("Failed to connect to PostgreSQL maintenance database (%s): %v", maintenanceCfg.Host, err)
		r.streams.Info("Ensure PostgreSQL server is running (e.g. 'docker compose up -d postgres').")
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	for _, dbName := range targetDBs {
		created, err := ensureSingleDatabase(ctx, conn, dbName)
		if err != nil {
			return err
		}
		if created {
			r.streams.Success("Database [%s] created.", dbName)
		} else {
			r.streams.Info("Database [%s] already exists.", dbName)
		}
	}
	return nil
}

func (r *appInitRunner) runMigrations(ctx context.Context) error {
	if r.skipMigrate {
		r.streams.Info("Step 3/5: Skipping database migrations (--skip-migrate)")
		return nil
	}

	r.streams.Title("Step 3/5: Applying database migrations via Atlas...")
	if err := runAtlas(ctx, r.streams, "migrate", "apply", "--env", "app"); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}
	r.streams.Success("Database migrations applied.")
	return nil
}

func (r *appInitRunner) seedDatabase(ctx context.Context) error {
	if r.skipSeed {
		r.streams.Info("Step 4/5: Skipping database seeding (--skip-seed)")
		return nil
	}

	r.streams.Title("Step 4/5: Seeding initial database data...")
	db, err := r.f.DB(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire database handle for seeding: %w", err)
	}
	if err := runSeederWithProgress(ctx, r.streams, db, &seeders.DatabaseSeeder{}); err != nil {
		return fmt.Errorf("database seeding failed: %w", err)
	}
	r.streams.Success("Initial data seeded.")
	return nil
}

func (r *appInitRunner) initS3(ctx context.Context, envMap map[string]string) error {
	if r.skipS3 {
		r.streams.Info("Step 5/5: Skipping S3 bucket initialization (--skip-s3)")
		return nil
	}

	r.streams.Title("Step 5/5: Initializing S3 storage bucket...")

	disk := envMap["FILESYSTEM_DISK"]
	if disk != "" && disk != "s3" {
		r.streams.Info("Storage disk is configured as [%s] (non-s3). Ensuring local storage dir...", disk)
		storagePath := "storage/app"
		if err := os.MkdirAll(storagePath, 0o750); err != nil {
			return fmt.Errorf("failed to create local storage directory: %w", err)
		}
		r.streams.Success("Local storage directory [%s] ready.", storagePath)
		return nil
	}

	endpoint := envMap["S3_ENDPOINT"]
	bucket := envMap["S3_BUCKET"]
	accessKeyID := envMap["S3_ACCESS_KEY"]
	secretAccessKey := envMap["S3_SECRET_KEY"]
	region := envMap["S3_REGION"]
	useSSL := envMap["S3_USE_SSL"] == "true"

	if endpoint == "" || bucket == "" || accessKeyID == "" || secretAccessKey == "" {
		r.streams.Warn("Incomplete S3 credentials in %s (skipping S3 bucket creation).", r.envPath)
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
		r.streams.Warn("Could not reach S3 endpoint [%s]: %v (ensure Garage/MinIO is running)", endpoint, err)
		return nil
	}

	if !exists {
		if err := minioClient.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: region}); err != nil {
			return fmt.Errorf("failed to create S3 bucket [%s]: %w", bucket, err)
		}
		r.streams.Success("S3 Bucket [%s] created successfully.", bucket)
	} else {
		r.streams.Info("S3 Bucket [%s] already exists.", bucket)
	}
	return nil
}

func hasConfiguredSecret(content string, secretRegex *regexp.Regexp) bool {
	matches := secretRegex.FindStringSubmatch(content)
	return len(matches) > 1 && strings.TrimSpace(matches[1]) != ""
}

func upsertEnvSecret(content string, secretRegex *regexp.Regexp, keyName string, force bool) (string, string, bool, error) {
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

func ensureEnvFileExists(envPath, examplePath string, streams *IOStreams) error {
	if _, err := os.Stat(envPath); err == nil {
		streams.Info("Found existing %s file.", envPath)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to stat %s: %w", envPath, err)
	}

	if data, err := os.ReadFile(examplePath); err == nil {
		if err := os.WriteFile(envPath, data, 0o600); err != nil {
			return fmt.Errorf("failed to write %s: %w", envPath, err)
		}
		streams.Success("Created %s from %s.", envPath, examplePath)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to read %s: %w", examplePath, err)
	}

	if err := os.WriteFile(envPath, []byte(""), 0o600); err != nil {
		return fmt.Errorf("failed to create %s: %w", envPath, err)
	}
	streams.Info("Created fresh %s file.", envPath)
	return nil
}

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
		result[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(val), `"'`)
	}
	return result
}

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
		if _, exists := seen[parsed.Database]; !exists {
			seen[parsed.Database] = struct{}{}
			targets = append(targets, parsed.Database)
		}
	}
	return targets
}

func connectMaintenance(ctx context.Context, cfg *pgx.ConnConfig) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err == nil {
		return conn, nil
	}
	fallbackCfg := cfg.Copy()
	fallbackCfg.Database = "template1"
	if fallbackConn, fallbackErr := pgx.ConnectConfig(ctx, fallbackCfg); fallbackErr == nil {
		return fallbackConn, nil
	}
	return nil, err
}

func ensureSingleDatabase(ctx context.Context, conn *pgx.Conn, dbName string) (bool, error) {
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", dbName).Scan(&exists); err != nil {
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

// newGeoIPUpdateCmd 构造从官方源或镜像源下载最新 ip2region 数据库的命令。
func newGeoIPUpdateCmd(f Factory) *cobra.Command {
	var (
		dbPath string
		rawURL string
		force  bool
	)

	cmd := &cobra.Command{
		Use:   "geoip:update",
		Short: "Download or update the offline ip2region IP database (ip2region_v4.xdb)",
		Args:  maxArgs(0),
		RunE: runE(func(ctx context.Context, cmdArgs []string) error {
			path := strings.TrimSpace(dbPath)
			if path == "" {
				if cfg, err := f.Config(); err == nil && cfg != nil && cfg.GeoIP.DBPath != "" {
					path = cfg.GeoIP.DBPath
				} else {
					path = defaultGeoIPDBPath
				}
			}
			if strings.TrimSpace(path) == "" {
				return &ValidationError{Field: "path", Message: "database path cannot be empty"}
			}
			_ = cmdArgs

			streams := f.IOStreams()
			if _, err := os.Stat(path); err == nil && !force {
				if err := confirmOrCancel(
					f,
					false,
					fmt.Sprintf("GeoIP database already exists at [%s]. Overwrite it?", path),
					"A new version will replace the current ip2region_v4.xdb file.",
					"Yes, update",
					"GeoIP database update cancelled.",
				); err != nil {
					return err
				}
			}

			urls := defaultGeoIPDownloadURLs
			if u := strings.TrimSpace(rawURL); u != "" {
				urls = []string{u}
			}

			targetDir := filepath.Dir(path)
			if err := os.MkdirAll(targetDir, 0o755); err != nil { //nolint:gosec // 需保证目录对应用进程与运维可读写
				return fmt.Errorf("failed to create directory [%s]: %w", targetDir, err)
			}

			tmpFile := filepath.Join(targetDir, fmt.Sprintf(".%s.tmp.%d", filepath.Base(path), time.Now().UnixNano()))
			defer func() { _ = os.Remove(tmpFile) }()

			client := resilience.NewResilientHTTPClient(resilience.ResilientTransportConfig{Timeout: 90 * time.Second})

			var downloadErr error
			for i, u := range urls {
				streams.Title("Downloading GeoIP database [Source %d/%d: %s]...", i+1, len(urls), u)
				if downloadErr = downloadGeoIPFile(ctx, streams, client, u, tmpFile); downloadErr == nil {
					break
				}
				streams.Warn("Download failed from [%s]: %v. Trying next mirror...", u, downloadErr)
			}
			if downloadErr != nil {
				return fmt.Errorf("failed to download GeoIP database from all available sources: %w", downloadErr)
			}

			streams.Info("Verifying database integrity...")
			if err := verifyXDBFile(tmpFile); err != nil {
				return fmt.Errorf("downloaded database file verification failed: %w", err)
			}

			if err := os.Rename(tmpFile, path); err != nil {
				return fmt.Errorf("failed to replace database file at [%s]: %w", path, err)
			}

			streams.Success("GeoIP database updated successfully at [%s].", path)
			return nil
		}),
	}

	cmd.Flags().StringVar(&dbPath, "path", "", "Destination file path (default from GEOIP_DB_PATH or storage/app/geoip/ip2region_v4.xdb)")
	cmd.Flags().StringVar(&rawURL, "url", "", "Custom remote URL to download ip2region_v4.xdb from")
	cmd.Flags().BoolVarP(&force, FlagForce, "f", false, "Overwrite existing database without confirmation")
	return cmd
}

func downloadGeoIPFile(ctx context.Context, streams *IOStreams, client *http.Client, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "cgin-cli/geoip-updater")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http server returned non-200 status: %s", resp.Status)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	total := resp.ContentLength
	if total <= 0 {
		total = 11 * 1024 * 1024
	}
	bar := NewByteProgressBarTo(streams.Out, streams.CanPrompt(), total, "Downloading")
	defer bar.Finish()

	buf := make([]byte, 32*1024)
	var totalRead int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				return wErr
			}
			totalRead += int64(n)
			bar.Set(totalRead)
		}
		if rErr != nil {
			if errors.Is(rErr, io.EOF) {
				break
			}
			return rErr
		}
	}
	return nil
}

func verifyXDBFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() < 512*1024 {
		return fmt.Errorf("file size too small (%d bytes), likely an incomplete or corrupted download", fi.Size())
	}

	headerBuff := make([]byte, xdb.HeaderInfoLength)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if _, err := io.ReadFull(f, headerBuff); err != nil {
		return fmt.Errorf("failed to read xdb header: %w", err)
	}
	header, err := xdb.LoadHeaderFromBuff(headerBuff)
	if err != nil {
		return fmt.Errorf("invalid xdb header data: %w", err)
	}
	if _, err := xdb.VersionFromHeader(header); err != nil {
		return fmt.Errorf("invalid xdb version metadata: %w", err)
	}
	return nil
}
