// Package config 负责从环境变量装载并校验应用程序的全局配置。
package config

import "errors"

// Config 汇总应用的所有配置模块。
type Config struct {
	App          AppConfig
	Database     DatabaseConfig
	Cache        CacheConfig
	Storage      StorageConfig
	JWT          JWTConfig
	Mail         MailConfig
	I18n         I18nConfig
	Log          LogConfig
	Auth         AuthConfig
	Limiter      LimiterConfig
	CORS         CORSConfig
	Session      SessionConfig
	Seed         SeedConfig
	Queue        QueueConfig
	Search       SearchConfig
	Realtime     RealtimeConfig
	GeoIP        GeoIPConfig
	WeChat       WeChatConfig
	Payment      PaymentConfig
	Notification NotificationConfig
	Media        MediaConfig
	PDF          PDFConfig
	EventBus     EventBusConfig
	Telemetry    TelemetryConfig
}

// Validate 依次执行各模块的语义与有效性校验，并汇总全部失败原因。
//
// 校验分三层，职责不重叠：
//   - env tag（required / notEmpty / 类型解析）：解析期失败，只负责“取不到值”与“类型不符”。
//   - 各 validateXxx：语义期失败，负责区间、跨字段关系、条件必填，并保证错误信息里出现环境变量名。
//   - 少数字段在此完成默认值回填（如 Notification.SMS.Driver、Queue.RedisURL），
//     因此本方法必须完整执行，不得因前置模块失败而短路。
//
// 返回的 error 可能包含多条失败原因（errors.Join），逐行呈现。
func (c *Config) Validate() error {
	return errors.Join(
		c.validateApp(),
		c.validateServer(),
		c.validateDatabase(),
		c.validateCache(),
		c.validateStorage(),
		c.validateJWT(),
		c.validateAuth(),
		c.validateMail(),
		c.validateQueue(),
		c.validateSearch(),
		c.validateRealtime(),
		c.validateCORS(),
		c.validateSession(),
		c.validateLimiter(),
		c.validateWeChat(),
		c.validatePayment(),
		c.validateNotification(),
		c.validateMedia(),
		c.validateEventBus(),
		c.validateTelemetry(),
	)
}
