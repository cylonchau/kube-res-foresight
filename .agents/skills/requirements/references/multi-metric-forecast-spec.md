# 多指标加权预测、算法评分与动态扩缩容：规格（已实现，含偏差）

> **注意（2026-10-25）：** 算法已改为平台级统一配置并自动应用到所有模块，指标来源回到模块，不再有算法实例与绑定。本文中与此冲突的描述以 [module-algorithm-spec.md](module-algorithm-spec.md) 为准。

对应 2026-10-24 提出的「预测扩缩容系统需求优化」10 条。本文先定数据模型、计算公式、算法清单、评分与选择规则、分期和验收，经确认后实施。与已有实现和先前规格 [forecast-redesign-spec.md](forecast-redesign-spec.md) 冲突的地方集中在第 1 节。

## 1. 与现状的差异（先说清楚要改什么）

| 现状 | 改为 |
|---|---|
| 模块只有固定的 cpu / memory / rps 三个指标槽位，辅助指标另放 extra_mappings | **指标源**：模块下任意多条，类型含 CPU、GPU、GPU 显存、内存、OOM、网络收/发、Prometheus 自定义；同类型可配多条，各有优先级、权重、统计方式、触发阈值 |
| 一个算法绑定只预测一个指标，推荐按资源分别决定 | 算法实例声明**参与哪些指标源**，所有指标共同参与加权计算，推荐 CPU/内存/GPU/副本并给出每个指标的计算依据 |
| 算法参数可存为**模板**，绑定可引用模板；另有**输入指标集** | 算法目录是固定架构（内置、不可增删），用户在**创建预测算法实例**时直接传参数、选参与指标、预测周期、权重；**删除参数模板和输入指标集**（旧数据在 `--upgrade` 迁入算法实例，先打印清单） |
| 回测只在指标内比较并与基线对比晋升 | 同一时间段内对每个算法实例按资源和副本分别算误差，合成**综合评分**，保留历史排名，自动选最高分且满足门槛者 |
| 最大副本数为固定值 | 常规上限 + 动态扩展 + **独立安全硬上限** |
| 预测管理与模块管理在同一菜单 | **模块管理**与**预测管理**为两个一级菜单 |
| 算法只有 6 个 + macd_trend | 增加 KNN、朴素贝叶斯、随机森林、衰减直方图百分位、RNN/GRU/LSTM 等（第 4 节） |

## 2. 数据模型

- `metric_source`（指标源，属于模块）：`module_id`、`name`、`kind`（cpu / gpu / gpu_memory / memory / oom / network_rx / network_tx / custom）、`resource`（它影响的推荐对象：cpu / memory / gpu / replicas / signal 仅作信号）、`mapping_id`（Prometheus 查询）、`priority`（同类型内数字小者优先）、`weight`（≥0）、`stat`（avg / p50 / p90 / p95 / p99 / max）、`window_hours`（统计窗口）、`threshold` 与 `unit`、`enabled`、`params`（JSON，如缩放系数、自定义说明）。
- `forecast_algorithm`（预测算法实例，取代 module_algorithm）：`module_id`、`name`、`algorithm_id`（固定目录）、`params`（创建时传入，按目录 schema 校验）、`source_ids`（参与指标源）、`horizon_minutes`、`weight`、`history_days`、`train_budget_seconds`、`enabled`。
- `algorithm_score`（评分记录）：按算法实例与评估窗口保存各资源误差、副本准确度、OOM 次数、数据完整度、综合评分、排名。
- 模块新增字段：`min_score`、`min_history_hours`、`predict_timeout_seconds`、`min_replicas`（硬下限）、`max_replicas`（常规上限）、`max_replicas_mode`（fixed / dynamic）、`hard_max_replicas`（安全硬上限）、`expand_step_ratio`、`expand_min_score`、`combine`（每种资源的合并方式）、`fallback_*`（沿用）。
- `recommendation` 增加 `gpu`、`score`、`algorithm`、`effective_max_replicas`、`basis`（JSON，每个指标源一行的计算依据）。
- `admission_decision`（准入记录）：模块、命名空间/Pod、结果、改前改后 requests/limits、原因；保留期可配。

