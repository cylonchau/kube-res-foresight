# 算法框架、Prometheus 数据集与评分页面：规格

对应 2026-10-25 的「Kubernetes 预测扩缩容系统优化需求（精简分类后）」。在 [module-algorithm-spec](module-algorithm-spec.md)（模块 → 自动关联所有启用算法）的基础上补充：可扩展算法框架、自定义公式算法、训练记录、模块数据集、数据周期、拆分后的评分页面。与之冲突处以本文为准。

系统结构：**模块管理 → Metrics 数据集 → 算法框架 → 自动训练/预测 → 算法评估排名 → 预测结果 → Controller/Kyverno 执行**。

## 1. 算法框架

算法只通过注册表进入平台，列表、表单、校验、训练、评分都从注册表出发，没有别处写死算法名：

- **Go 实现**：`forecast.Register(forecast.Implementation{Definition, Build, Check})`。`Definition` 带参数 Schema（名称、类型、默认值、取值范围、单位、说明），`Build` 用已校验、已补默认值的参数构造 `forecast.Model`，`Check` 校验各自合法但组合非法的参数。内置算法（naive、seasonal_naive、ewma、macd_trend、holt_winters、hist_percentile、random_forest、knn、naive_bayes、rnn/gru/lstm、ridge_ar、gbdt）都是这样注册的（`pkg/forecast/registry.go`、`builders.go`）。新增 Go 算法只需实现 `Model` 并注册，配单测。
- **公式算法**（自定义算法）：管理员在页面给出 **参数 Schema + 公式**，不上传代码。`pkg/forecast/formula.go` 是平台自带的解释器：词法/语法分析一次，按节点计数的运算预算内执行；没有循环、赋值和任何函数表之外的调用。变量：`x`（历史，从旧到新）、`k`（向前第几步）、`h`、`n`、各参数名、`c_<来源键>`（同一模块其他指标的历史，要求在同一栅格上）。函数：`last mean sum std median smin smax pct ema lag diff slope linfit season macd macdsig abs sqrt exp log floor ceil round pow min max clip if`，运算符 `+ - * / % ^ < <= > >= == != && || !`。每步结果不得为负、不得为 NaN/无穷；除零、对数域错误都会报错并指明位置。`递归多步预测`开启时，每步的预测值追加到 `x` 再算下一步。
- **保存前真实试算**：`forecast.SetCustom` 编译公式，并在合成的日周期序列上用默认参数真的预测一次；不能运行的公式在保存时被拒绝，而不是第一次定时执行时才失败。编辑公式后实现版本（`formula-<哈希>`，哈希涵盖公式与参数 Schema）变化，模型版本随之变化，旧预测与晋升不会被误当作新模型的。
- **多实例一致**：自定义算法存在 `algorithm_config`（`formula`、`param_schema`、`min_samples`、`recursive`、`category`），每个服务实例在 `EnsureAlgorithms` 里把数据库里的自定义算法同步进本进程注册表（公式未变则跳过），已被删除的注销；一个公式编译失败只记日志，不影响其他算法。
- **生命周期**：新增（`POST /algorithms`，id 为小写字母/数字/下划线、3–40 位、不得与内置重名）、编辑（`PUT /algorithms/:id`，自定义算法可改公式、参数 Schema、最少样本、递归、类别；内置算法拒绝）、启用/停用、删除（`DELETE`；仅自定义算法，且需先停用；已产生的预测、回测、评分记录保留）。`POST /algorithm-formulas/check` 是编辑器用的校验并试算（返回错误位置，或合成历史与公式预测），`GET /algorithm-formulas/functions` 返回可用的变量与函数。
- **类别**：baseline、statistical、technical（技术指标）、machine_learning、deep_learning、custom。MACD/EMA 等技术指标只是预测算法之一（`macd_trend` 与可由公式表达的 EMA/MACD 类算法），不是固定的唯一逻辑；金融指标只用作构造资源趋势特征的参考，不产生买卖信号。

