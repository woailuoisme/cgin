package config

import (
	"cmp"
	"strings"
)

const (
	defaultTemporalHostPort    = "127.0.0.1:7233"
	defaultTemporalNamespace   = "default"
	defaultTemporalTaskQueue   = "bgin-tasks"
	defaultTemporalConcurrency = 10
)

// TemporalConfig 描述 Temporal 分布式工作流客户端与 Worker 协程参数。
type TemporalConfig struct {
	Enabled             bool   `mapstructure:"temporal_enabled"`
	HostPort            string `mapstructure:"temporal_host_port"`
	Namespace           string `mapstructure:"temporal_namespace"`
	TaskQueue           string `mapstructure:"temporal_task_queue"`
	ActivityConcurrency int    `mapstructure:"temporal_activity_concurrency"`
	WorkflowConcurrency int    `mapstructure:"temporal_workflow_concurrency"`
}

// validateTemporal 校验 Temporal 参数，为未配置字段赋予默认值。
func (c *Config) validateTemporal() error {
	c.Temporal.HostPort = cmp.Or(strings.TrimSpace(c.Temporal.HostPort), defaultTemporalHostPort)
	c.Temporal.Namespace = cmp.Or(strings.TrimSpace(c.Temporal.Namespace), defaultTemporalNamespace)
	c.Temporal.TaskQueue = cmp.Or(strings.TrimSpace(c.Temporal.TaskQueue), defaultTemporalTaskQueue)
	if c.Temporal.ActivityConcurrency <= 0 {
		c.Temporal.ActivityConcurrency = defaultTemporalConcurrency
	}
	if c.Temporal.WorkflowConcurrency <= 0 {
		c.Temporal.WorkflowConcurrency = defaultTemporalConcurrency
	}
	return nil
}
