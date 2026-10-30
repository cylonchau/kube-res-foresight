---
name: control-plane
description: 用于 kube-resource-fcst Operator、副本执行和 Platform Prometheus Metrics 的设计实现及审查，检查副本唯一控制者和故障恢复。
---

# 控制面与副本执行

先读 [代码地图](../requirements/references/repository-map.md)、[架构职责](references/architecture.md) 和 [共享契约](../requirements/references/contracts.md)。Operator 已实现，没有 CRD（已于 2026-10-31 移除）。

## 实施流程

1. 按目标集群版本和能力发现选择 Kubernetes SDK/controller-runtime。operator 不在集群里创建任何自定义资源，不发布 API group。
2. 交付 RBAC、manifests 和兼容测试；不能把研究字段当 Kubernetes 原生字段。
3. Auto 前确认副本控制权（由 kube-resource-fcst-operator 取代 HPA/KEDA）；存在 HPA/KEDA 或其他写入控制器时拒绝双写并告警。切换执行后端先释放原所有者。
4. 每一轮读平台的模块和决策，检查硬边界、冷却、限幅、最低 Ready 容量，再通过 scale 子资源修改目标副本。分别记录 API 结果与 current/desired/ready，写成功不等于 Pod Ready。PDB 不替代显式缩容保护。
5. 重复事件和重启保持幂等并恢复冷却/决策；过期或模型故障停止危险缩容并显示 fallback。固定周期和 bypass 遵循 [admission-safety](../admission-safety/SKILL.md)。
6. 提前量包含抓取、推理、控制循环、Pod P95 Ready 与必要 Node 启动时间；不能预测任意瞬时 burst，使用最低 Ready 容量保障。

## 复用与验证

管理 API 接既有 Gin/DAO/Vue，运行入口沿用 pkg/cmd 生命周期。研究中的服务名不要求拆成八个微服务，新业务包路径实施时确定。

复用 pkg/metrics Registry 和 HTTP 生命周期，已有 /metrics、/healthz、/readyz。Factory 自带 kube_resource_fcst_ 前缀；研究 predictive_* 命名须先确定映射，避免重复前缀/注册。指标包含预测、年龄、fallback/override、误差、实际/目标副本及动作结果，明确 HELP/TYPE/单位和基数预算；run/request/Pod UID/时间戳放库内。Exporter 只供观测，控制/准入不等待 Prometheus 抓取回读。

验证 HPA 冲突、重启恢复、过期推荐、重复 reconcile、限幅、API 失败、Ready 延迟及指标审计对账。Observe→Recommend→Admission→Auto 逐阶段推进，使用隔离集群及实际验证证据。

## kube-resource-fcst-operator：模块就是配置，没有 CRD

**2026-10-31 删除了 CRD**：`PredictiveScaler` 只是把模块里已有的内容（工作负载、上下界、fallback、节奏）再写一遍，两处会不同步，现在 operator 只读平台的模块，不在集群里创建任何资源，只写目标的副本。删掉的有：`pkg/operator/api/v1alpha1`、`deploy/operator/crd` 与示例、Helm 的 `crds/`、控制器与状态条件、Static 和 Prometheus 决策来源、Observe 模式、命令行 `--predictivescaler-crd`，RBAC 里对 `predictivescalers`、`secrets`、`events` 的权限，以及 `kube_resource_fcst_operator_prometheus_*` 指标。集群里残留的 CRD 与对象由部署者自己删除（`kubectl delete crd predictivescalers.autoscaling.kube-resource-fcst.io`），没有任何东西再读它们。

