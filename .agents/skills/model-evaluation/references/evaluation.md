# 评估、回填与领域审计

预测唯一键：module_id + metric + prediction_time + target_time + model_version + quantile。原预测不可覆写，评估追加实测来源、粒度、缺样、到达时间和评估版本；回填幂等，实测修正保留修订关系。

滚动训练/验证/测试，fit、归一化、填补、选参、校准仅用当时可见数据。封存特征窗口、数据源/映射/参数版本和模型产物引用，能重放决策。历史分析支持 7/30/90 天；至少 30 天原始回放及审计保留期可配置。

## 指标定义

- MAE=mean(abs(pred-actual))；RMSE=sqrt(mean((pred-actual)^2))，单位与目标一致。
- APE=abs(pred-actual)/abs(actual)*100%，实际非零时计算。1800 对 1950 约 7.69%，不能推导区间覆盖。
- WAPE=sum(abs(error))/sum(abs(actual))；分母全零返回不可计算及原因。sMAPE 明确具体公式及双方为零/近零处理。
- **RiskWAPE（低估加权误差）**=（高估部分 |e| + 低估部分 |e|×w）/sum(abs(actual))，w 默认 3（`backtest.DefaultUnderWeight`）：预测偏低会让工作负载资源不足，偏高只是浪费，所以排行榜排名和回测晋升看它，WAPE 保留作准确度并列展示。已存的实测评估只有 WAPE/MAE/偏差，用 `backtest.RiskWAPE(wape, mae, bias, w)` 由它们还原（低估占比=(MAE−偏差)/(2·MAE)，偏差=预测−实际的均值）。回测结果里 `under_weight` 为 0 表示旧结果，没有该指标，晋升按旧规则判，不拿它比较。
- **共同起点**：候选与基线必须在同一批回测起点上比较（`backtest.Origins`），起点铺开在整段历史上（相邻至少一个视野，步距避开整数倍的日周期）；历史要求随视野增长的模型（KNN）用 `forecast.MinSamplesFor`。判定要求两边点数与起点数相同，否则“不可比”。
- **分位预测的晋升**（算法设了 `quantile_level`，或 GBDT 的分位目标）：回测按该分位算 pinball 与覆盖率，基线也按同一分位评分；晋升看覆盖率不低于 level−0.1（`backtest.QuantileCoverageSlack`）、pinball 胜过基线、峰值低估不更差，不看 WAPE（故意偏高的线必输）。结果里 `alpha` 为该分位，0.5 或未设走原规则。
- pinball loss 按分位数；P90 coverage 明确单侧分位数覆盖或某个区间覆盖，不能混名。
- burst precision/recall 固定事件定义、阈值、对齐容差、最低样本，提前量关联控制/Ready 延迟；证据不足不能承诺可预测。
- 按 workload×metric×horizon×algorithm_version 分组，带样本量、缺样率、置信度、SLO violation 和额外 Pod-hours；低误差不代表安全或低成本。

## 审计关联

decision_id 关联决策/执行，推荐 ID 关联快照/准入。保存数据质量、全部候选、模型/参数/策略版本、权重、选择原因、固定覆盖/fallback、原/建议/应用资源、目标/current/ready、副本 API 结果、Ready 时间、操作者、UTC 时间和 trace/request ID。

登录日志、普通操作日志和领域证据分别保留。Metrics 只承载有界汇总；完整关联 ID 和资源 JSON 入库，导出需权限和脱敏。

异步事件带 schemaVersion、幂等键、重试和 DLQ；Webhook 不等消息平台，用有界缓冲/outbox 并监控积压与丢失。审计故障不能伪造成功。
