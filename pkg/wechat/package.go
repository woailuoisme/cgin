package wechat

// 本文件保留作为 wechat 包的元信息与快速入口文档。
//
// 架构指南：
// 1. 全局装配：推荐使用 NewManager(cfg.WeChat, cacheClient) 构造多小程序管理器；
// 2. 领域消费：业务层可通过 mgr.Default() 或 mgr.App("user") 获取 MiniProgramClient；
// 3. 接口隔离：各子模块（Auth/Phone/WxaCode）提供独立契约接口（AuthService/PhoneService/WxaCodeService），便于单测与最小依赖注入；
// 4. 离线开发：单测与本地开发可使用 NewFakeClient() / NewFakeManager()，无需外部网络凭证。