## 3. 多指标综合计算

对模块的每条指标源 s，算法实例给出预测序列，取其 `stat` 得到需求值 d_s；`ratio_s = d_s / threshold_s`。

1. **资源推荐**：对每种资源 R（cpu / memory / gpu），可用指标源按 `combine` 合并：加权平均（默认）、最大值或求和。同类型多条指标源中，数据缺失的跳过，按优先级顺序补位，权重在可用者中归一。结果再乘余量系数并受边界约束。
2. **副本压力**：`P = Σ(w_s · ratio_s) / Σ w_s`（只含 `resource` 为 replicas 或有阈值的指标源）。`max_ratio = max(ratio_s)`。
   - `P < 1` 且 `max_ratio < 1`：不扩容，缩容受平滑与下限约束。
   - 否则 `replicas = ceil(当前副本 × max(P, max_ratio 对应的强约束))`；任一 `kind = oom` 的指标源在窗口内发生事件时，内存推荐不低于 OOM 地板，副本在原结果上至少再加 1 个步长，并降低对应算法评分。
3. 例：CPU P99 1.2 核 / 阈值 5 核 → ratio 0.24；内存 P99 8 GiB / 阈值 10 GiB → ratio 0.8；OOM 1 次 → 内存地板生效、副本加一档。权重由用户给定，页面逐行展示 d_s、阈值、ratio、权重、贡献和是否触发。

## 4. 算法目录（固定、内置、纯 Go，Prometheus 采集的历史就是数据集）

| 类别 | 算法 | 说明 |
|---|---|---|
| 基线 | Naive、SeasonalNaive | 已有 |
| 统计 | EMA（span / alpha、偏差修正）、Holt-Winters | 已有；用户可调参数 |
| 统计 | MACD 趋势（参考金融指标，仅取快慢信号 EMA 差分做有上限的趋势修正） | 已有，说明里标明其来源 |
| 统计 | 衰减直方图百分位（Kubernetes VPA / Google Autopilot 式，CPU 与内存半衰期不同） | 新增，最贴近内存与 OOM 场景 |
| 机器学习 | 岭回归 AR、GBDT | 已有；协变量改为由算法实例的“参与指标”提供 |
| 机器学习 | 随机森林、KNN（相似窗口回归）、朴素贝叶斯（把未来水平分桶后分类，取桶中位数） | 新增 |
| 深度学习 | Elman RNN、GRU、LSTM | 新增，进程内训练，受 `train_budget_seconds` 限制，超时按失败走兜底 |

每个算法在创建实例时独立配置参数、参与指标、预测周期、权重，不写死；算法目录接口返回参数 schema 供界面渲染。

## 5. 回测、评分与自动选择

- **回测窗口**：所有算法实例在同一时间段（如 2026-10-01 11:00–12:00）上，用截止点之前的数据预测，与同段真实使用比较，无未来泄漏。
- **真实需求**：用同一套综合计算对该段真实使用算出“理想副本数、理想 CPU / 内存 / GPU”。
- **逐项准确度**：副本准确度 = 完全一致为 100%；预测偏少按 2 倍权重惩罚（预测 3、实际 4 且发生 OOM 则额外扣分）。CPU、内存、GPU 分别算 WAPE 与峰值低估，不只比较副本。
- **综合评分**（0–100）：副本准确度、各资源准确度、峰值低估惩罚、OOM 惩罚、数据完整度，各项权重在模块里可调，默认见实现中的常量并在页面展示。
- **保留历史与排名**：每次评估写 `algorithm_score`，页面查看趋势与排名。
- **自动选择**：符合要求（评分 ≥ `min_score`、历史 ≥ `min_history_hours`、本周期预测成功且未超时）的最高分实例；并列时取复杂度低者；为避免来回切换，新实例需连续领先若干周期才替换在用者。选中结果连同评分、有效期发送给 Controller。

## 6. 兜底与上下限