## 2. 训练记录与“模型保存”

平台**不保存模型权重**：统计、树、神经网络算法每次都在历史窗口上重新拟合（固定随机种子，结果可复现）。取而代之的是**训练记录 + 可复现版本**：每个算法在每个指标上的每次运行写一条 `forecast_training_run`——历史窗口（起止、样本数、步长）、预测步数与产出点数、实现版本与模型版本（实现、参数、数据来源的哈希）、参数 JSON、自定义算法当时的公式与参数 Schema、协变量及未能提供的原因、耗时、状态（成功/跳过/失败）与原因。预测值本身按 `forecast_prediction` 持久化。同一实现版本、参数与历史窗口得到同一组预测。记录保留 30 天（`Retention.Trainings`），可经 `GET /training-runs`（分页、按模块/指标/算法/状态/时间筛选）与 `GET /training-runs/:id` 读取，页面「预测与训练记录」。

## 3. Metrics 数据集

数据集属于模块，所有启用的算法共用，不允许每个算法另维护一套。每个指标来源（`metric_source`）新增：

| 字段 | 含义 |
|---|---|
| `scale` | 写入前对查询结果乘的系数（0.001：毫核→核；0.01：百分比→比例） |
| `precision` | 保留的小数位（0–12，默认 6） |
| `aggregation` | 模块粒度大于查询步长时，同一粒度内样本的合并方式：avg、max、min、last、sum |
| `missing` | 准备预测时缺失步的处理：`interpolate`（线性插值）、`forward_fill`（沿用上一个值）、`zero`（填 0，OOM 与错误率的默认值）、`skip`（不填充，断点处截断历史） |

指标按类别分组（`model.KindCategory`）：CPU（使用量、利用率、Requests/Limits、限流）、内存（工作集、JVM Heap/Non-Heap、利用率、Requests/Limits）、GPU（使用卡数、利用率、显存使用量与容量）、网络（接收/发送速率、连接数、错误率、延迟）、其他（RPS、OOM、GC、JVM 线程、副本数、自定义）。新增的类型：`cpu_util`、`memory_util`、`gpu_util`、`gpu_memory_total`、`network_conn`、`network_error`。单位由类型决定，自定义类型取查询的单位；换算系数负责把查询结果对到该单位。

**自定义 PromQL**：创建指标来源时可以不选映射，直接写 PromQL（数据源、层级、步长、自定义类型的单位）；系统先校验（占位符只能是 `{{namespace}} {{workload}} {{service}} {{pod}} {{container}} {{range}}`，层级要求的变量必须出现）并创建映射，再创建来源；来源创建失败时删除刚建的映射。

**栅格与聚合**：模块的采样粒度（`step_seconds`）是数据集的时间栅格。查询仍按映射的步长执行，`collector.Shape` 把查询点聚合到栅格上：一个点标在桶的**结束时刻**，只含该时刻之前的样本（不看未来），只保留完整的桶；先聚合再换算再四舍五入。栅格必须是查询步长的整数倍、不得更细，否则采集报错、模块与来源的保存被拒绝。更改映射、栅格步长或换算/精度/聚合时，已采集的序列因形态签名（`metric_series.shape`）变化被丢弃并按新设置重新采集；升级前的序列（签名为空）按默认形态处理，不被清空。

## 4. 数据周期

模块新增：`step_seconds`（采样粒度，0＝沿用各指标查询的步长）、`window_steps`（训练窗口长度：算法只用最近这么多步，0＝整个历史范围；窗口之前的历史仍保留，改窗口不必重新采集，窗口进入模型版本）、`min_points`（一个指标至少多少个连续点才预测，叠加在算法自身要求之上）、`run_interval_minutes`（调度任务多久为这个模块执行一次，0＝每个任务周期）。页面提供 5m、15m、1h、1d、30d 预设与自定义输入（组件 `duration-input`）。

