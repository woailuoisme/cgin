package console

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"bgin/internal/console/core"
)

// jwtSecretRegexp 匹配 .env 中已存在的 JWT_SECRET 配置行。
var jwtSecretRegexp = regexp.MustCompile(`(?m)^JWT_SECRET=(.*)$`)

// JWTSecretOptions 封装 jwt:secret 命令的参数与依赖容器 (SRP & DIP)。
type JWTSecretOptions struct {
	Show    bool
	Force   bool
	EnvPath string
	Factory Factory
}

// NewJWTSecretOptions 构造 JWTSecretOptions 实例。
func NewJWTSecretOptions(f Factory) *JWTSecretOptions {
	return &JWTSecretOptions{
		EnvPath: ".env",
		Factory: f,
	}
}

// Complete 从命令行上下文解析并补全配置。
func (o *JWTSecretOptions) Complete(_ *cobra.Command, _ []string) error {
	return nil
}

// Validate 校验命令选项合法性。
func (o *JWTSecretOptions) Validate() error {
	if o.EnvPath == "" {
		return &ValidationError{Field: "envPath", Message: "cannot be empty"}
	}
	return nil
}

// Run 执行 JWT 密钥生成或写入。
func (o *JWTSecretOptions) Run(_ context.Context) error {
	secret, err := GenerateRandomSecret(32)
	if err != nil {
		return fmt.Errorf("failed to generate secret: %w", err)
	}

	ioStreams := o.Factory.IOStreams()
	if o.Show {
		_, _ = fmt.Fprintln(ioStreams.Out, secret)
		return nil
	}

	o.ensureEnvFile()

	content, err := os.ReadFile(o.EnvPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to read %s: %w", o.EnvPath, err)
	}

	strContent := string(content)
	matches := jwtSecretRegexp.FindStringSubmatch(strContent)

	if len(matches) > 1 && strings.TrimSpace(matches[1]) != "" && !o.Force {
		confirmed, err := o.Factory.Prompter().Confirm(
			"A valid JWT_SECRET already exists in .env. Overwrite it?",
			"This will invalidate all previously issued tokens.",
			"Yes, overwrite",
		)
		if err != nil {
			return err
		}
		if !confirmed {
			core.WarnTo(ioStreams.Out, "JWT secret update cancelled.")
			return nil
		}
	}

	var newContent string
	if jwtSecretRegexp.MatchString(strContent) {
		newContent = jwtSecretRegexp.ReplaceAllString(strContent, "JWT_SECRET="+secret)
	} else {
		if len(strContent) > 0 && !strings.HasSuffix(strContent, "\n") {
			strContent += "\n"
		}
		newContent = strContent + "JWT_SECRET=" + secret + "\n"
	}

	if err := os.WriteFile(o.EnvPath, []byte(newContent), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", o.EnvPath, err)
	}

	core.SuccessTo(ioStreams.Out, "JWT secret [%s] set successfully.", secret)
	return nil
}

func (o *JWTSecretOptions) ensureEnvFile() {
	if _, err := os.Stat(o.EnvPath); !errors.Is(err, os.ErrNotExist) {
		return
	}
	exampleContent, err := os.ReadFile(".env.example")
	if err != nil {
		return
	}
	if err := os.WriteFile(o.EnvPath, exampleContent, 0o600); err == nil {
		core.TitleTo(o.Factory.IOStreams().Out, ".env file not found, created from .env.example.")
	}
}

// newJWTSecretCmd 创建或更新 .env 中的 JWT_SECRET 签名密钥（仿照 Laravel artisan jwt:secret）。
func newJWTSecretCmd(f Factory) *cobra.Command {
	opts := NewJWTSecretOptions(f)

	cmd := &cobra.Command{
		Use:   "jwt:secret",
		Short: "Set the JWT secret key in .env file",
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

	cmd.Flags().BoolVarP(&opts.Show, "show", "s", false, "Display the key instead of modifying files")
	cmd.Flags().BoolVarP(&opts.Force, core.FlagForce, "f", false, "Skip confirmation prompt when overwriting an existing key")

	return cmd
}

// GenerateRandomSecret 生成指定字节长度的高强度随机密钥，以标准 base64 字符串形式返回。
func GenerateRandomSecret(byteLength int) (string, error) {
	b := make([]byte, byteLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
