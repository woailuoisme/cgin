# 0011. 敏感配置与密钥管理选型：SOPS + age GitOps 实践

## 上下文与决策背景

在 `bgin` 项目容器化与 Helm 部署体系演进过程中，生产与预发环境敏感凭证（如 `JWT_SECRET`、`DB_URL`、`REDIS_URL`、`AWS_*`、第三方 API Key）的管理面临两种主流路径的选型权衡：

1. **中心化外部密钥系统 (External Secrets Operator + OpenBao / Vault)**：
   - 依赖集群内运行的 ESO Controller 与中心化 OpenBao/Vault 实例；
   - 优势：具备动态短期凭证、细粒度租约、自动轮转与完备审计；
   - 劣势：运维复杂度与资源开销极高（OpenBao HA 部署、存储后端 PVC、解封机制 Unsealing、Operator 对账延迟与单点故障风险），对于当前自包含的独立 Web API 骨架而言属于显著过度设计。

2. **GitOps 原生密文同行体系 (SOPS + age)**：
   - 基于 Mozilla SOPS 与现代化高强度椭圆曲线加密工具 `age`（X25519）；
   - 优势：
     - **零集群守护进程**：无需在 Kubernetes 集群部署与维护任何 Controller 或数据库；
     - **密文随 Git 版本追溯**：环境密文（`secrets.<env>.enc.yaml`）通过受控公钥加密后安全入库，与应用代码发布严格原子绑定；
     - **精细化加密**：仅对敏感值字段（`secrets` 字典）加密，保留顶层 YAML 结构明文，便于代码审查与 Diff；
     - **多接收者支持**：公钥直接公开在 `.sops.yaml` 中，支持多名研发/运维公钥与 CI 部署公钥共同授权解密。

经过技术评审与权衡，决定采用 **SOPS + age** 作为本仓库生产秘钥管理的唯一官方标准，同时在 Helm Chart 保持中立设计（支持 `existingSecret`，未来可无缝接入 ESO）。

---

## 核心架构契约与落地规范

1. **环境覆盖与 Values 合并架构 (Overlay Structure)**：
   - 仓库提供未加密模板 [helm/bgin/secrets.example.yaml](file:///Users/seaside/Projects/go/gin-bun/helm/bgin/secrets.example.yaml) 作为参考事实源。
   - 各环境真实凭证以 `helm/bgin/secrets.<env>.enc.yaml` 格式保存为 SOPS 密文。
   - 文件内部格式严格对齐 `values.yaml` 中的 `secrets:` 字典，部署时通过 Helm 多文件合并参数 `-f values.yaml -f <(sops -d secrets.<env>.enc.yaml)` 无缝覆盖注入。

2. **声明式规则配置 (`.sops.yaml`)**：
   - 根目录设立统一的 [.sops.yaml](file:///Users/seaside/Projects/go/gin-bun/.sops.yaml)，配置路径正则匹配 `^helm/.*secrets\..*\.ya?ml$`。
   - 启用 `encrypted_regex: "^(secrets|stringData)$"`，确保元数据与非敏感字段透明可读。
   - 授权 age 公钥透明提交至 Git，私钥绝对禁止入库。

3. **双重防泄漏安全边界 (`.gitignore`)**：
   - [.gitignore](file:///Users/seaside/Projects/go/gin-bun/.gitignore) 严格拦截所有未加密明文文件：
     ```gitignore
     helm/**/secrets.*.yaml
     !helm/**/secrets.example.yaml
     !helm/**/secrets.*.enc.yaml
     *.key
     ```
   - 杜绝因开发者疏忽将未加密的 `secrets.prod.yaml` 或本地 `age.key` 意外提交进版本库。

4. **现代化本地工作流 (`justfile`)**：
   - `just sops-init`：自动探测并生成本地 age 密钥对至私有目录 `.local/age.key`（权限 600），并打印对应的公钥。
   - `just sops-encrypt [env]`：从未加密的 `secrets.<env>.yaml` 安全加密生成 `secrets.<env>.enc.yaml`。
   - `just sops-edit [env]`：通过系统 `$EDITOR` 原地解密-编辑-保存自动重新加密。
   - `just sops-view [env]`：终端只读查看解密后内容。
   - `just helm-template-env [env]`：利用 `sops exec-file` 在内存临时管道中解密并执行 `helm template`，全流程零明文落地。

5. **CI/CD 与 GitOps 集成方案**：
   - **CI 流水线 (CNB / GitHub Actions)**：在仓库 Secret 中配置 `SOPS_AGE_KEY`（私钥内容），流水线运行时注入环境变量，通过 `sops -d` 或管道向 `helm upgrade` 提供 values，集群端只接收标准 Kubernetes Secret。
   - **GitOps (ArgoCD / Flux)**：在集群端启用 `helm-secrets` 插件或 `argocd-vault-plugin`，挂载 age 私钥 Secret，ArgoCD 在拉取 Git 仓库生成 Manifests 时自动完成解密交付。