保存时校验彼此自洽：粒度在 15 秒到 1 天之间；历史范围 ≥ 最少数据量 × 粒度；训练窗口 ≥ 最少数据量；预测跨度 ≥ 一个粒度；执行频率 ≥ 一个粒度（两次执行之间没有新数据）；粒度与每个启用来源的查询步长相容。

**算法需求不是固定数据量**：`GET /modules/:id/requirements` 对每个启用算法与每个预测来源，按算法**当前参数**构造模型取它的 `MinSamples`，乘以该来源的栅格得到需要的历史时长，与历史范围（有训练窗口时取窗口）在该栅格下能容纳的步数比较，不足时说明怎样修（延长范围或改粗粒度）。例如 15 分钟栅格上周期为一天的季节基线需要 96 步，换成 1 小时只需要 24 步。历史不够时算法被跳过（记入训练记录），不会用补出来的数据硬算。`forecast.run` 按 `run_interval_minutes` 与最近一次训练记录的时间决定模块是否到期。

## 5. 评分、排名与页面

评分拆成三个页面，不再堆在一张表里：

- **算法评分**（`forecast_score-records`）：每次评分每个算法一条记录；详情给出各部分（副本数、CPU、内存、GPU）的准确度、误差、权重份额和对综合分的贡献，加上数据完整度、OOM 扣分、窗口数。
- **当前排名**（`forecast_ranking`）：按模块展示最新排名、使用中与挑战中的算法、最优算法；第二个页签是**指标评分**：把每个算法在每个预测来源上的预测与对应时间的 Prometheus 实际值逐点比较，给出每个指标的误差（WAPE）、准确度（1−误差）、MAE、RMSE、偏差、峰值低估、覆盖率与权重，以及每个算法的综合结果（`GET /modules/:id/metric-scores`）。**统一标准**：只取所有被测算法都有实测的来源，按来源权重（模块上设置）归一后加权；没有共同来源时各自用自己的来源并标注“仅供参考”；综合分 = 100 × 加权准确度，同时对比最优基线。
- **历史排名**（`forecast_ranking-history`）：每次评估的排名都保留（`GET /algorithm-rankings/history`，按模块与评估时间成组），可看不同模块、不同时间最优算法的变化与当时使用的算法，并有各算法评分随时间变化的曲线；`GET /algorithm-rankings/current` 给出各模块当前排名。

预测结果与推荐执行页面：回测、实测评估、准入决策补充详情弹窗；新增「预测与训练记录」页（分页、搜索、筛选、详情）。

## 6. 菜单与迁移

新增页面：算法评分（`forecast_score-records`）、当前排名（`forecast_ranking`）、历史排名（`forecast_ranking-history`）、预测与训练记录（`forecast-results_runs`）、Metrics 数据集（`monitoring_datasets`）。退役：`forecast_scores`（授权带到前三者）、`forecast-modules_sources`（授权带到 `monitoring_datasets`）；`monitoring_metrics` 仍是指标目录。升级用 AutoMigrate 加列并保持原行为：旧来源得到 scale 1、precision 6、avg、interpolate，模块 `step_seconds`/`min_points`/`run_interval_minutes` 为 0，`algorithm_config` 的公式字段为空，`forecast_training_run` 表新建。端到端升级测试（上一版结构的库）覆盖这些默认值与授权带走。

## 7. 验证状态与限制

