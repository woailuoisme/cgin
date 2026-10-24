package auth

import (
	"context"
	"time"

	"github.com/samber/oops"
	"go.uber.org/zap"

	"bgin/internal/platform/errs"
	"bgin/internal/platform/logger"
	"bgin/internal/platform/mailer"
	"bgin/internal/platform/queue"
)

// TaskSendWelcomeEmail 声明发送欢迎邮件的 Asynq 任务唯一标识符。
const TaskSendWelcomeEmail = "auth:send_welcome_email"

// WelcomeEmailJob 向新注册用户异步投递欢迎邮件的后台队列任务。
type WelcomeEmailJob struct {
	queue.JobMeta
	Email string `json:"email"`
	Name  string `json:"name"`

	// mailer 由工厂闭包在 Worker 启动期显式注入，不参与 JSON 序列化
	mailer mailer.Mailer `json:"-"`
}

// NewWelcomeEmailJob 创建发送欢迎邮件的任务载荷实例（用于入队派发）。
func NewWelcomeEmailJob(email, name string) *WelcomeEmailJob {
	return &WelcomeEmailJob{
		JobMeta: queue.NewJobMeta(TaskSendWelcomeEmail, queue.DefaultQueueName).
			WithRetries(3).
			WithTimeout(30 * time.Second),
		Email: email,
		Name:  name,
	}
}

// RegisterJobs 向队列注册表登记该任务的还原工厂与执行依赖。
func RegisterJobs(reg queue.Registry, m mailer.Mailer) {
	if reg == nil {
		return
	}
	reg.Job(func() queue.Job {
		return &WelcomeEmailJob{
			JobMeta: queue.NewJobMeta(TaskSendWelcomeEmail, queue.DefaultQueueName),
			mailer:  m,
		}
	})
}

// Handle 任务执行入口，由 Asynq Worker 协程池调用。
func (j *WelcomeEmailJob) Handle(ctx context.Context) error {
	log := logger.FromContext(ctx)
	log.Info("processing welcome email task",
		zap.String("email", j.Email),
		zap.String("name", j.Name),
	)

	if j.mailer == nil {
		log.Warn("mailer dependency is not injected, skipped welcome email task")
		return nil
	}

	htmlBody, err := mailer.RenderTemplate("welcome", map[string]any{
		"Name":    j.Name,
		"AppName": "bgin",
		"Button": map[string]any{
			"Text":  "Get Started",
			"Url":   "#",
			"Color": "primary",
		},
	})
	if err != nil {
		return j.fail(err)
	}

	msg := mailer.Message{
		To:      j.Email,
		Subject: "Welcome to bgin",
		Body:    htmlBody,
		IsHTML:  true,
	}

	if err := j.mailer.Send(ctx, msg); err != nil {
		return j.fail(err)
	}

	log.Info("welcome email sent successfully",
		zap.String("email", j.Email),
	)
	return nil
}

func (j *WelcomeEmailJob) fail(err error) error {
	return oops.Code(string(errs.CodeInternal)).
		With("task", TaskSendWelcomeEmail).
		With("email", j.Email).
		Wrap(err)
}
