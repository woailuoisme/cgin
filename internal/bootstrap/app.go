// Package bootstrap 负责应用依赖装配与生命周期管理。
package bootstrap

import (
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"cgin/internal/config"
	"cgin/pkg/eventbus"
)

// Application 聚合装配完成的 HTTP 引擎与资源关闭栈。
//
// 只保留运行期真正被读取的依赖：数据库、缓存、邮件、队列与通知引擎都仅在退出时
// 需要释放，统一交由 resources 登记，不再挂只写不读的字段。
type Application struct {
	cfg       *config.Config
	logger    *zerolog.Logger
	eventbus  *eventbus.Manager
	engine    *gin.Engine
	resources *lifecycle
}

// Engine 暴露已装配的路由引擎，供集成测试直接驱动请求。
func (a *Application) Engine() *gin.Engine { return a.engine }

// Close 逆序释放装配期登记的全部外部资源。
func (a *Application) Close() { a.resources.closeAll() }