- 已验证：Go 单元与集成测试（公式引擎的语法、类型、预算与错误、自定义算法注册与版本变化、数据集栅格/聚合/换算/缺失/重建、训练记录、评分三个接口与指标评分的口径、执行频率、迁移默认值与授权）；全新 SQLite 库上的浏览器验收（`scripts/e2e/modules-algorithms.mjs` 92 项、`platform.mjs`）：页面里新增自定义算法并看到试算曲线、错误公式被拒、保存、删除；数据集页的周期弹窗、算法需求、自定义 PromQL；评分与训练记录详情。
- 浏览器里**没有**可对比的实测数据（预测的目标时间尚未到来），指标评分的数据路径由 `TestMetricScoresRankTheAlgorithmsOnTheEvaluationsOfTheRealPipeline`（预测→推进时钟→采集→评估→评分）和接口测试验证。
- 未验证：真实 Prometheus（自定义 PromQL、预设）、真实集群、MySQL/PostgreSQL（新增列、`forecast_training_run`、历史排名的分组查询只在 SQLite 上跑过）、真实负载上的准确率。
- 已知限制：公式算法只读数值与布尔参数；平台级参数仍按步数计（见 module-algorithm-spec 第 3 节），各模块栅格不同时需要分别考虑；算法需求只报告不够，不自动延长历史范围；预测目标仍是各来源的值，没有跨来源的联合目标。

## 8. 2026-10-26 调整：派生算法、模块直接驱动、页面规范

- **派生算法（variant）**：新增算法先选「实现方式」。「基于内置算法」= 选类型（基线、技术指标、统计、机器学习、深度学习）再选该类型的一个内置算法，表单只显示**这个算法**的参数（Naive 没有参数，Seasonal Naive 配置季节周期，EWMA 配置平滑系数，其余按各自的注册表 Schema），保存为一个有自己 id 和参数值的新算法；与原算法同时参与评分排名。后端：`algorithm_config.base` 列、`forecast.SetVariant`（复用基算法的构造器、Check、参数 Schema 与 ImplVersion，`Baseline` 置 false，没有参数的算法拒绝派生）、`POST /algorithms` 带 `base`；`GET /algorithms` 每项有 `origin`（builtin / variant / formula）和 `base`；编辑沿用内置算法的参数表单，停用后可删除。「自定义公式」仍是参数 Schema + 公式。`macd_trend` 的类别改为「技术指标」。
- **模块是唯一配置，不再生成 CRD**：平台从不生成 CRD；PredictiveScaler 只是 operator 的输入，与模块里的上下界/兜底/节奏重复且会不同步。operator 新增 `--platform-url` 模式（`GET /api/v1/operator/modules`，权限随 `forecast:operator:use`），直接按模块配置经 scale 子资源调整副本，细节见 control-plane 技能。模块新增 `scale_up_max_step`、`scale_down_max_step`、`scale_up_cooldown_seconds`、`scale_down_cooldown_seconds`（0 = 不限制）。
- **指标一致性**：指标目录（`monitoring_metrics`）改为 Prometheus 指标的统一入口，三类条目在同一张列表里：数据映射（与「数据指标映射」同一份数据，可新增/编辑/删除）、内置模板（只读，可一键添加为映射）、平台导出（只读，可看曲线）。映射表单与「内置指标映射库」共用 `metric-mapping-form.vue`：PromQL 可由模板自动生成（`POST /metric-presets/:id/apply` 现在接受 `query` 覆盖），也可以完全手写。
- **排名页面**：当前排名、指标评分、历史排名的明细改为排行榜形式（`rank-lane.vue`：名次、得分条、各部分准确度、名次变化），不再用表格；`GET /modules/:id/ranking` 每项带 `previous_rank` / `previous_score`（上一次评估），页面据此显示名次变化。
- **预测结果页面**：趋势对比与指标曲线拆成图表、摘要卡片（每条曲线一张，预测卡片标出峰值相对实测的偏差）、分页的数据点三个独立区域（`metric-analysis.vue`）。训练记录页加运行概览（`GET /training-runs/summary`：成功/跳过/失败、各算法、未产出预测的常见原因），菜单名改为「训练记录」；用途：算法是否按时运行、没有预测时为什么、一个预测怎么得到并可复现。
- **审计导出**：主页面只留导出历史；导出类型、模块、范围在「导出」按钮打开的弹窗里选择。
- **发现并修复**：`page.forecast.auditExports.help` 含 `@`，vue-i18n 把它当作链接消息，编译失败，整页空白；locales 测试现在对每条消息跑一遍 vue-i18n 编译，这类问题在测试里就会失败。

