# 03 数据、CRD、API 与 Metrics 契约

> 来源：根目录 `docs/03-数据与接口契约.md`。所有新增接口、表和事件为规划；先对照 [代码地图](repository-map.md) 复用现有身份/权限/存储，再依据 [融合约定](integration-decisions.md) 确定 API group、指标前缀与版本方案。

以下均为**本项目拟定的自定义接口设计**，不是 Kubernetes 原生已存在的 kind/field。

## 自定义 CRD
- `PredictiveScaler`（建议组 `autoscaling.example.io/v1alpha1`，实施时替换自有域名）：spec 包含 targetRef、moduleRef、mode、minReplicas、maxReplicas、fallbackReplicas、evaluationInterval、cooldown、scaleUp/Down policies、安全界限；status 包含 selectedAlgorithm、modelVersion、desired/current/ready、lastDecision、recommendationValidUntil、conditions、reason。
- `PredictiveResourcePolicy`：Workload/Container 匹配、推荐模式、原始配置保存策略、默认 requests/limits、最小最大资源、固定周期覆盖引用、禁用和 bypass。
- `AlgorithmProfile`：算法 ID、schemaVersion、参数 JSON、特征映射、版本、启用状态和验证结果。
注意正式实现要通过 Kubebuilder 生成结构体、OpenAPI v3 Schema、RBAC、CRD manifests、转换/兼容测试；禁止把示意 YAML 当作 Kubernetes 已安装 CRD。

## REST API（已实现部分与规划）
已实现（均在 /api/v1 下，受 JWT/Casbin/操作日志保护；ADMIN 经通配权限使用）：
- 数据源：`GET/POST /datasources`、`GET /datasources/options`、`GET/PUT/DELETE /datasources/{id}`、`POST /datasources/batch-delete`、`POST /datasources/{id}/test`。
- 指标映射：`GET/POST /metric-mappings`、`GET/PUT/DELETE /metric-mappings/{id}`、`POST /metric-mappings/batch-delete`、`GET /metrics/catalog`。
- 算法：`GET /algorithms`、`GET /algorithms/{id}/schema`（参数 schema 与校验见 pkg/forecast/registry.go；`dry-run` 未实现）。
- 模块：`GET/POST /modules`、`GET/PUT/DELETE /modules/{id}`；算法实例与来源见下；固定周期 `GET/POST /modules/{id}/overrides`、`PUT/DELETE /modules/{id}/overrides/{pid}`。创建/更新只开放 observe、recommend 模式，admission、auto 被拒绝。
- 流水线：`GET /modules/{id}/history?metric=&from=&to=`（样本+质量报告）、`POST /modules/{id}/collect|predict|backtest|recommend`、`GET /modules/{id}/backtests`、`GET /recommendations?module_id=`、`GET /predictions?module_id=&metric=&algorithm_id=&from=&to=`。observe 模式调用 recommend 返回 409。
- 指标来源：`GET/POST /modules/{id}/sources`、`PUT/DELETE /modules/{id}/sources/{sid}`。来源 = 类型（cpu、memory、gpu、gpu_memory、rps、replicas、network_rx/tx、custom、OOM/JVM/Kubernetes 类）+ 指标映射 + 作用资源（cpu/memory/gpu/replicas/signal）+ 优先级、权重、统计方式（avg/p50/p90/p95/p99/max/sum/last）、取值依据（forecast 预测值 / history 实测窗口）、阈值；系列键 = 类型，同类型第二条起为 kind_2…（创建后不变，历史与预测按它归档）；类型与映射的指标种类必须一致，仍被算法实例使用的来源不可删。同一资源多个来源的合并方式在模块 `combine`（weighted 默认 / max / sum / primary）。
- 算法实例：`GET/POST /modules/{id}/algorithms`、`PUT/DELETE /modules/{id}/algorithms/{bid}`、`GET /modules/{id}/algorithms/{bid}/trace`（请求体 name、algorithm_id、params、sources、horizon_minutes、weight、history_days、train_budget_seconds、priority、enabled）；`POST /modules/{id}/score`、`GET /modules/{id}/ranking`、`GET /algorithm-scores`（评分历史，module_id、algorithm、selected、from、to、sort）；`GET /admissions`（准入决策记录：module_id、applied、keyword、from、to）。已删除 `/algorithm-templates` 与 `/input-sets`。
- 列表与筛选（均服务端分页，`sort_by`/`sort_order` 走白名单）：`GET /recommendations`（module_id、mode、version、source、degraded、oom_floor、from、to）、`GET /scaling-decisions`（module_id、result、action、keyword、from、to）、`GET /fallbacks`（降级与回退记录：reasons、notes、recovered_at、duration_seconds、ongoing；reason 为 no_data / stale_data / insufficient_history / predictor_failed / no_prediction / stale_prediction / no_eligible_model / recovering / low_score / short_history / predict_timeout / oom_floor）、`GET /backtests`（跨模块回测，promoted 等）、`GET /evaluations`（from、to、sort）。`GET /modules` 每行带 `summary`（来源/实例数量、最新推荐与是否降级、连续失败次数）。模块历史对尚未采集的指标返回 200 与 `not_collected: true`。
- 控制：`POST /controls/{id}/bypass`（reason 必填）、`POST /controls/{id}/resume`，id 为模块 ID；经操作日志中间件审计。

