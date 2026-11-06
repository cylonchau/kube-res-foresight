---
name: model-evaluation
description: 用于 kube-res-foresight 准确率、时间滚动回测、算法排行榜、模型晋升和预测/准入/伸缩审计，实现可复现误差和执行效果评估时使用。
---

# 回测、评估与领域审计

先读 [代码地图](../requirements/references/repository-map.md)、[评估契约](references/evaluation.md)、[阶段验收](../requirements/references/acceptance.md) 与 [共享契约](../requirements/references/contracts.md)。

## 实施流程

1. 区分 prediction_time、target_time 和实测到达时间，封存生成时可见特征与版本。真实目标到达后追加评估，不覆盖原预测。
2. 按 workload×metric×horizon×algorithm_version 滚动回测；训练、归一化、选参、校准不看未来。区分观测、推断、预测、执行事实及粒度。
3. 同时评估 MAE/RMSE/WAPE/sMAPE、pinball loss、分位数覆盖、burst precision/recall、提前量、SLO 和额外 Pod-hours；明确零值/缺样、样本量与置信度。点误差和区间覆盖不能混成准确率。
4. 记录全部候选、权重、选用原因、固定覆盖/fallback、原/建议/应用资源、目标副本、API 结果、实际 Ready 和失败回退。操作日志复用基础框架，领域证据需专门模型。
5. 晋升检查尾部风险、安全性、SLO、数据充分性和授权，排行榜第一不能自动进 Auto。查询/导出有权限、分页、时间范围和保留策略。

回测在 pkg/backtest（滚动起点、无泄漏、MAE/RMSE/WAPE/pinball/峰值低估/覆盖率、晋升判定 Evaluate），编排与落库在 forecastsvc.Backtest（backtest_run）。实测回填在 forecastsvc.Evaluate：预测周期结束且实测样本到达后追加 prediction_evaluation（预测行不改写，没有实测的周期也记一行 points=0 避免重复扫描），Leaderboard 按已回填的 WAPE 排名，排名本身不晋升任何算法；forecast.evaluate 任务每小时执行并按 DefaultRetention 清理（预测 30 天，回测/推荐/评估 180 天，各模块最新推荐永远保留）。审计导出接口已删除（问题修复 R3），记录页的「导出所选」是浏览器端 CSV。

**指标目录是历史、预测、表现与执行数据的唯一来源**（pkg/forecastsvc/catalog.go）：每个指标有名称、类别（usage/prediction/performance/execution）、单位、标签（仅 metric/algorithm/quantile）、来源表、时间列、聚合方式、保留天数和可选的 /metrics 导出名。GET /api/v1/forecast-metrics/catalog 列目录，GET /api/v1/forecast-metrics/query 按步长对齐返回序列（每序列最多 5000 点，源行上限 20 万并标 truncated；预测按 target_time 对齐并取每个目标时间的最新预测，所以多算法可同窗口对比）。/metrics 导出的每个值与查询接口最后一点一致（catalog_test.go 对账），新增指标只改目录。保留：样本=模块 lookback_hours，预测 30 天，回测/评估/推荐 180 天（每模块最新推荐永远保留），扩缩容记录 90 天。

副本数求解（pkg/decision/solver.go，模块 replica_mode=dynamic 时启用，fixed 保持原公式）用各算法自己的实测峰值低估作为不确定度：u=s/(1−s)，s 取最近至多 30 次评估（≥5 次）的 P90，不足用最近回测，再无则用模块余量并标「无实测」；单 Pod 容量可由历史估计（pkg/decision EstimateCapacity，需要近饱和样本 ≥30，否则拒绝并说明）。估计容量尚未与压测对照。执行效果评估（Pod-hours、SLO）与 Ready 延迟统计尚未实现。

调度复用 pkg/job，回填任务幂等；持久化走 GORM DAO 和显式迁移，不重复建用户/权限体系。管理页面复用现有表格，UTC 保存、按配置时区展示。

验证 1800 对 1950 的 APE≈7.69%、零真实值、全零窗口、延迟/重复回填、跨版本比较、未来泄漏和 API 写成功但 Ready 失败；只报告已执行的验证证据。

