---
trigger: always_on
description: 核心 MCP 工具链（context7, exa, tavily, codegraph, ast-grep）通用选型与协同规范
---

# 核心 MCP 工具链通用规范 (MCP Tools Standard)

本规范明确了在项目中集成与调用的五大核心 MCP 工具（`context7`、`exa`、`tavily`、`codegraph`、`ast-grep`）的职责边界、选型矩阵与协同准则。

---

## 1. 核心工具选型决策树 (Tool Selection Matrix)

| 工具名称 | 核心定位与能力 | 强制首选场景 | 禁用与降级场景 |
| :--- | :--- | :--- | :--- |
| **`codegraph`** | 本地代码知识图谱（SQLite 索引） | **探索本地代码拓扑、跨文件调用链（Caller/Callee）、重构影响面（Blast Radius）**。一次调用替代反复的 `grep` + `view_file` | 模糊文本匹配、未被索引的临时文件 |
| **`ast-grep`** | 语法级 AST 分析、模式检索与规则测试 | **AST 语法检索、函数/结构体签名提取、语法迁移规则测试与批量重构**。严禁正则改写代码 | 纯文本搜索、跨项目外部技术调研 |
| **`context7`** | 第三方库与开源框架权威实时文档 | **查询第三方开源库/框架官方文档、API 签名与配置示例**。杜绝依赖陈旧知识库与臆测参数 | 查询项目私有业务代码、查询语言内置基础语法 |
| **`exa`** | 神经语义网页搜索与高质文章提取 | **架构设计范式、前沿技术博文、开源生态模式对比、深度技术方案调研** | 简单的实时新闻、查询已知库的标准 API 用法（应优先用 context7） |
| **`tavily`** | 面向 LLM 优化的实时搜索引擎与正文抓取 | **实时时效性资讯、开源库最新 Bug/Issue/CVE 讨论、Breaking Changes、指定 URL 深度提取** | 本地代码检索、查询成熟框架官方 API 文档 |

---

## 2. 工具调用契约与最佳实践

### 2.1 `codegraph` (本地代码拓扑与影响分析)

- **调用工具**: `codegraph_explore`
- **核心契约**:
  1. **必须显式传参 `projectPath`**: 传入当前工作区根目录的绝对路径。
  2. **视同已读原则 (Read-Equivalent)**: 返回的行号源代码已被加载到上下文，**严禁再次调用 `view_file` 重复读取同一代码段**。
  3. **高密度查询**: `query` 既支持自然语言问题（如 `"how does transaction middleware propagate context in auth service"`），也支持核心符号组合（如 `"AuthService Login database.GetDB"`）。通常 1~2 次调用即可完成拓扑探查。

### 2.2 `context7` (第三方依赖官方文档)

- **调用工具**: `resolve-library-id` ➜ `query-docs`
- **核心契约**:
  1. **两阶段标准流**: 必须先调用 `resolve-library-id`（提供 `libraryName` + `query`）获取标准 ID，再通过 `query-docs` 检索具体主题。
  2. **单主题聚焦**: `query` 必须聚焦具体概念（如 `"JWT middleware token parsing"`），避免空泛（如 `"auth"`）。
  3. **频次限制**: 每个任务内调用 `query-docs` 严禁超过 3 次；未找到时结合 `exa` 或 `tavily` 补充。

### 2.3 `exa` (深度技术语义搜索与抓取)

- **调用工具**: `web_search_exa`、`web_fetch_exa`
- **核心契约**:
  1. **描述理想页面**: `web_search_exa` 的 `query` 应为语义化描述（如 `"idiomatic service layer pattern with fiber and sqlc in Go"`），而非孤立关键字。
  2. **深度提取**: 当摘要信息不足以支撑架构决策时，挑选高价值 URL 调用 `web_fetch_exa` 提取干净 Markdown 正文。

### 2.4 `tavily` (实时时效性搜索与 Issue 排查)

- **调用工具**: `tavily-search`、`tavily-extract`
- **核心契约**:
  1. **实时与精确排查**: 用于排查最新版本的 Bug、Breaking Changes、CVE 漏洞或 GitHub 讨论。
  2. **按需配置深度**: 深度研判使用 `search_depth: "advanced"` 并可限定权威域名（如 `include_domains: ["github.com"]`）；提取指定页面正文使用 `tavily-extract`。

### 2.5 `ast-grep` (AST 结构分析与精准重构)

- **调用工具**: `dump_syntax_tree`、`find_code`、`find_code_by_rule`、`test_match_code_rule`
- **核心契约**:
  1. **MCP + CLI 闭环协作**: 在智能体回合内使用 MCP 工具转储 CST、检索模式、测试 YAML 规则；测试无误后通过终端 CLI（`rtk ast-grep run/scan --update-all`）批量应用。
  2. **关系型规则闭合**: 在编写带有 `inside` / `has` 关系的嵌套规则时，**必须显式指定 `stopBy: end`** 以保证完整遍历。
  3. **绝对红线**: 严禁使用正则批量改写代码，严禁使用单文件替换工具轮询逐个修改相同模式代码。

---

## 3. 标准四步协同工作流 (Standard 4-Step Flow)

```text
1. [查阅规范] → context7 (查官方最新依赖 API 与配置规范)
2. [深度调研] → exa / tavily (搜开源工业实践方案或排查时效性 Issue)
3. [感知拓扑] → codegraph (定位本地代码调用链、模块入口与波及面)
4. [精准重构] → ast-grep (MCP 沙盒验证规则 ➜ CLI 批量安全应用)
```
