# 02 系统架构与控制职责

> 来源：根目录 `docs/02-系统架构.md`。以下是目标架构，非当前已部署组件。接入现有结构见 [代码地图](../../requirements/references/repository-map.md)，冲突与未决项见 [融合约定](../../requirements/references/integration-decisions.md)。版本敏感能力需在实施时核实。

## 总体数据流
```
Prometheus ──HTTP API──> Collector/Feature Store ──> Algorithm Runners (并行插件)
                                                     │
                                                Backtest/Evaluation
                                                     │
                                               Decision Policy Engine
                                                     │
                            ┌────────────────────────┴──────────────────────┐
                            ▼                                               ▼
                    Resource Snapshot                                Scale Recommendation
                            │                                               │
                            │                                               │
            新 Pod requests/limits JSON Patch                         Deployment 副本
                            │                                               │
                原始配置 or 计算后配置                           Pod Ready / Node scheduling
                            └────────────────────────┬──────────────────────┘
                                                     ▼
                            Platform 内置 Metrics / GORM 数据存储
                                                     │
                                             Backtest 与结果回填
```

## 关键边界

业务与审计持久化复用现有 GORM ORM 和数据库配置，兼容 SQLite、MySQL、PostgreSQL，由部署配置选择。系统设置、认证授权、用户、角色、权限、菜单及操作日志沿用现有模型、DAO、API 和页面，只做必要扩展，不重复建设。
1. **不修改 kubelet**：kubelet 负责节点上执行 Pod 资源，不具备自定义准入入口。独立 Pod Resource Admission 服务经 API Server 的 MutatingAdmissionWebhook 处理，运行时资源修改走 `pods/resize` 或安全重建，需按集群版本与能力检测。Kubernetes 1.30 不得默认依赖 1.35 的稳定特性。
2. **执行副本的唯一所有者**：此项目自定义 `PredictiveScaler` CRD + Operator 是 HPA/KEDA 的替代品，独占目标工作负载的副本控制（内部经 scale 子资源写入）；如工作负载原有 HPA，必须在 Auto 前检查并拒绝双写，改为 Observe/Recommend 或明确迁移所有权；不让同一个 Deployment 同时被 HPA 和本 Operator 调副本。
3. **Platform 内置 Metrics**：暴露结果供 Prometheus、Grafana 和可选 HPA Adapter 使用；不以 Prometheus 抓取结果作为准入实时依赖。要切换原生 HPA 执行后端时，先停止自定义 Operator 的副本写入。
4. **准入执行边界**：Pod CREATE 变更实际 Pod spec，与 Deployment PodTemplate 可能不同；保存原配置、推荐 ID、快照时间及策略版本，防止配置漂移；必要时采用工作负载 Template 管理，但不得造成无穷滚动发布。
5. **动态资源调整**：本期强制覆盖创建时准入；运行中原地资源修改属于单独开关且版本敏感，优先只建议、不强制自动调低 Memory Limit。
6. Kafka 为异步计算/审计消息，不参与同步准入阻塞。快照存储应本地缓存加持久库，带 TTL 与签名/版本。

## 资源推荐优先级（高到低）
`全局紧急 bypass` → `Namespace/模块禁止更改` → `管理员固定周期覆盖` → `有效且通过风控的模型建议` → `模块 fallback 默认值` → `用户原始值`。
注意：当全局 bypass 时**始终保留用户原始值**；模块 fallback 用于已经启用平台控制但模型不可用的模块，若 fallback 本身校验失败则保留原始值。全程不能低于 Namespace LimitRange、超过 ResourceQuota 或破坏 requests<=limits。

## 预测周期与调度
时间粒度：15s/30s/1m/5m（按数据源能力）；历史窗口 1d/7d/30d/90d；horizon 1m/5m/15m/30m/60m。用于扩容的预测必须预留 Prometheus 抓取延迟、推理时间、控制循环、Pod P95 Ready 时间以及必要的 Node 启动时间。突发短于准备时间，采用事先保底 Ready Pod/容量，禁止声称模型可预测任意瞬时 burst。

## 建议模块/服务
- `platform`：认证、LDAP、管理、系统参数、算法/模块配置 API。
- `collector`：Prometheus 数据源、查询、聚合、采样质量。
- `feature-engine`：窗口特征（趋势、周期、分位数、波动性）。
- `algorithm-runtime`：多算法执行、版本管理、模型训练/推理。
- `decision-engine`：融合、成本/SLO 安全边界、固定周期与 fallback。
- `kube-resource-fcst-operator`（逻辑职责名，非现有目录）：自定义 CRD Operator，唯一负责目标工作负载副本执行与对账；不同时启用原生 HPA 写同一目标。
- Metrics：内置于 `platform`（复用 pkg/metrics），暴露给 Prometheus，不设独立 exporter 服务。
- `evaluation-worker`：时间穿越隔离回测、执行效果与预测准确率。

## 部署模式
观察（只做预测）→ 推荐（可看建议）→ 准入（新 Pod 修改 requests/limits）→ 自动（Operator 调 replicas）。独立功能开关及全局熔断；故障时退出 Auto 可禁缩容并保留保障副本。对已有业务初次上线必须有手工审阅阶段。