评估缺口已按 P7 的 A1/A2 修复（2026-10-28，计划与状态见 [算法改进计划](../algorithm-library/references/improvement-plan.md)，实测基线见 [A0 基线](../algorithm-library/references/baseline-a0.md)）：候选与基线在共同起点上比较（`backtest.Origins`，起点铺开、默认 10 个、避开日周期整数倍，判定要求点数与起点数相同）；排名与晋升加入 RiskWAPE（低估按 3 倍计，定义见 [评估契约](references/evaluation.md)）。改评估时保持无泄漏，并用 `go run ./scripts/baseline -db <库>` 在同一份数据副本上对比前后；页面上的排名依据列要默认可见并配「?」说明。

## 算法评分的准确度口径（R11）

评分针对资源（CPU、内存、GPU）而不是副本数：负载需要的资源 ÷ 一个 Pod 的规格得出副本数，副本数不是独立的准确度。资源准确度 = 1 − Σ|预测−实际| ÷ Σ实际（低估乘 `Under`，下限 0）；每个资源有自己的分与排名，内存分另扣 OOM；综合分按权重合成，只有模块没有任何资源来源时才退回副本数。公式、例子与保留数量（`forecast.score_max_per_module`）见 [algorithm-framework-spec 第 17 节](../requirements/references/algorithm-framework-spec.md)；代码在 `pkg/scoring`。
- **扩容及时性评估（R19）**：每次向上的副本决策记一条 `scale_outcome`（决策时的副本数、要求的副本数、提前量、投票）；`evaluateOutcomes` 用就绪副本数序列找「到要求副本数的第一个时刻」，用实测负载（CPU/内存/请求量按每副本容量折算）找「第一次需要比当时多的副本」，判定及时／偏晚／负载没来（观察期＝3 个提前量，至少 10 分钟）。没有就绪副本数来源的模块无法评价，记录里写明。结果驱动下一次提前量（`decision.ComputeLead`）。`GET /scale-outcomes` 与扩缩容记录页的「扩容及时性」弹窗读它。验证只用合成数据，没有真实集群的及时率。
- **选算法要看最近的跟踪误差（R18）**：回放评分每几小时才重算，只看它会让一个已经落后的 top1 一直被选。每次决策都用最近 10 步里「该时刻之前最后一次预测」与实际值算 WAPE 和偏差（`forecastsvc.Tracking`）；至少 3 步且 WAPE 超过 50% 的算法本轮不参与选择，只要还有别的算法没有落后；全部落后时按评分。落后的原因写进决策说明，不静默切换。
- **评分也评「负载上升」的窗口（R22）**：只评最近 6 个窗口（`DefaultScoreWindows`）评的是最近一两个小时的平稳，此时「沿用最后值」（Naive）最准，会长期压住能预见日高峰的算法。`scoreInstance` 在最近窗口之外，再从更早的历史里选至多 `DefaultEventWindows`（6）个**峰值比前一个窗口高出至少 `EventMinRise`（15%）的窗口**一并回放评分（`eventWindows`，只从算法有足够历史的位置选，按上升幅度取最大的）；它们与最近窗口一样计入，明细里标 `event` 与 `rise`，评分页显示其中几个是负载上升的窗口。口径不变（低估乘 `Under`），变的是「评哪些窗口」。合成日高峰负载的单测：只看最近窗口时 Naive 不输，加上上升窗口后季节算法胜。限制：没有真实负载验证；上升若由发布、重启造成也会入选；回放次数最多翻倍（评分是每 6 小时的任务）；切换裕度和次数没改。
- **需求对比与估算（R22）**：每个推荐的详情带 `need`（`forecastsvc.NeedDetail`）：副本数、单 Pod CPU、单 Pod 内存各有 **算法需求（还没套模块最小/最大）/推荐/当前/上下限**，以及总 CPU、总内存（副本×单 Pod）。算法需求来自副本方案的 `ReplicaPlan.Needed`（约束里最大的需求，在上下限之前）和不带 `Bounds` 再算一次的 `decision.Decide`；被下限抬高记 `bound=min`、建议 `lower_min`，被上限压低记 `max`、`raise_max`；近 24 小时按算法的 CPU 预测、Pod CPU 请求和目标利用率反推的副本范围给出建议的下限和上限（只有动态副本且写了 Pod CPU 请求的模块才有，否则 `range_note` 说明原因）。`GET /modules/:id/need`（`Service.Estimate`）只读：不存推荐、不推进状态，模块停用或只观察也能答，所以配置前就能看需求。当前副本数来自 operator 的最近上报，当前 Pod 规格是模块里填的 Pod 请求（没填则未知）。前端 `need-compare.vue`，模块列表的「需求」按钮和推荐详情都用它，建议的上下限可以一键写回模块。**副本需求拆成三项分开显示（R23）**：`need.parts` 有当前用量（实测的最新负载，`Ahead.Live`）、预测（决定副本的约束里预测峰值，写明算法，偏保守的算法标「P90/峰值包络」）、安全余量（`1+不确定度`，来源是实测低估、回测或模块的余量系数，以及它多出几个副本）和单 Pod 承载；三者不合成一个数字，因为已发生的、没发生的和为预测偏低留的是三件事。
- **工作负载总量要摊到各个 Pod 上才是单 Pod 的 CPU 和内存（R23）**：指标映射的层级是 `workload`（所有 Pod 加起来）时，它的预测是总量；决策以前把总量当成单个 Pod 的值再被模块上限夹住（总 53 核的负载显示成「单 Pod 53 核」、推荐压在上限 6 核）。现在 `decide` 对每种来源**全是 workload 层级**的资源，把候选值除以它摊到的副本数（刚决定的副本数，没有就用在跑的副本数）再决定单 Pod 的值（`perPodInputs`，纯函数再调一次 `decision.Decide`）；未夹的需求（`need`）也按需要的副本数摊。来源是 pod/container 层级、或混合层级的资源不变。副本数一直按总量算，不受影响。副本数是动态求解的（目标利用率在 0~1 之间）时，单 Pod 值 = 分到的负载 ÷ 目标利用率（取代余量系数，`perPodInputs` 的 `util`），与副本数按「Pod 规格 × 利用率」算的口径一致。需求拆开看里「当前用量」取与「预测」同一种资源的实测（`LiveDetail.Resources`，没有才退回另一种并标 `other_resource`）；推荐的单 Pod CPU/内存若被 Pod 规格保持（变化在 `forecast.resource_change_ratio` 内、或降低要等 `forecast.resource_hold_hours`）留住，`NeedField.Held` 写明；仅推荐模式下保持的值不超过模块填的在跑规格（`inForce`），因为那时推荐从没被应用，准入、自动模式不变。
- **OOM 只扣内存分（R21）**：评分里 OOM 只在「该窗口内存给少了」时计入，只降低内存得分（每次扣 `oom_weight`，上限 60%），CPU 与副本得分不受影响；模块的 `oom_weight_enabled` 默认关，关时 OOM 不进任何评分（`scoring.WeightsWithOOM`）。
- **预测延迟验收（R21）**：`decision.DelayIndicators` 六项（扩容及时率≥90%、迟到 P90≤60 s、实时兜底占比≤20%、峰值低估 P90≤15%、预测期/提前量≥1、决策等待≤120 s），样本不足不评判，配置类两项不能单独让模块达标；`GET /modules/{id}/delay-report`。目标是代码常量，改动要评审。
- **效果报告与变更预览（P8 B1、B2，已实现）**：`pkg/forecastsvc/effect.go` 把回填准确度（在用算法、按预测点数加权的 WAPE、峰值低估、偏差、覆盖率）、`DelayReport`、超配与不足（推荐的副本数对比同期实测负载的最大需要副本数，`pkg/decision/effect.go`）、扩缩容反复与降级原因、算法采用占比汇成一份按模块的报告，样本不足的部分写“样本不足”而不是 0；`pkg/forecastsvc/preview.go` 用历史回放比较保存的设置与拟改设置，只读，用同一批无泄漏的窗口和同一个“不留余量的实际需要”（风险误差里少 1 个副本算 3 个）。注意两处限制：效果报告里的准确度不含低估加权误差（回填评估表没存分项）；预览不重放平台决策（冷却、步长、前瞻、保护），model_service 只用预训练模型。范围、口径、与原计划的差别和未验证项见 [requirements/references/product-closure-p8.md](../requirements/references/product-closure-p8.md)。**延迟验收总评**：四项证据（扩容及时率、迟到 P90、实时兜底占比、峰值低估 P90）全部能判断且达标才是“达标”，一项不达标就是“未达标”，其余“样本不足”。**执行与容量**：实际 Ready 副本对照实测负载需要的副本，给出 Pod·小时、多占、不足和容量 SLO（没有不足的时间占比，目标是系统参数），它不是延迟 SLO，多占不是节省。B3 告警联动未实现。
