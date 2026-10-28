---
name: control-plane
description: 用于 kube-resource-fcst 自定义 CRD、Operator、副本执行、状态和 Platform Prometheus Metrics 的设计实现及审查，检查副本唯一控制者和故障恢复。
---

# 控制面与副本执行

先读 [代码地图](../requirements/references/repository-map.md)、[架构职责](references/architecture.md) 和 [共享契约](../requirements/references/contracts.md)。CRD 与预测 Operator 尚待建设。

## 实施流程

1. 按目标集群版本和能力发现选择 Kubernetes SDK/controller-runtime；PredictiveScaler 等是自定义设计。正式 API group 待定，不发布 example.io 占位值。
2. 交付可生成的类型、OpenAPI schema、RBAC、manifests、status/conditions 和兼容测试；不能把研究字段当 Kubernetes 原生字段。
3. Auto 前确认副本控制权（由 PredictiveScaler CRD 与 kube-resource-fcst-operator 取代 HPA/KEDA）；存在 HPA/KEDA 或其他写入控制器时拒绝双写并告警。切换执行后端先释放原所有者。
4. reconcile 读直接内部有效推荐快照，检查模式、硬边界、冷却、限幅、最低 Ready 容量、配额和节点容量，再通过 scale 子资源修改目标副本（实现细节，对外语义是 CRD 驱动的扩缩容）。分别记录 API 结果与 current/desired/ready，写成功不等于 Pod Ready。PDB 不替代显式缩容保护。
5. 重复事件和重启保持幂等并恢复冷却/决策；过期或模型故障停止危险缩容并显示 fallback。固定周期和 bypass 遵循 [admission-safety](../admission-safety/SKILL.md)。
6. 提前量包含抓取、推理、控制循环、Pod P95 Ready 与必要 Node 启动时间；不能预测任意瞬时 burst，使用最低 Ready 容量保障。

## 复用与验证

管理 API 接既有 Gin/DAO/Vue，运行入口沿用 pkg/cmd 生命周期。研究中的服务名不要求拆成八个微服务，新业务包路径实施时确定。

复用 pkg/metrics Registry 和 HTTP 生命周期，已有 /metrics、/healthz、/readyz。Factory 自带 kube_resource_fcst_ 前缀；研究 predictive_* 命名须先确定映射，避免重复前缀/注册。指标包含预测、年龄、fallback/override、误差、实际/目标副本及动作结果，明确 HELP/TYPE/单位和基数预算；run/request/Pod UID/时间戳放库内。Exporter 只供观测，控制/准入不等待 Prometheus 抓取回读。

验证 HPA 冲突、重启恢复、过期推荐、重复 reconcile、限幅、API 失败、Ready 延迟及指标审计对账。Observe→Recommend→Admission→Auto 逐阶段推进，使用隔离集群及实际验证证据。

## 不再需要生成 CRD：模块就是配置（platform-driven）

PredictiveScaler 只是把模块里已有的内容（工作负载、上下界、fallback、节奏）再写一遍，两处会不同步，所以 operator 支持直接读平台模块：`--platform-url`（+ `--platform-credential-file`、`--platform-sync-interval`，Helm 的 `platform.*`）。每个周期读 `GET /api/v1/operator/modules`（enabled、auto、非 bypass、有副本、Deployment/StatefulSet，且 `forecast.allow_auto_mode` 打开），为每个模块在内存里构造一个 Auto 的 PredictiveScaler（`controller.PlatformModule.Scaler`）并走与 CRD 完全相同的 `evaluate`/`MakePlan`，经 scale 子资源写副本；不创建任何对象。模块上的 min/max（动态最大值取当前生效的）、fallback、`scale_up/down_max_step`、`scale_up/down_cooldown_seconds`、模式、bypass、启用状态下个周期生效。平台读不到时副本不动；`max_replicas<1` 或 min>max 的模块被跳过（否则会被夹到 0）；同一目标有 PredictiveScaler 对象、HPA 或 KEDA 时以它们为准并回报冲突。冷却时钟存在 operator 内存里（重启后重新计时）。`--predictivescaler-crd=false` 时不注册 CRD 控制器，也不需要安装 CRD。以下 CRD 方式仍保留，可与前者对不同工作负载并存。验证：pkg/operator/controller/modules_test.go（fake client + httptest 平台）；未用 Helm 渲染过 chart。

