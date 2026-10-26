package bootstrap

import (
	"slices"

	"github.com/rs/zerolog"
)

// lifecycle 按登记顺序记录资源释放回调，关闭时严格逆序执行。
//
// 装配失败与运行期退出共用同一个栈：成功时交给 Application 复用，
// 避免维护第二份手写清单而漏关资源。
type lifecycle struct {
	log     *zerolog.Logger
	closers []closer
}

// closer 是带名称的释放回调，名称用于定位关闭失败的资源。
type closer struct {
	name string
	fn   func() error
}

// add 按资源创建顺序登记释放回调。
func (l *lifecycle) add(name string, fn func() error) {
	l.closers = append(l.closers, closer{name: name, fn: fn})
}

// setLogger 在日志器就绪后挂载，用于记录后续关闭失败。
func (l *lifecycle) setLogger(log *zerolog.Logger) { l.log = log }

// closeAll 逆序释放已登记资源；日志器未就绪时静默丢弃关闭错误。
func (l *lifecycle) closeAll() {
	for _, c := range slices.Backward(l.closers) {
		if err := c.fn(); err != nil && l.log != nil {
			l.log.Warn().Err(err).Str("resource", c.name).Msg("failed to close resource")
		}
	}
}
