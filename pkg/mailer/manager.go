package mailer

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/samber/oops"

	"cgin/pkg/errs"
	"cgin/pkg/logger"
)

// DriverFactory 定义邮件驱动构造函数签名。
type DriverFactory func() (Driver, error)

var (
	registryMu sync.RWMutex
	drivers    = make(map[string]DriverFactory)
)

// RegisterDriver 注册自定义邮件驱动构造工厂。
func RegisterDriver(name string, factory DriverFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	drivers[strings.ToLower(strings.TrimSpace(name))] = factory
}

// GetDriverFactory 获取已注册的邮件驱动构造工厂。
func GetDriverFactory(name string) (DriverFactory, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	factory, ok := drivers[strings.ToLower(strings.TrimSpace(name))]
	return factory, ok
}

// Manager 邮件管理器，实现 Mailer 契约与 do.ShutdownerWithError 优雅停机契约。
type Manager struct {
	driver Driver
	log    *zerolog.Logger
}

var _ Mailer = (*Manager)(nil)

// NewManager 封装底层 Driver 构建 Manager 实例。
func NewManager(driver Driver, log ...*zerolog.Logger) *Manager {
	var l *zerolog.Logger
	if len(log) > 0 && log[0] != nil {
		l = log[0]
	} else {
		l = logger.Nop()
	}
	return &Manager{
		driver: driver,
		log:    l,
	}
}

// Driver 返回底层驱动。
func (m *Manager) Driver() Driver {
	return m.driver
}

// Send 统一收敛校验与日志投递邮件。
func (m *Manager) Send(ctx context.Context, msg Message) error {
	if len(msg.To) == 0 {
		return errs.BadRequest("email recipient 'To' cannot be empty")
	}
	if m.driver == nil {
		return oops.Code(errs.CodeInternal).Errorf("mailer: driver is not initialized")
	}

	if err := m.driver.Send(ctx, msg); err != nil {
		m.log.Error().
			Err(err).
			Strs("to", maskRecipients(msg.To)).
			Strs("cc", maskRecipients(msg.Cc)).
			Strs("bcc", maskRecipients(msg.Bcc)).
			Str("subject", msg.Subject).
			Int("attachments", len(msg.Attachments)).
			Msg("mail delivery failed")
		return err
	}

	m.log.Debug().
		Strs("to", maskRecipients(msg.To)).
		Strs("cc", maskRecipients(msg.Cc)).
		Strs("bcc", maskRecipients(msg.Bcc)).
		Str("subject", msg.Subject).
		Int("attachments", len(msg.Attachments)).
		Bool("is_html", msg.IsHTML).
		Msg("mail delivered successfully")
	return nil
}

// SendMailable 渲染结构化模板邮件并投递。
func (m *Manager) SendMailable(ctx context.Context, to string, mailable Mailable) error {
	to = strings.TrimSpace(to)
	if to == "" {
		return errs.BadRequest("email recipient 'To' cannot be empty")
	}
	if mailable == nil {
		return errs.BadRequest("mailable cannot be nil")
	}

	tmplName, data := mailable.Template()
	htmlBody, err := RenderTemplate(tmplName, data)
	if err != nil {
		return oops.Code(errs.CodeInternal).Wrapf(err, "mailer: render template %s", tmplName)
	}

	return m.Send(ctx, Message{
		To:      []string{to},
		Subject: mailable.Subject(),
		Body:    htmlBody,
		IsHTML:  true,
	})
}

// Ping 检查驱动可用性。
func (m *Manager) Ping(ctx context.Context) error {
	if m.driver == nil {
		return errors.New("mailer: driver is not initialized")
	}
	return m.driver.Ping(ctx)
}

// Close 释放驱动底层资源。
func (m *Manager) Close() error {
	if m.driver != nil {
		return m.driver.Close()
	}
	return nil
}

// Shutdown 原生满足 do.ShutdownerWithError 优雅停机契约。
func (m *Manager) Shutdown() error {
	return m.Close()
}
