package dto

import "cgin/pkg/ready"

const (
	// StatusReady、StatusDegraded 与 StatusNotReady 是就绪探针的状态值。
	StatusReady    = ready.StatusReady
	StatusDegraded = ready.StatusDegraded
	StatusNotReady = ready.StatusNotReady

	// CheckHealthy、CheckDegraded、CheckUnreachable 与 CheckDisabled 是依赖组件的状态值。
	CheckHealthy     = ready.CheckHealthy
	CheckDegraded    = ready.CheckDegraded
	CheckUnreachable = ready.CheckUnreachable
	CheckDisabled    = ready.CheckDisabled
)

// ReadinessResource 是 GET /readyz 的响应资源，完全兼容 ready.Result。
type ReadinessResource = ready.Result

// ComponentCheck 是单个依赖的探测状态元信息，完全兼容 ready.ComponentStatus。
type ComponentCheck = ready.ComponentStatus

type (
	ReadinessResponse = ReadinessResource
)