## kube-resource-fcst-operator（已实现；CRD 路径与 platform-driven 路径都只在 Docker Desktop kubeadm 上验证过）



- 框架：controller-runtime（Operator SDK / Kubebuilder 工具链生成）。CRD `PredictiveScaler`，API group `autoscaling.kube-resource-fcst.io/v1alpha1`（暂定，域名待项目确定）；类型在 pkg/operator/api/v1alpha1，用 controller-gen 生成 deepcopy、CRD（deploy/operator/crd）和 RBAC（deploy/operator/rbac，来自 pkg/operator/controller/rbac.go 的包级标记）。
- 规则全部在纯函数 `controller.MakePlan`：冲突则不动作；新鲜决策为目标，缺失/过期决策用 fallback 否则冻结且永不缩容；目标裁剪到 [min,max]，当前值越界时无视冷却和 Ready 强制纠正；未全部 Ready 时暂缓缩容；按方向冷却与 maxStep 限幅。改规则必须同步 plan_test.go。
- Reconciler：经 scale 子资源读写 Deployment/StatefulSet（apps/v1）；冲突来源为同目标的 HPA、KEDA ScaledObject（CRD 不存在则忽略）和更早创建的 PredictiveScaler；Observe 只评估不写；状态含 Ready/Conflict/DecisionValid 条件、lastDecision、lastScaleTime（重启后冷却依据）。向平台上报只在有动作/暂缓/冲突/错误且与上次不同时发生。
- 决策来源：Static、PlatformAPI（GET /api/v1/operator/decisions，开发者凭据，POST /api/v1/operator/reports 回传，响应含 created_at 供上报决策年龄）和 Prometheus（pkg/operator/controller/prometheus.go：一次即时查询须恰好返回一条有限、非负的序列，向上取整；无序列/多序列/NaN/负值/陈旧/401/403 均报错，由 reconciler 冻结或用 fallback；ValidUntil=样本时间+maxStalenessSeconds）。Adapter（external metrics）来源**有意不做**：本 operator 取代 HPA/KEDA，不依赖它们。平台侧还检查 forecast.allow_auto_mode，关闭即回 available=false。
- 业务指标（pkg/operator/controller/metrics.go，kube_resource_fcst_operator_ 前缀，注册在 controller-runtime metrics.Registry，随其 /metrics 端点导出）：reconciles_total{result}、reconcile_duration_seconds、scale_actions_total{direction,result}、conflicts_total{source}、desired/current/ready_replicas{module}、decision_valid / decision_expires_in_seconds / decision_age_seconds{module}、platform_requests_total{result}、prometheus_reads_total{result}。标签只用 module/direction/result/source，module 为平台模块名（无则 scaler 名）；Metrics 为 nil 时全部空操作。metrics_test.go 用 fake client 校对一次对账后的数值与标签基数。清单 deploy/operator/monitoring（Service+ServiceMonitor）。
- 入口 `kube-resource-fcst operator`（pkg/cmd/operator，默认开启 leader election）；清单 deploy/operator。验证只用 controller-runtime fake client：未做 envtest 与真实集群，扩缩容、RBAC、CRD 安装、leader election 切换都未验证。

**operator 只管副本（R10）**：operator 决策接口只回副本数与边界，不回 CPU/内存；Pod 资源由准入设置并保持不变，二者读同一个推荐快照里互不重叠的字段，不会冲突。操作记录里「执行失败」「冲突未执行」属于回退记录，「没有可用决策」属于降级记录（见规格第 16.7 节）。