- 评分低于阈值、历史不足、指标缺失、预测超时、系统异常任一发生，按字段采用模块的 fallback 默认值（沿用现有降级链与恢复滞后），降级记录写明原因。
- `min_replicas` 是硬下限，任何算法、兜底、固定周期都不得突破。
- `max_replicas` 在 fixed 模式下就是上限；dynamic 模式下，当预测需求超过它且所选算法评分 ≥ `expand_min_score`、集群容量与配额检查通过、单周期增幅 ≤ `expand_step_ratio` 时，有效上限自动抬高，**永远不超过 `hard_max_replicas`**。

## 7. Controller / Kyverno / GPU

- 推荐载荷：CPU、内存、GPU、副本、算法、评分、有效期、有效上下限。
- Controller 执行副本，硬下限与有效上限由它再次校验；Kyverno 通过准入接口取 CPU / 内存 requests，GPU 为整数卡数。
- 每次准入回答和每次执行结果都落库并可查询。

## 8. 菜单

- **模块管理**（一级）：模块列表（基本信息、边界与硬上限、fallback、固定周期）、指标源。
- **预测管理**（一级）：预测算法（创建实例）、算法评分与排名。
- 其余沿用：预测结果、推荐与执行（快照、执行记录、准入记录、降级记录）、监控指标、数据源。

## 9. 分期与验收

| 期 | 内容 | 验收 |
|---|---|---|
| A | 指标源、算法实例、删除模板与输入指标集、旧数据迁移、两个一级菜单 | 迁移测试（旧库升级后绑定变为实例）；同类型多指标源可建、可排优先级与权重 |
| B | 多指标综合计算与计算依据展示 | 用例：CPU 1.2/5、内存 8/10 GiB、OOM 1 次得到约定的推荐；两组不同数据得到不同结果 |
| C | 新算法 | 每个算法有已知输出单测、无泄漏断言、超时被中止 |
| D | 评分、排名、自动选择、兜底阈值、动态上限 | 表驱动：副本 4 vs 4 得 100%，3 vs 4 且 OOM 扣分；低分/历史不足/缺指标/超时走 fallback；有效上限永不超过硬上限 |
| E | 推荐载荷、Controller 校验、Kyverno 准入记录、GPU | operator 单测（硬下限/有效上限）；准入记录可查询 |
| F | 页面、浏览器验收、文档与技能同步 | 新脚本覆盖全部新页面与三种宽度 |

## 10. 已确认的决定与实现偏差

决定：同一资源多来源的合并方式按资源可配，默认加权平均；RNN/GRU/LSTM 为纯 Go 进程内小模型；GPU 是与 CPU、内存并列的独立资源。

与上文设计的偏差（以代码为准）：

- 数据模型沿用 `module_algorithm` 表作算法实例（加 name、sources、horizon_minutes、weight、history_days、train_budget_seconds），没有新建 `forecast_algorithm`；模块的辅助映射槽位全部变成指标来源，系列键 = 类型（同类型第二条起 kind_2），所以既有历史、预测、回测不用改。
- 实例的「权重」只是排名时乘在综合分上，不是模型融合权重；回测与评分的窗口用模块（或实例）的预测视野回放最近 `score_windows` 个窗口。
- 来源统计只用 avg/p50/p90/p95/p99/max/sum/last；`basis=history` 的来源（OOM、Kubernetes requests/limits 等）取最近 `window_hours` 的实测值，不预测。
- 动态上限**没有**做集群容量与配额检查（平台不读集群）：有效上限只由评分门槛、`expand_step_ratio` 的逐周期上调和独立的 `hard_max_replicas` 约束；`hard_max_replicas` 应由管理员按集群容量设定。
- GPU 只通过 Kyverno 准入对新 Pod 生效（requests = limits 的整数卡数），不改运行中的 Pod；Kyverno 策略仍是未验证草稿。
- 准入决策记录只记录命中某个模块的回答（含拒绝）；没有任何模块观察的负载不记录，避免被无关 Pod 刷满。
- 旧数据迁移：`--migration/--upgrade` 把旧绑定合并为实例（同模块同算法同参数合并多个指标；参数不同的第二个起命名 算法_2 并改名既有预测/回测/评估），输入集的协变量并入来源，映射槽转为来源，随后清空槽并退役 `algorithm_template`、`input_set` 表。
- 评分任务 `forecast.score`（每 6 小时）由迁移种子创建；评分、排名与切换状态保存在 `module_run_state`。

