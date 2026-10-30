# kube-resource-fcst 代码接入地图

路径以仓库根目录为基准，来自融合时源码检查，实施仍需核对最新代码。研究组件名不代表已存在的目录或服务。

| 领域 | 当前实现 / 最近示例 | 接入规则 |
|---|---|---|
| 入口 | cmd/main.go（平台）、cmd/operator/main.go（kube-resource-fcst-operator）；pkg/cmd | Cobra + bootstrap 生命周期，复用配置 |
| HTTP | pkg/app/app.go、pkg/app/router/router.go | Gin /api/v1，显式构造 handler 注入 DAO；受保护路由在 JWT/Casbin/操作日志中间件后注册 |
| 分层 | pkg/apis → pkg/app/server/api → pkg/dao/rdb → pkg/model | 找最近 CRUD 扩展；算法/控制逻辑不能塞进 handler |
| 数据库 | pkg/store/rdb_store.go、pkg/migration/migration.go | 已支持 SQLite/MySQL/PostgreSQL；模型注册并接显式 --migration/--upgrade，普通启动不做破坏迁移 |
| 身份权限 | handle_auth.go、handle_user.go、handle_role.go、handle_permission.go；pkg/app/middleware | 复用 JWT/会话/Casbin/审计；LDAP 尚待集成 |
| 菜单 | pkg/model/permission.go、handle_route.go、pkg/migration/migration.go | 当前白名单为 system/authorization/task-center/datasource/forecast/forecast-results/forecast-actions/monitoring 与隐藏个人页；旧菜单码（forecast_evaluation、forecast_recommendations、forecast_history、forecast_metrics、authorization_ldap）由 pkg/migration/upgrade_menus_ldap.go 的 menuSuccessors 把授权映射到新码后退役；新增需同步白名单、种子、前端路由和本地化 |
| 任务 | pkg/job/scheduler.go、pkg/job/tasks/registry.go | 复用持久化、claim/heartbeat/retry；目前仅注册 notifytask，新预测/回填独立注册 |
| 数据源 | pkg/model/datasource.go、metric_mapping.go；pkg/promclient；handle_datasource.go、handle_metric_mapping.go；web/src/views/datasource | P1-01 已实现连接管理与指标映射，复用其扩展 P1-02 采集；Prometheus 访问统一经 pkg/promclient |
| 预测流水线 | pkg/collector（模板渲染、分段、质量）、pkg/forecast（算法与参数 schema）、pkg/backtest（滚动回测与晋升）、pkg/decision（推荐决策，纯函数）、pkg/forecastsvc（编排、实测回填、排行榜、Prometheus 指标）；pkg/model/forecast.go；pkg/dao/rdb/forecast_module.go、forecast_data.go；handle_forecast.go、handle_runtime.go、handle_audit.go；pkg/job/tasks/forecasttask | 新增算法在 forecast 包实现并注册，保持无状态、可取消，配单测与回测断言；定时任务 forecast.run/backtest/evaluate 由迁移种子创建 |
| Operator | pkg/operator/controller（`MakePlan` 纯函数、`Applier` 读目标/问决策/写副本、`ModuleSync` 按平台模块逐轮评估、`PlatformDecider`/`CachingDecider`、`SettingsStore`/`SettingsRefresher`、`Metrics`）、pkg/cmd/operator；deploy/operator（RBAC、manager、monitoring）、deploy/helm/kube-resource-fcst-operator | 没有 CRD（2026-10-31 删除）；规则改动同步 plan_test.go；只用 fake client 与假平台测过 |
| 准入 | deploy/kyverno/kube-resource-fcst-requests.yaml、handle_runtime.go 的 Admission | Kyverno 策略为未验证草稿；接口只回 requests |
| LDAP | pkg/ldapauth（Directory 接口、go-ldap 客户端、store.go 读写 ldap.* 系统参数、Service：登录/映射/同步）、pkg/model/ldap.go、handle_ldap.go、pkg/job/tasks/ldaptask；web/src/components/business/ldap-settings-panel.vue，在系统管理→系统参数的 LDAP 分类里显示 | 本地账号永远本地认证；明文 ldap:// 被拒绝；测试服务器见 pkg/ldapauth/testserver_test.go |
| 预设与演示 | pkg/preset（只读 PromQL 预设库，含来源/限制，均标未验证）、pkg/demo（合成数据生成器，识别预设的序列名）、handle_preset.go；预设在指标模板表单里挑选（R21 删除了「内置指标预设库」页面）。**一键演示已于 2026-10-30 删除**（接口 /forecast-demo、模块列表上的按钮、forecastsvc/demo.go 都没有了）：一个由合成数据生成的 demo-shop 模块混在正式模块里容易被当成真实工作负载；pkg/demo 只剩给预设单测用，`--migration`/`--upgrade` 由 pkg/migration/retire_demo.go 删除遗留的 demo-shop 模块、demo-synthetic 数据源及其映射 | 保留 type=demo 数据源的查询分支只为迁移前的库不报错；映射保存 preset_id |
| 算法框架与数据集 | pkg/forecast/{registry,builders,formula,custom}.go（注册表、内置实现、公式引擎、自定义算法注册）、pkg/collector/shape.go（栅格聚合/换算/精度）、forecastsvc/{custom,requirements,metricscore}.go、handle_{algorithm_custom,dataset,training,scoring}.go、model/training.go；web/src/components/{advanced/{duration-input,param-schema-editor},business/{custom-algorithm-form,formula-preview,score-breakdown,record-detail-modal}}.vue、views/monitoring/datasets | 算法只经 forecast.Register 进入平台；公式在保存前真实试算；数据集栅格与来源形态变化会重建序列；见 requirements/references/algorithm-framework-spec.md |
| 算法中心与指标 | handle_algorithm_center.go（使用情况、追溯、评分、排名、准入记录）、forecastsvc/catalog.go（指标目录，含 JVM/OOM/Kubernetes 用量指标与 recommendation_degraded / recommendation_oom_floor）、query.go（查询）、replicas.go + pkg/decision/solver.go（动态副本求解）；web/src/views/{forecast/algorithms,forecast/score-records,forecast/ranking,monitoring/metrics}、components/business/replica-explain.vue | 目录是历史/预测/表现/执行指标的唯一来源；/metrics、查询接口与目录由 catalog_test.go 对账 |
| 登录日志 | pkg/model/login_audit_log.go、dao/rdb/login_audit_log.go、handle_login_log.go、login_guard.go（锁定阈值/窗口读 sys.auth.login_failure_*）、cmd/server/operation_log_retention.go；web/src/views/system/login-log | 每次登录尝试一行（方式、原因、UA），保留天数 sys.login_log.retention_days |
| 前端预测页 | web/src/views/forecast-modules/modules、monitoring/datasets、web/src/views/forecast/{algorithms,score-records,ranking}、forecast-results/{history,backtests,evaluations,leaderboard,runs,algorithm-runs}、forecast-actions/{recommendations,executions,fallbacks,admissions}、monitoring/metrics；共享件 components/business/{list-search-card,list-table-panel,metric-series-chart,recommendation-explain}.vue、hooks/business/{use-paged-list,use-forecast-modules}.ts、components/advanced/algorithm-params-editor.vue、service/api/forecast.ts | 列表页用 ListSearchCard + ListTablePanel + usePagedList；参数编辑器由 /algorithms 的 schema 驱动；浏览器验收脚本 scripts/e2e/{multimetric,optimization,platform}.mjs |
| 多指标、评分与保护 | pkg/model/metric_source.go（MetricSource、AlgorithmScore、AdmissionDecision）、module_run_state.go、pkg/multimetric（每来源统计→需求/阈值比→按资源合并→加权压力，表驱动单测）、pkg/scoring（回放窗口的副本/CPU/内存/GPU 准确度、OOM 扣分、完整度→0-100 综合分）、forecastsvc/{inputs,score,recommend,protect}.go（来源装载、评分、按分数×权重排名与切换滞后、资格门槛、动态上限 expandCeiling、降级原因码）、dao/rdb/{metric_source,run_state,forecast_audit}.go、pkg/migration/forecast_sources.go（旧绑定→实例、旧映射槽→来源，退役模板与输入集表） | 选择：评分≥min_score 且预测新鲜且已晋升（基线除外）；领先者需超过 switch_margin 并连续 switch_cycles 次评估才取代在用者；降级按字段独立（bypass > 固定周期 > 算法 > 模块 Fallback > 保持原值），OOM 地板对除 bypass 外所有来源生效；恢复需 recover_cycles 个连续健康周期；最大副本数 dynamic 时有效上限每周期至多上调 expand_step_ratio、需评分≥expand_min_score、永不超过 hard_max_replicas |
| 前瞻副本决策 | pkg/decision/ahead.go（提前量、窗口、可信度加权共识、结算、效果判定，纯函数）、pkg/forecastsvc/ahead.go（窗口求解、实测负载、效果记录与评价）、model/scale_outcome.go、dao/rdb/scale_outcome.go、handle_runtime.go（urgent、/scale-outcomes）、operator plan.go（urgent）；web components/business/{ahead-explain,scale-outcome-modal}.vue | 系统参数 forecast.lead_*、consensus_*、emergency_scale_up；见 docs/02-系统架构.md「前瞻性副本决策」 |
| 效果报告与变更预览（P8 B1、B2） | pkg/forecastsvc/{effect,preview}.go、pkg/decision/effect.go（超配、反复扩缩、Pod·小时与容量 SLO，纯函数）、pkg/dao/rdb/effect.go、handle_module_data.go 的 ModuleEffectReport / ModuleChangePreview、路由 /modules/:id/effect-report 与 /change-preview；web views/forecast-results/effect、components/business/change-preview-modal.vue；菜单码 forecast-results_effect（pkg/migration 种子） | 只读已有表，不写预测、推荐、训练记录；口径与差别见 requirements/references/product-closure-p8.md |
| 首页 dashboard | pkg/forecastsvc/dashboard.go（模块计数、需要关注的模块与旗标、operator 已执行的扩缩容按时间段累计）、pkg/dao/rdb/dashboard.go（固定条数的读取）、handle_module_data.go 的 Dashboard、路由 GET /dashboard；web views/dashboard；菜单码 dashboard（pkg/migration 种子），GetUserRoutes 在有它时把落地页设为 dashboard | 只读，不写任何表；旗标：连续失败 ≥3 次、结果降级、推荐已过期、应有推荐却从未产生、已旁路；观察模式与不预测的模块不因“没有推荐”被标记 |
| 指标 | pkg/metrics/metrics.go、pkg/forecastsvc/metrics.go | Registry/Factory；metrics.listen 提供 /metrics、/healthz、/readyz；预测状态由 forecastsvc.Collector 在抓取时读库（缓存 10 秒，标签仅 module/metric/algorithm/resource/source） |
| 配置 | pkg/config、handle_sys_config.go、handle_secret_key.go | 复用 Viper/KUBE_RESOURCE_FCST_ 环境变量和密钥保护；未来集群凭据另定义 Secret 接入 |
| UI | web/src/views；web/package.json | Vue3/TypeScript/Naive UI/Pinia/i18n/ElegantRouter，沿用页面模式 |
| 页面示例 | web/src/views/authorization/user/index.vue、web/src/views/system/config/index.vue、web/src/views/task-center/definition/index.vue、web/src/views/datasource/metric-mappings/index.vue | 找最近列表/表单，不独立重设计 |
| 表格 | web/src/components/advanced/expandable-data-table.vue、table-header-operation.vue、table-column-setting.vue；web/src/hooks/business/use-table-column-config.ts | 复用用户列配置；大数据分页、检索、排序在服务端 |
| 前端接口 | web/src/service/api、web/src/typings/api | 保持请求封装和 DTO 序列化一致 |
| 路由文案 | web/src/router/elegant、web/src/router/routes、web/src/locales/langs | 使用现有路由生成机制，同步中英文与权限 |
| 实时刷新 | web/src/hooks/business/use-stats-stream.ts、handle_stats_stream.go | 新事件同步后端 topic 允许集，预测 topic 尚不存在 |

