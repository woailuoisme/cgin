package console

import (
	"context"
	"sync"

	"github.com/rs/zerolog"

	"cgin/internal/config"
	"cgin/internal/console/core"
	"cgin/internal/database"
	"cgin/pkg/logger"
)

// ValidationError 表示控制台命令参数校验失败的强类型错误。
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return e.Field + ": " + e.Message
	}
	return e.Message
}

// Factory 按需提供控制台命令所需的外部依赖与基础设施（懒加载）。
// 命令通过依赖工厂按需获取服务，实现对底层物理依赖（配置、数据库连接、终端流、交互 Prompt）的彻底解耦。
type Factory interface {
	IOStreams() *core.IOStreams
	Prompter() core.Prompter
	Config() (*config.Config, error)
	DB(ctx context.Context) (*database.DB, error)
	Logger() *zerolog.Logger
}

// defaultFactory 提供生产环境默认的懒加载工厂实现。
type defaultFactory struct {
	ioStreams *core.IOStreams
	prompter  core.Prompter

	cfgOnce sync.Once
	cfg     *config.Config
	cfgErr  error

	dbOnce sync.Once
	db     *database.DB
	dbErr  error

	loggerOnce sync.Once
	logger     *zerolog.Logger
}

// NewDefaultFactory 构造生产环境默认的依赖工厂实例。
func NewDefaultFactory() Factory {
	streams := core.SystemIOStreams()
	return &defaultFactory{
		ioStreams: streams,
		prompter:  core.NewHuhPrompter(streams.CanPrompt),
	}
}

// IOStreams 返回终端输入输出流。
func (f *defaultFactory) IOStreams() *core.IOStreams {
	return f.ioStreams
}

// Prompter 返回终端交互询问器。
func (f *defaultFactory) Prompter() core.Prompter {
	return f.prompter
}

// Config 懒加载并缓存强类型应用配置。
func (f *defaultFactory) Config() (*config.Config, error) {
	f.cfgOnce.Do(func() {
		f.cfg, f.cfgErr = config.Load()
	})
	return f.cfg, f.cfgErr
}

// DB 懒加载并缓存数据访问入口。
func (f *defaultFactory) DB(ctx context.Context) (*database.DB, error) {
	f.dbOnce.Do(func() {
		cfg, err := f.Config()
		if err != nil {
			f.dbErr = err
			return
		}
		pool, err := database.Open(ctx, cfg.Database, cfg.App.Name)
		if err != nil {
			f.dbErr = err
			return
		}
		f.db = database.NewDB(pool)
	})
	return f.db, f.dbErr
}

// Logger 懒加载并返回结构化 Zerolog 日志记录器。
func (f *defaultFactory) Logger() *zerolog.Logger {
	f.loggerOnce.Do(func() {
		cfg, err := f.Config()
		if err == nil {
			f.logger = logger.New(cfg)
		} else {
			f.logger = logger.Nop()
		}
	})
	return f.logger
}

// TestFactory 提供供纯内存单元测试使用的可定制工厂实现。
type TestFactory struct {
	StreamsValue  *core.IOStreams
	PrompterValue core.Prompter
	ConfigValue   *config.Config
	ConfigErr     error
	DBValue       *database.DB
	DBErr         error
	LoggerValue   *zerolog.Logger
}

func (f *TestFactory) IOStreams() *core.IOStreams {
	if f.StreamsValue == nil {
		s, _, _, _ := core.TestIOStreams()
		return s
	}
	return f.StreamsValue
}

func (f *TestFactory) Prompter() core.Prompter {
	if f.PrompterValue == nil {
		return core.NewMockPrompter()
	}
	return f.PrompterValue
}

func (f *TestFactory) Config() (*config.Config, error) {
	return f.ConfigValue, f.ConfigErr
}

func (f *TestFactory) DB(_ context.Context) (*database.DB, error) {
	return f.DBValue, f.DBErr
}

func (f *TestFactory) Logger() *zerolog.Logger {
	if f.LoggerValue == nil {
		return logger.Nop()
	}
	return f.LoggerValue
}
