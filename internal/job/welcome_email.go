package job

import (
	"context"
	"log/slog"
	"time"

	"github.com/samber/oops"

	"cgin/pkg/errs"
	"cgin/pkg/logger"
	"cgin/pkg/mailer"
	"cgin/pkg/queue"
)

// TaskSendWelcomeEmail 声明发送欢迎邮件的 Asynq 任务唯一标识符。
const TaskSendWelcomeEmail = "auth:send_welcome_email"

// WelcomeEmailPayload 向新注册用户异步投递欢迎邮件的纯数据传输载荷 (DTO)。
type WelcomeEmailPayload struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// WelcomeEmailHandler 专职负责欢迎邮件任务执行的业务服务组件 (SRP)。
type WelcomeEmailHandler struct {
	mailer mailer.Mailer
}

// NewWelcomeEmailHandler 创建带有 Mailer 依赖的欢迎邮件执行处理器。
func NewWelcomeEmailHandler(m mailer.Mailer) *WelcomeEmailHandler {
	return &WelcomeEmailHandler{mailer: m}
}

// RegisterJobs 向任务路由器显式挂载该任务的强类型处理函数与默认执行选项。
func RegisterJobs(router *queue.TaskRouter, m mailer.Mailer) {
	if router == nil {
		return
	}
	h := NewWelcomeEmailHandler(m)
	queue.Register(router, TaskSendWelcomeEmail, h.Handle,
		queue.WithQueue(queue.DefaultQueueName),
		queue.WithMaxRetry(3),
		queue.WithTimeout(30*time.Second),
	)
}

// Handle 任务执行入口，由 Worker 协程池在消费时反序列化载荷后直接调度。
func (h *WelcomeEmailHandler) Handle(ctx context.Context, p WelcomeEmailPayload) error {
	log := logger.FromContext(ctx)
	log.InfoContext(ctx, "processing welcome email task",
		slog.String("email", p.Email),
		slog.String("name", p.Name),
	)

	if h.mailer == nil {
		log.WarnContext(ctx, "mailer dependency is not injected, skipped welcome email task")
		return nil
	}

	htmlBody, err := mailer.RenderTemplate("welcome", map[string]any{
		"Name":    p.Name,
		"AppName": "cgin",
		"Button": map[string]any{
			"Text":  "Get Started",
			"Url":   "#",
			"Color": "primary",
		},
	})
	if err != nil {
		return h.fail(p, err)
	}

	msg := mailer.Message{
		To:      []string{p.Email},
		Subject: "Welcome to cgin",
		Body:    htmlBody,
		IsHTML:  true,
	}

	if err := h.mailer.Send(ctx, msg); err != nil {
		return h.fail(p, err)
	}

	log.InfoContext(ctx, "welcome email sent successfully",
		slog.String("email", p.Email),
	)
	return nil
}

func (h *WelcomeEmailHandler) fail(p WelcomeEmailPayload, err error) error {
	return oops.Code(string(errs.CodeInternal)).
		With("task", TaskSendWelcomeEmail).
		With("email", p.Email).
		Wrap(err)
}