## 9. 问题修复版（列表规范、真实历史、K 线对比、指标查询测试）

### 9.1 全局页面规范（所有列表与图表页）

- **列表**：统一用 `ListSearchCard` + `ListTablePanel`（或排名用 `RankList`），支持搜索、筛选、分页、复选框、批量操作；不在页面里直接放裸表格。操作列固定在右侧、`small` 按钮、编辑为 primary ghost、删除为 error ghost、居中，宽度按按钮实际所需，不留白、不溢出（e2e 逐页检查）。
- **批量操作**（`BatchActions`）：模块与算法有启用/停用/删除（`POST /modules/batch`、`POST /algorithms/batch`，逐项返回成功数与未处理原因）；记录类列表（评分、回测、实测评估、训练、执行、准入）只有批量删除（`POST /records/batch-delete`，白名单 `scores backtests evaluations trainings executions admissions`；推荐快照与降级回退是审计链路，没有批量删除）；排名、历史排名、算法实测排名、历史数据、指标曲线的批量操作是导出所选为 CSV；指标目录只有“数据映射”行可批量删除。删除无实际用途的操作：模块列表的“运行”（采集/预测/回测/评分/评估/推荐由调度任务与各结果页触发）已删。
- **时间筛选**：统一为开始时间、结束时间选择器（`TimeRangeField`，附 1h/6h/24h/7d/30d 快捷项），参数为 `from`、`to`（RFC3339），不再用“最近 N 小时”作为唯一方式。资源推荐快照与降级回退按要求本版不改。
- **数值与单位**（`utils/format.ts`）：预测值、图表值最多 3 位小数；字节自动换算（67108864 → 64 MiB），CPU 不足 1 核显示 mCPU，其余显示核；比例显示百分比；列表与提示里不再写 `CPU (cpu)`、`副本数 (replicas)` 这类重复括号。
- **滚动**：排名列表、数据点列表自带滚动区与分页；图表页整页可滚动，e2e 检查无横向滚动。

### 9.2 数据口径与时间对齐规则

1. **实际值来自 Prometheus，不来自平台已采集的样本**：`GET /modules/:id/compare` 与历史/曲线页在请求时刻按模块数据集的映射（同一命名空间、同一工作负载）向 Prometheus 查询所选区间，包括模块创建之前已有的历史；查询经过与采集相同的整形（步长、换算系数、精度、聚合）。
2. **历史缺失的判定**：只有 Prometheus 在整个区间没有任何样本才是“历史缺失”（`missing=true`）；区间内个别步长没有样本只算缺口，在质量报告里列出。
3. **同一对象**：实际指标、实际副本数（模块里 kind=replicas 的来源）与预测都属于同一个模块的同一组 Pod，不混用其他对象的数据。
4. **时间网格**：网格步长 = 模块数据集步长；展示桶 = 整数个步长，使点数不超过上限（`bucket_seconds` 随响应返回）；每个点标在桶的**起始**时刻。实际值是桶内的开/高/低/收/均（K 线）；预测值是落在桶内的各目标时刻预测的均值。
5. **预测的取舍**：一个目标时间有多次预测时取**预测时间不晚于目标时间的最新一次**（晚于目标时间做的预测是事后诸葛，丢弃）；只用中位数（分位 500）。
6. **预测副本数**：只对 CPU 预测、且模块声明了每 Pod CPU 请求（动态副本 + Pod CPU request）时给出：`ceil(预测值 × 余量 ÷ (Pod CPU request × 目标利用率))`，夹在模块最小/最大副本之间；否则为空，并在 `replica_basis` 说明原因。预测副本数是推导值，不是执行结果；实际副本数是 Prometheus 里的观测值。
7. **评分与实测排名**使用同一份历史：只取所有被测算法都有实测值的指标按权重归一，各算法因此可比；覆盖所有已启用且有有效实测的算法（动态读取，不写死个数）。
8. **指标查询测试**（`POST /metric-queries/test`）：先校验模板变量与层级，再由 Prometheus 解析和执行；`stage` 为 template / datasource / syntax / query / empty / ok，原样给出 Prometheus 的错误；结果按换算系数与精度整形，与数据集存储的值一致，窗口最长 7 天，不写库。新增自定义 PromQL 必须先通过测试才能保存。

