package ready

import (
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/samber/lo"
)

// 全局就绪状态与组件健康度常量。
const (
	StatusReady    = "ready"
	StatusDegraded = "degraded"
	StatusNotReady = "not ready"

	CheckHealthy     = "healthy"
	CheckDegraded    = "degraded"
	CheckUnreachable = "unreachable"
	CheckDisabled    = "disabled"
)

// ComponentStatus 描述单个被测组件的健康状态与元信息。
type ComponentStatus struct {
	Status   string `json:"status"`
	Driver   string `json:"driver,omitempty"`
	Latency  string `json:"latency,omitempty"`
	Critical bool   `json:"critical"`
}

// Result 聚合全部探测组件的就绪评估结果。
type Result struct {
	Status string                     `json:"status"`
	Checks map[string]ComponentStatus `json:"checks"`
}

// IsReady 返回当前服务是否具备对外承接流量的能力（ready 或 degraded 均视为就绪）。
func (r Result) IsReady() bool {
	return r.Status != StatusNotReady
}

// StatusCode 返回与评估结果匹配的标准 HTTP 状态码（就绪返回 200，未就绪返回 503）。
func (r Result) StatusCode() int {
	return lo.Ternary(r.Status == StatusNotReady, http.StatusServiceUnavailable, http.StatusOK)
}

type resultReducer struct {
	mu      sync.Mutex
	results map[string]ComponentStatus
	overall string
}

func newResultReducer() *resultReducer {
	return &resultReducer{
		results: make(map[string]ComponentStatus),
		overall: StatusReady,
	}
}

// escalateOverall 依据检查项关键性升级整体状态。调用方必须持有 r.mu。
func (r *resultReducer) escalateOverall(critical bool) {
	if critical {
		r.overall = StatusNotReady
		return
	}
	if r.overall == StatusReady {
		r.overall = StatusDegraded
	}
}

// failureStatus 返回单项失败状态：关键项为 unreachable，非关键项为 degraded。
func failureStatus(critical bool) string {
	return lo.Ternary(critical, CheckUnreachable, CheckDegraded)
}

func (r *resultReducer) addChecked(check Check, err error, latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	status := CheckHealthy
	if err != nil {
		r.escalateOverall(check.Critical)
		status = failureStatus(check.Critical)
	}

	latencyStr := lo.Ternary(latency >= 0, latency.Round(time.Microsecond).String(), "")

	r.results[check.Name] = ComponentStatus{
		Status:   status,
		Driver:   check.Driver,
		Latency:  latencyStr,
		Critical: check.Critical,
	}
}

func (r *resultReducer) addDisabled(check Check) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.results[check.Name] = ComponentStatus{
		Status:   CheckDisabled,
		Driver:   check.Driver,
		Critical: check.Critical,
	}
}

func (r *resultReducer) snapshot() Result {
	r.mu.Lock()
	defer r.mu.Unlock()

	copied := make(map[string]ComponentStatus, len(r.results))
	maps.Copy(copied, r.results)

	return Result{
		Status: r.overall,
		Checks: copied,
	}
}
