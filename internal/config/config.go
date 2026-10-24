// Package config 负责从环境变量装载并校验应用程序的全局配置。
package config

// Config 汇总应用的所有配置模块。
type Config struct {
	App          AppConfig          `mapstructure:",squash"`
	Database     DatabaseConfig     `mapstructure:",squash"`
	Cache        CacheConfig        `mapstructure:",squash"`
	Storage      StorageConfig      `mapstructure:",squash"`
	JWT          JWTConfig          `mapstructure:",squash"`
	Mail         MailConfig         `mapstructure:",squash"`
	I18n         I18nConfig         `mapstructure:",squash"`
	Log          LogConfig          `mapstructure:",squash"`
	Auth         AuthConfig         `mapstructure:",squash"`
	Limiter      LimiterConfig      `mapstructure:",squash"`
	CORS         CORSConfig         `mapstructure:",squash"`
	Seed         SeedConfig         `mapstructure:",squash"`
	Queue        QueueConfig        `mapstructure:",squash"`
	Search       SearchConfig       `mapstructure:",squash"`
	Realtime     RealtimeConfig     `mapstructure:",squash"`
	GeoIP        GeoIPConfig        `mapstructure:",squash"`
	Excel        ExcelConfig        `mapstructure:",squash"`
	WeChat       WeChatConfig       `mapstructure:",squash"`
	Payment      PaymentConfig      `mapstructure:",squash"`
	Notification NotificationConfig `mapstructure:",squash"`
	Media        MediaConfig        `mapstructure:",squash"`
	PDF          PDFConfig          `mapstructure:",squash"`
	EventBus     EventBusConfig     `mapstructure:",squash"`
	Sentry       SentryConfig       `mapstructure:",squash"`
	Telemetry    TelemetryConfig    `mapstructure:",squash"`
	Temporal     TemporalConfig     `mapstructure:",squash"`
}

// Validate 依次执行各模块的语义与有效性校验。
func (c *Config) Validate() error {
	validators := []func() error{
		c.validateApp,
		c.validateServer,
		c.validateDatabase,
		c.validateCache,
		c.validateStorage,
		c.validateJWT,
		c.validateAuth,
		c.validateMail,
		c.validateLog,
		c.validateQueue,
		c.validateSearch,
		c.validateRealtime,
		c.validateServices,
		c.validateCORS,
		c.validateLimiter,
		c.validateI18n,
		c.validateWeChat,
		c.validatePayment,
		c.validateNotification,
		c.validateMedia,
		c.validateEventBus,
		c.validateSentry,
		c.validateTelemetry,
		c.validateTemporal,
	}
	for _, fn := range validators {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}