### 9.3 各页面的操作权限范围

权限沿用 RBAC：读 = `GET /api/v1/*`（`sys:all:read` 或页面菜单权限），写 = `POST/PUT/DELETE`（`sys:all:write`），运行时接口有各自的按钮权限（`forecast:operator:use`、`forecast:admission:read`、`forecast:audit:export`、`forecast:query:test`）。

| 页面 | 只读可做 | 需要写权限 | 说明 |
|---|---|---|---|
| 模块列表 | 查看、跳转数据集 | 新增、编辑、克隆、固定周期、旁路/恢复、删除、批量启用/停用/删除 | 删除前必须先停用 |
| Metrics 数据集 | 查看、查询测试（`forecast:query:test`） | 新增/编辑/删除指标、数据周期、批量删除 | 自定义 PromQL 保存前必须测试通过 |
| 指标目录 | 查看、测试查询 | 映射的新增/编辑/删除/批量删除、从模板添加 | 模板与平台导出只读 |
| 指标曲线 | 全部 | — | 只读，数据来自 Prometheus |
| 算法管理 | 查看、详情 | 新增、编辑、启用/停用、删除、批量启停/删除 | 内置算法只能停用不能删除 |
| 算法评分 / 历史排名 | 查看、详情、导出所选 | 批量删除评分记录 | |
| 当前排名 | 查看、导出所选 | 立即评分 | |
| 历史数据 / 预测趋势对比 | 全部 | — | 只读 |
| 滚动回测 / 实测评估 / 训练记录 | 查看、详情、概览 | 批量删除 | |
| 算法实测排名 | 全部（导出所选） | — | |
| 资源推荐快照 / 降级与回退 | 查看 | — | 本版不改 |
| 扩缩容执行记录 / 准入记录 | 查看、详情 | 批量删除 | 准入记录页不再带审计导出入口 |
| 审计导出 | 查看导出历史 | 导出（`forecast:audit:export`） | 导出在弹窗里选类型与范围 |

### 9.4 验收标准与完成判定

完成 = 下列全部成立，且 `scripts/e2e/modules-algorithms.mjs` 全绿、`go test`、`vue-tsc`、lint、locales 测试通过：

1. 每个列表页都能搜索、筛选、分页、勾选，批量操作按 9.1 的表格工作；时间筛选是起止选择器。
2. 模块表单里最小/最大/兜底副本与恢复周期是模块配置，“安全边界”里不再有副本上下限；operator 读取的就是这些值。
3. 历史数据页能显示模块创建之前的 Prometheus 历史；区间内无任何样本才显示“历史缺失”；K 线、算法多选对比、预测/实际值与副本数的逐时间点提示与 9.2 一致。
4. 趋势对比覆盖所有已启用且有预测的算法，没有预测摘要；数值最多三位小数，单位可读。
5. 训练记录的统计（命中率、成功/跳过/失败及其含义）全部来自 `forecast_training_run`；记录能对应到模块、数据集（指标来源）、训练时间、模型版本。
6. 新增或修改数据集指标时可以直接测试 PromQL，看到错误、数值与曲线后再保存。
7. 排名区域可滚动；数值精度、单位换算、无横向滚动由 e2e 检查。
8. 验证范围以 e2e/测试的实际结果为准；指标评分排行榜的有数据路径在浏览器里用拦截的 API 应答验证（浏览器里没有可对比的实测值）。
