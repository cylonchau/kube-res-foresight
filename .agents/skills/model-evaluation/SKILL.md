---
name: model-evaluation
description: 用于 kube-resource-fcst 准确率、时间滚动回测、算法排行榜、模型晋升和预测/准入/伸缩审计，实现可复现误差和执行效果评估时使用。
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
- **OOM 只扣内存分（R21）**：评分里 OOM 只在「该窗口内存给少了」时计入，只降低内存得分（每次扣 `oom_weight`，上限 60%），CPU 与副本得分不受影响；模块的 `oom_weight_enabled` 默认关，关时 OOM 不进任何评分（`scoring.WeightsWithOOM`）。
- **预测延迟验收（R21）**：`decision.DelayIndicators` 六项（扩容及时率≥90%、迟到 P90≤60 s、实时兜底占比≤20%、峰值低估 P90≤15%、预测期/提前量≥1、决策等待≤120 s），样本不足不评判，配置类两项不能单独让模块达标；`GET /modules/{id}/delay-report`。目标是代码常量，改动要评审。
- **效果报告与变更预览（P8，规划）**：B1 把准确度、延迟验收、超配、稳定性汇成一份按模块的报告，B2 用历史回放预览配置变更的影响（只读，不写预测与推荐），口径沿用本技能的低估加权误差与 `DelayReport`；范围与验收见 [requirements/references/product-closure-p8.md](../requirements/references/product-closure-p8.md)。未实现。
