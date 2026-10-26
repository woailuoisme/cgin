package bootstrap

import (
	"slices"

	"github.com/rs/zerolog"
)

// lifecycle 按登记顺序记录资源释放回调，关闭时严格逆序执行。
//
// 装配失败与运行期退出共用同一条关闭路径：NewApplication 出错时立即 closeAll，
// 成功时把同一个栈交给 Application，由 Close 在进程退出时复用。两份手写清单合一后，
// 就不存在「装配期少关一个资源」这类漂移——例如分布式锁由 cache.Client.Close 内部释放，
// 关闭顺序也天然满足依赖倒置（通知引擎先于队列与邮件，队列先于事件总线）。
type lifecycle struct {
	log     *zerolog.Logger
	closers []closer
}

// closer 是带名称的释放回调，名称用于定位关闭失败的资源。
type closer struct {
	name string
	fn   func() error
}

// add 登记资源释放回调；调用方按资源创建顺序登记即可。
func (l *lifecycle) add(name string, fn func() error) {
	l.closers = append(l.closers, closer{name: name, fn: fn})
}

// setLogger 在日志器就绪后挂载，用于记录后续关闭失败。
func (l *lifecycle) setLogger(log *zerolog.Logger) { l.log = log }

// closeAll 逆序释放已登记资源。
//
// 日志器在 telemetry 之后才就绪，装配早期失败时 l.log 仍为空，此时静默丢弃关闭错误。
func (l *lifecycle) closeAll() {
	for _, c := range slices.Backward(l.closers) {
		if err := c.fn(); err != nil && l.log != nil {
			l.log.Warn().Err(err).Str("resource", c.name).Msg("failed to close resource")
		}
	}
}