- 评估与审计：`POST /modules/{id}/evaluate`、`GET /modules/{id}/leaderboard?metric=&since_hours=`、`GET /evaluations`、`GET /scaling-decisions`（operator 上报）。审计导出接口 `GET /audit/export` 与页面已在问题修复 R3 中删除；记录页只保留列表内「导出所选」（浏览器端 CSV）。
- 机器接口（开发者凭据，各有独立权限）：`GET /operator/decisions?module=`（replicas、算法与评分、有效期，以及 min_replicas 硬下限、max_replicas 当前有效上限与 hard_max_replicas 安全硬上限；operator 对超出者收回）、`POST /operator/reports`、`GET /admission/recommendations?namespace=&workload=&container=[&cpu_milli=&memory_bytes=&limit_*]`（回答含 CPU/内存 requests 与 GPU 卡数，GPU 只对新 Pod 生效；每个回答，含拒绝，写入准入决策记录）。admission/auto 模式需系统设置 forecast.allow_admission_mode / forecast.allow_auto_mode 为 true，且每次回答都会检查。
- LDAP（仅管理员）：`GET/PUT /ldap/settings`（含 `group_role_mappings`，每行 `目录组=角色编码`，保存时校验角色存在）、`POST /ldap/test`、`POST /ldap/sync`；配置存为 `ldap.*` 系统参数（不出现在通用系统参数接口），密码与 CA 加密存储、读取不回显。组映射独立的 `/ldap/mappings` 接口已移除。
- 指标端点（metrics.listen）：`/metrics`、`/healthz`、`/readyz`。

规划、尚未实现：`POST /algorithms/{id}/dry-run`、Kyverno 之外的准入 Webhook。

## Metrics 设计（自定义，禁止高基数标签）
```
predictive_module_prediction_value{namespace="production",workload="api",metric="rps",horizon="15m",quantile="0.9"} 2100
predictive_module_desired_replicas{namespace="production",workload="api"} 11
predictive_module_fallback_active{namespace="production",workload="api",reason="insufficient_data"} 0
predictive_module_override_active{namespace="production",workload="api",rule="holiday"} 1
predictive_module_recommendation_age_seconds{namespace="production",workload="api"} 18
predictive_module_cpu_request_cores{namespace="production",workload="api",container="main"} 0.5
predictive_module_memory_limit_bytes{namespace="production",workload="api",container="main"} 10737418240
predictive_module_prediction_mae{namespace="production",workload="api",metric="rps",horizon="15m",model="gbdt"} 120
predictive_module_prediction_coverage{namespace="production",workload="api",metric="rps",quantile="0.9"} 0.89
predictive_operator_scale_actions_total{namespace="production",workload="api",direction="up",result="success"} 17
```
指标名称全部为平台拟定，实现须统一命名与 HELP/TYPE，避免 `run_id`, `pod_uid`, `timestamp`, `request_id` 出现在 Metrics 标签中；这些在 DB 中保存。

## GORM 数据模型与三数据库兼容
持久化统一复用现有 GORM ORM、pkg/store 数据库配置与显式迁移，兼容 SQLite、MySQL、PostgreSQL，由部署配置选择；不要求只使用 PostgreSQL。字段、JSON 存储、查询、索引和迁移须考虑三种数据库差异，不无适配地依赖某一种方言。

系统设置复用现有 SysConfig（表 sys_config），用户、角色、权限/菜单复用 User、Role、Permission，操作日志复用 OperationLog（表 operation_log）；沿用既有 DAO、API、页面、Casbin 与菜单权限关系。上述基础模块不重新建表，不另建 system_settings 或 operation_logs 平行替代。

新增预测领域模型按需规划：`ldap_mappings`（仅补充目录映射）、`datasources`、`metric_mappings`、`workloads`、`algorithms`、`algorithm_versions`、`algorithm_parameters`、`modules`、`module_algorithm_bindings`、`module_fallbacks`、`module_overrides`、`feature_snapshots`、`predictions`、`prediction_evaluations`、`resource_recommendations`、`admission_audits`、`scaling_decisions`、`scaling_executions`、`backtest_runs`、`model_artifacts`、`audit_events`。这些名称是设计标识，实际模型与表名按现有命名、注册和迁移约定确定。

关键唯一约束：`predictions` 用 `module_id + metric + prediction_time + target_time + model_version + quantile`；算法评估不得覆写原预测；决策与执行用 `decision_id` 关联；所有时间按 UTC 时间戳存储，规则时区单独字段；原始请求资源与应用后资源以结构化 JSON 保存，不可只保留差量。

## Kafka 事件（平台自定义 topic）
`predictive.features.ready`, `predictive.forecast.created`, `predictive.recommendation.validated`, `predictive.admission.applied`, `predictive.scaling.decided`, `predictive.scaling.executed`, `predictive.forecast.evaluated`, `predictive.alerts`。消费者幂等、重试、DLQ、schema version、trace_id。**Webhook 审计采用非阻塞 outbox/本地缓冲，Kafka 不可用不得中断 Admission**。

## 控制与安全规则
- Operator 计算 `desiredReplicas`，再通过目标工作负载 `scale` subresource 提交，并按 API 实际结果记录成功/失败；不会直接指定单个 Pod 创建。
- 对仅以 Service RPS 观测到的业务：记录 aggregate 级别，不把 aggregate/replicas 标记成每 Pod 的真值。
- 保护工作负载的 PDB、节点容量、配额、用户原始请求和最低容量；控制器重启可恢复决策状态。
- 接口支持版本演进、错误码、幂等键和最小权限认证。
