# 算法管理与模块：规格

对应 2026-10-25 的「预测扩缩容系统优化需求」。**取代**上一版“独立算法实例 + 手动绑定”的设计，也取代 [forecast-redesign-spec](forecast-redesign-spec.md)、[multi-metric-forecast-spec](multi-metric-forecast-spec.md) 中与此冲突的描述。

后续的算法框架、数据集、数据周期与评分页面见 [algorithm-framework-spec](algorithm-framework-spec.md)，其中与本文冲突之处（菜单、指标来源页、评分页）以那份为准。

核心流程：创建模块 → 自动关联所有已启用算法 → 按模块指标和预测周期执行预测 → 对比各算法预测结果与准确度 → 选择最优算法 → 输出扩缩容决策 → Kubernetes Controller 执行。

## 1. 职责

| 概念 | 是什么 | 管什么 |
|---|---|---|
| 算法（`AlgorithmConfig`） | 目录里每个算法的**平台级**设置，每个目录算法一行，由系统创建 | 是否启用、参数、权重、训练时间预算、采用门槛（晋升策略）；对所有模块生效 |
| 模块（`ForecastModule`） | 一个 Kubernetes 工作负载（命名空间 + Deployment/StatefulSet/DaemonSet + 名称），一个工作负载一个模块 | 自己的指标来源（含阈值）、历史窗口、预测周期、数据质量限制，以及推荐如何落到工作负载（模式、上下界、Fallback、余量、固定周期、OOM/堆保护、副本上限策略） |
| 指标来源（`MetricSource`） | 模块的一个 Prometheus 指标（经指标映射） | 用途、权重、统计方式、阈值、取值依据 |
| 关联 | **不存在手动关联**：每个模块自动使用所有已启用的算法 | 新增模块即得到全部已启用算法；启用或新增算法即应用到全部现有模块，下一个周期生效 |

不再有：算法实例、绑定关系、按实例的指标来源、按实例的预测。上一版的 `algorithm_instance`、`module_binding` 表退役（迁移见第 6 节）。

内部派生对象 `model.AlgorithmRun` 只是“某个启用算法在某个模块上运行”的内存视图，**不落库**：历史序列、预测、回测、评估都按 `module_id` 存储，`algorithm_id` 列存算法 id。

## 2. 周期与结果

`forecast.run` 对每个启用的模块：采集历史（每个来源只采一次，与算法个数无关）→ 每个启用算法预测每个预测来源 → 模块推荐。`forecast.backtest`、`forecast.evaluate`、`forecast.score` 同样逐模块进行。一个算法跑不起来（缺必填参数、历史不足、超时）只在它自己的结果里报错，不影响其他算法，也不让任务失败。

推荐：每个算法按“基线或已晋升的回测 + 预测新鲜 + 综合分 ≥ 模块最低分”判定资格，按 `综合分 × 算法权重` 排名（切换有迟滞），按资源取排名最前的可用算法；多指标合成只用模块自己的来源（按权重加权平均，不再有合并方式设置）；没有可用算法时走模块 Fallback 并写明原因。

默认启用的算法：不需要参数的基线（如 `naive`）。`seasonal_naive` 需要周期长度、重型算法（RNN/GRU/LSTM 等）开销大，创建时默认停用，由管理员在算法管理页启用。

## 3. 算法参数的注意点

参数是平台级的，所有模块共用一套。**按步数计的参数**（周期长度、滞后、窗口）以指标映射的步长为单位，因此各模块的指标映射步长应一致；步长不同的模块要用同一个算法时，需要按最小公倍的步长设置，或把它们的映射统一。算法编辑页的每个参数都有问号提示：含义（来自算法定义）、计算作用、配置依据（前端文案 `page.forecast.algorithms.paramHelp.<参数名>`）、取值范围与默认值。

## 4. 接口

| 路径 | 说明 |
|---|---|
| `GET /algorithms?page&page_size&keyword&category&enabled` | 算法目录与各自的平台设置（分页、搜索、过滤），含参数定义 `params` 与当前取值 `values` |
| `GET/PUT /algorithms/:id` | 读取、修改一个算法的设置（`enabled`、`params`、`weight`、`train_budget_seconds`、`promote_*`、`description`） |
| `GET /algorithms/:id/schema`、`/usage` | 参数定义；各模块上的表现 |
| `/modules`（CRUD，创建时可带 `sources`）、`/modules/:id/sources`（CRUD）、`/modules/:id/overrides` | 模块、指标来源、固定周期 |
| `/modules/:id/{history,collect,predict,run,backtest,backtests,evaluate,trace?algorithm=}` | 数据与预测链路（预测/回测对所有启用算法） |
| `/modules/:id/{recommend,score,ranking,leaderboard}` | 推荐、评分、排名、排行榜 |
| `GET /forecast-metrics/{catalog,query}` | 指标目录与查询，统一用 `module_id` |

查询预测、回测、评估用 `module_id` 与 `algorithm_id`；已移除 `/algorithm-instances`、`/bindings`。

## 5. 菜单与页面

- 模块管理：模块列表（指标来源移到监控指标的“Prometheus 指标数据集”）。
- 预测管理：算法管理（分页、搜索、新增/编辑/删除自定义公式算法；启用开关、参数、权重、采用门槛；详情含各模块上的表现与追溯）、算法评分、当前排名、历史排名。
- 预测结果：历史数据、回测、实测评估、排行榜、趋势对比、预测与训练记录——统一按模块选择。
- 推荐与执行：推荐快照、执行记录、降级记录、准入记录、审计导出。
- 监控指标：Prometheus 指标数据集（按模块管理指标、数据处理、数据周期、算法需求）、指标目录（只列指标定义，可跳到曲线）、指标曲线（独立页面，按模块画任意目录指标的曲线）。
- 授权管理：独立的一级菜单。菜单种子每次都把父级重置为种子的值，所以旧库里挂在别处的菜单会被放回一级。
- 列表页统一：同一套搜索卡片与表格面板，同一组时间窗口（6 小时、24 小时、7 天、30 天），模块选择可搜索，操作列统一叫“操作”、按钮统一用小尺寸。

## 6. 迁移

由上一版（算法实例形态）升级：历史、预测、回测、评估、评分按实例归档，无法映射到模块，**删除并打印清单**，由模块重新采集；每个模块取它第一个已启用绑定的实例的指标来源与历史窗口/预测周期；已启用实例的算法在平台设置里启用，参数取该算法第一个实例的参数；`algorithm_instance`、`module_binding` 由退役清理删除。更早的形态（模块上的指标槽位）不再转换，需重新添加来源。转换幂等。

## 7. 验证状态

- 已验证：单元与集成测试（SQLite）；全新库上的浏览器验收（`scripts/e2e/modules-algorithms.mjs` 92 项，`platform.mjs`）。
- 未验证：MySQL/PostgreSQL 上的迁移与新唯一索引；真实 Prometheus；真实集群（Kyverno 策略仍是参考草案）。
- 已知限制：平台级参数按步数计，见第 3 节；模块的 Service、容器仅作 PromQL 变量，不参与工作负载匹配。
