package console

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"bgin/internal/console/core"
	platformsentry "bgin/internal/platform/sentry"
)

// SentryTestOptions 封装 sentry:test 命令的运行参数与依赖容器 (SRP & DIP)。
type SentryTestOptions struct {
	Message string
	Factory Factory
}

// NewSentryTestOptions 构造 SentryTestOptions 实例。
func NewSentryTestOptions(f Factory) *SentryTestOptions {
	return &SentryTestOptions{
		Message: "Test GlitchTip connection from bgin CLI",
		Factory: f,
	}
}

// Complete 补全配置与默认参数。
func (o *SentryTestOptions) Complete(_ *cobra.Command, _ []string) error {
	if strings.TrimSpace(o.Message) == "" {
		o.Message = "Test GlitchTip connection from bgin CLI"
	}
	return nil
}

// Validate 校验命令选项合法性。
func (o *SentryTestOptions) Validate() error {
	return nil
}

// Run 执行向 Sentry / GlitchTip 发送测试诊断消息。
func (o *SentryTestOptions) Run(_ context.Context) error {
	streams := o.Factory.IOStreams()

	cfg, err := o.Factory.Config()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	if !cfg.Sentry.IsEnabled() {
		core.WarnTo(streams.Out, "Sentry / GlitchTip is not enabled.")
		core.InfoTo(streams.Out, "Please set SENTRY_DSN or GLITCHTIP_DSN in your .env file to enable error tracking.")
		return nil
	}

	// 若未在当前进程中初始化，按当前配置执行初始化
	if !platformsentry.IsInitialized() {
		flush, initErr := platformsentry.Init(cfg.Sentry, cfg.App.Name, cfg.App.Env, "")
		if initErr != nil {
			return fmt.Errorf("failed to initialize Sentry client: %w", initErr)
		}
		defer flush()
	}

	core.TitleTo(streams.Out, fmt.Sprintf("Sending test message to Sentry/GlitchTip [DSN: %s]...", maskDSN(cfg.Sentry.EffectiveDSN())))

	eventID := platformsentry.CaptureMessage(o.Message)
	if eventID == nil {
		return errors.New("failed to capture message: event ID is nil")
	}

	// 确保事件推送到远端服务器
	platformsentry.Flush(5 * time.Second)

	core.SuccessTo(streams.Out, "Test event captured and flushed successfully! Event ID: %s", *eventID)
	core.InfoTo(streams.Out, "Check your GlitchTip / Sentry dashboard to verify receipt.")
	return nil
}

// maskDSN 对 DSN 密钥部分进行掩码显示，保护凭据安全。
func maskDSN(dsn string) string {
	proto, rest, foundProto := strings.Cut(dsn, "://")
	if !foundProto {
		return dsn
	}
	secret, host, foundAt := strings.Cut(rest, "@")
	if !foundAt || secret == "" {
		return dsn
	}
	if len(secret) > 8 {
		secret = secret[:4] + "..." + secret[len(secret)-4:]
	} else {
		secret = "***"
	}
	return proto + "://" + secret + "@" + host
}

// newSentryTestCmd 构造测试 Sentry / GlitchTip 连通性的命令。
func newSentryTestCmd(f Factory) *cobra.Command {
	opts := NewSentryTestOptions(f)

	cmd := &cobra.Command{
		Use:   "sentry:test",
		Short: "Send a test error message to Sentry / GlitchTip to verify connectivity",
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

	cmd.Flags().StringVarP(&opts.Message, "message", "m", opts.Message, "Custom test message to send to Sentry / GlitchTip")

	return cmd
}