表中的 handle_*.go 均位于 pkg/app/server/api。当前顶层入口是数据源、预测、系统管理、授权管理、任务中心；研究新导航是后续增量，技能融合不改变运行菜单。

## 待建设能力

模型产物持久化、预测业务指标之外的执行效果评估（Pod-hours、SLO）、Prometheus/Adapter 决策来源、Admission Webhook 之外的自研准入（现采用 Kyverno，策略未验证）均待建设；真实 Prometheus、真实集群、Kyverno、MySQL/PostgreSQL、真实 LDAP 与真实负载上的验证均未完成。go.mod 中 Kafka/Redis 依赖不证明预测查询/事件管线已实现，现有消息适配不是指标采集器。

数据库沿用已实现的 GORM ORM 和现有配置，兼容 SQLite、MySQL、PostgreSQL，由部署选择，不强制迁移到 PostgreSQL。系统设置、认证授权、用户、角色、权限、菜单和操作日志复用既有完整链路，不重建。Kafka 和 controller-runtime 等规划能力按需求接入；不能为文档一致性重写现有持久化或删除管理能力。新包路径在实施中确定，不伪装为现有代码。

## 验证入口

- 后端：受影响包 go test；全局接线变动再执行 go test ./...。
- 前端：web 中 pnpm typecheck、pnpm test:unit、pnpm build，按范围选择。
- 端到端：scripts/prepare-platform-qa.py、scripts/e2e/platform.mjs，先读参数和临时目录行为，在隔离数据运行。
- web 是独立 Git 仓库，根仓库忽略 /web/；跨端变更分别检查。文档修改不要求重新编译应用。
