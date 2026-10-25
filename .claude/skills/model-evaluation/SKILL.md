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

回测在 pkg/backtest（滚动起点、无泄漏、MAE/RMSE/WAPE/pinball/峰值低估/覆盖率、晋升判定 Evaluate），编排与落库在 forecastsvc.Backtest（backtest_run）。实测回填在 forecastsvc.Evaluate：预测周期结束且实测样本到达后追加 prediction_evaluation（预测行不改写，没有实测的周期也记一行 points=0 避免重复扫描），Leaderboard 按已回填的 WAPE 排名，排名本身不晋升任何算法；forecast.evaluate 任务每小时执行并按 DefaultRetention 清理（预测 30 天，回测/推荐/评估 180 天，各模块最新推荐永远保留）。审计导出 GET /api/v1/audit/export（CSV，单元格防公式注入）。

**指标目录是历史、预测、表现与执行数据的唯一来源**（pkg/forecastsvc/catalog.go）：每个指标有名称、类别（usage/prediction/performance/execution）、单位、标签（仅 metric/algorithm/quantile）、来源表、时间列、聚合方式、保留天数和可选的 /metrics 导出名。GET /api/v1/forecast-metrics/catalog 列目录，GET /api/v1/forecast-metrics/query 按步长对齐返回序列（每序列最多 5000 点，源行上限 20 万并标 truncated；预测按 target_time 对齐并取每个目标时间的最新预测，所以多算法可同窗口对比）。/metrics 导出的每个值与查询接口最后一点一致（catalog_test.go 对账），新增指标只改目录。保留：样本=模块 lookback_hours，预测 30 天，回测/评估/推荐 180 天（每模块最新推荐永远保留），扩缩容记录 90 天。

副本数求解（pkg/decision/solver.go，模块 replica_mode=dynamic 时启用，fixed 保持原公式）用各算法自己的实测峰值低估作为不确定度：u=s/(1−s)，s 取最近至多 30 次评估（≥5 次）的 P90，不足用最近回测，再无则用模块余量并标「无实测」；单 Pod 容量可由历史估计（pkg/decision EstimateCapacity，需要近饱和样本 ≥30，否则拒绝并说明）。估计容量尚未与压测对照。执行效果评估（Pod-hours、SLO）与 Ready 延迟统计尚未实现。

调度复用 pkg/job，回填任务幂等；持久化走 GORM DAO 和显式迁移，不重复建用户/权限体系。管理页面复用现有表格，UTC 保存、按配置时区展示。

验证 1800 对 1950 的 APE≈7.69%、零真实值、全零窗口、延迟/重复回填、跨版本比较、未来泄漏和 API 写成功但 Ready 失败；只报告已执行的验证证据。