- 入口：`--platform-url`（必填）、`--platform-credential-file`、`--platform-ca-file`、`--platform-sync-interval`（与缓存、恢复相关的几个只是启动值，见下面“时间限制是系统参数”）。每个周期读 `GET /api/v1/operator/modules`（enabled、auto、非 bypass、有副本、Deployment/StatefulSet，且 `forecast.allow_auto_mode` 打开），为每个模块在内存里构造一个 `controller.Scaler`（`PlatformModule.Scaler`），交给 `Applier.Evaluate`：读目标、问平台要决策、套 `MakePlan`、经 scale 子资源写副本。模块上的 min/max（动态最大值取当前生效的）、fallback、`scale_up/down_max_step`、`scale_up/down_cooldown_seconds`、模式、bypass、启用状态下个周期生效。平台读不到时副本不动（见缓存）；`max_replicas<1` 或 min>max 的模块被跳过（否则会被夹到 0）；同一目标有 HPA 或 KEDA ScaledObject 时以它们为准并回报冲突。冷却时钟在 operator 内存里（重启后重新计时）。
- 规则全部在纯函数 `controller.MakePlan`：冲突则不动作；新鲜决策为目标，缺失/过期决策用 fallback 否则冻结且永不缩容；目标裁剪到 [min,max]，当前值越界时无视冷却和 Ready 强制纠正；未全部 Ready 时暂缓缩容；按方向冷却与 maxStep 限幅。改规则必须同步 plan_test.go。
- 决策来源只有平台：`GET /api/v1/operator/decisions`（开发者凭据，`POST /api/v1/operator/reports` 回传，响应含 `created_at` 供上报决策年龄）。Adapter（external metrics）来源**有意不做**：本 operator 取代 HPA/KEDA，不依赖它们。平台侧还检查 `forecast.allow_auto_mode`，关闭即回 `available=false`。向平台上报只在有动作/暂缓/冲突/错误且与上次不同时发生。
- 业务指标（pkg/operator/controller/metrics.go，kube_resource_fcst_operator_ 前缀，注册在 controller-runtime metrics.Registry，随其 /metrics 端点导出）：reconciles_total{result}、reconcile_duration_seconds、scale_actions_total{direction,result}、conflicts_total{source}、desired/current/ready_replicas{module}、decision_valid / decision_expires_in_seconds / decision_age_seconds{module}、platform_requests_total{result}。标签只用 module/direction/result/source，module 为平台模块名（无则 scaler 名）；Metrics 为 nil 时全部空操作。metrics_test.go 用 fake client 校对一次对账后的数值与标签基数。清单 deploy/operator/monitoring（Service+ServiceMonitor）。
- 入口 `kube-resource-fcst operator`（pkg/cmd/operator，默认开启 leader election）；清单 deploy/operator 与 Helm chart。验证只用 controller-runtime fake client 和假平台：未做 envtest 与真实集群，扩缩容、RBAC、leader election 切换、Helm 渲染都未验证。

**operator 只管副本（R10）**：operator 决策接口只回副本数与边界，不回 CPU/内存；Pod 资源由准入设置并保持不变，二者读同一个推荐快照里互不重叠的字段，不会冲突。操作记录里「执行失败」「冲突未执行」属于回退记录，「没有可用决策」属于降级记录（见规格第 16.7 节）。
- **前瞻决策与紧急扩容（R19）**：动态副本由 `pkg/forecastsvc/ahead.go` 在 `recommend` 里把各算法的副本需求并起来：提前量（实测就绪时间 P90＋节点＋决策等待＋偏晚余量）窗口上的可信度加权分位共识决定扩容，整个预测范围的更严分位共识加当前实测负载决定缩容；实测负载需要的副本数超过预测和现有就直接要求并标 `urgent`，`OperatorDecision.urgent` → `controller.Decision.Urgent` → `MakePlan` 对 `urgent` 的扩容跳过冷却和 maxStep（缩容不受影响）。改任何一环要同步 `ahead_test.go`、`plan_test.go`。未在真实集群验证。
- **决策先于动作（R18）**：operator 读决策时，平台先保证它不比 `forecast.decision_max_age_seconds`（默认 60 秒，0 = 关）老：过期就由平台重新采集、预测、决策再回答（`FreshRecommendation`，每个模块一次一个，失败读存着的那版，有自己运行间隔的模块不刷新）。operator 不自己判断决策新不新鲜，也不在平台之前动作。
- **时间限制是系统参数，不写死（2026-10-30）**：超时、间隔、缓存时长都是 `sys_config` 里的行，程序里只有默认值和允许范围（`pkg/timing`，表驱动：种子、读取、校验共用一份定义，不在范围内的值取最近的端点，缺失或不是整数取默认）。平台自己用的由 `forecastsvc.Service.Timing` 读（经 10 秒的参数缓存，改了不用重启）：`forecast.decision_refresh_wait_seconds`、`decision_forecast_max_age_seconds`、`refresh_budget_seconds`、`pipeline_timeout_seconds`、`preview_budget_seconds`、`fine_tune_budget_seconds`、`train_budget_seconds`。operator 用的由平台经 `GET /api/v1/operator/settings`（开发者凭据的权限 `forecast:operator:use` 里已加这一条）下发：`operator.request_timeout_seconds`、`sync_interval_seconds`、`decision_cache_ttl_seconds`、`module_cache_ttl_seconds`、`recover_answers`、`settings_refresh_seconds`、`degrade_hold_seconds`、`degrade_cooldown_seconds`、`recover_stable_seconds`。operator 里 `SettingsStore` 保存当前值，`SettingsRefresher` 启动时马上取一次、之后按 `settings_refresh_seconds` 刷新；取不到就保留最后一次的值，平台没发的字段（旧平台）保持原值而不是 0；命令行 `--platform-sync-interval`、`--decision-cache-ttl`、`--module-cache-ttl`、`--decision-recover-answers` 只是平台第一次回答之前的启动值。每个请求用 `context.WithTimeout(请求超时)`，HTTP 客户端只带 120 秒的上限。请求等重算的时间始终比 operator 的请求超时至少短 1 秒（`Service.RefreshWait`），否则 operator 先挂断。**不在这套里的**：调度器的认领/心跳、LDAP 与通知的超时（CRD 与 Prometheus 决策源已删除，它们的超时不存在了）。
- **按需重算只重算决策（2026-10-31）**：operator 来取决策而库里的推荐比 `forecast.decision_max_age_seconds` 旧时，`FreshRecommendation` 起一次重算（同一模块同时只一次，在自己的上下文里跑，请求最多等 `RefreshWait`）。重算现在是：先 `RunCollect` 采集最新样本（实测负载、在用副本），再 `RunModule` 在**已存的预测**上重新求副本数；只有最新一条训练记录比 `forecast.decision_forecast_max_age_seconds`（默认 600 秒）还旧——例如定时任务没在跑——才像以前那样用 `RunData` 把所有算法重跑一遍。所以两次定时重算之间不会再重复跑算法，而最新样本照样推动决策（负载突增时紧急扩容立刻跟上，单测覆盖）。它不写训练记录，所以不影响 `Due`（带自己运行间隔的模块仍按间隔做预测）。参数应大于 `forecast.run` 的周期，否则两次定时之间会再跑一次算法。
- **平台失联与恢复（R21，2026-10-31 重做）**：每个模块在 operator 内存里有一个状态：`normal`→（连不上）`cached`（记住的决策不到 `operator.decision_cache_ttl_seconds`，默认 10 分钟：只上调不下调）→ `holding`（无可用记忆：**当前副本原样保持、不套兜底**，`operator.degrade_hold_seconds` 默认 5 分钟，从缓存用完算起，没有缓存从第一次失败算起）→ `degraded`（保持期满：才套兜底，只上调不低于当前，单步，两次之间至少 `operator.degrade_cooldown_seconds`）；平台回答后进 `recovering`（上调立即跟随，下调等**连续 `operator.recover_answers` 次回答并且距第一次回答过了 `operator.recover_stable_seconds`**，默认 2 次和 5 分钟）再回 `normal`。平台回答“无决策”是回答，不被顶替。缓存判有效用严格小于 TTL（等于 TTL 时 `MakePlan` 已判过期，否则会不经保持期直接套兜底）。实现 `controller/cache.go`（`CachingDecider` 状态机、`DegradedError`）、`plan.go`（`PlanInput.Degrade/FallbackCooldown`）。**离线记录不丢**：上报先进发件箱（`controller/outbox.go`：内存队列＋可选 `--report-spool-file` 文件，上限 10000，按发生顺序补报，平台拒绝的 400/404/409/413/422 丢弃并计数，凭据或网关错误继续重试）；每条带唯一的 `record_id`、`occurred_at`、`decision_source`、`state`、`operator`，平台按 `record_id` 唯一索引去重、以 `occurred_at` 入库。**operator 不连数据库**（`TestTheOperatorHasNoPathToTheDatabase` 看整棵依赖树）、**不改 requests/limits**（`TestScalingNeverChangesTheRequestsOrLimitsOfTheWorkload`）。规则表在 `docs/02-系统架构.md` 三（附二）。
