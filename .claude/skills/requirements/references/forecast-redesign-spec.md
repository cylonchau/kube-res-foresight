# 预测系统页面与功能重构：现状核对、菜单、验收标准与降级规则

> **注意（2026-10-25）：** 算法已改为平台级统一配置并自动应用到所有模块，指标来源回到模块，不再有算法实例与绑定。本文中与此冲突的描述以 [module-algorithm-spec.md](module-algorithm-spec.md) 为准。

对应 2026-10-24 提出的「预测系统功能与页面优化」6 条需求。本文是实施前的补充规格：先把菜单、验收标准、降级规则定下来，再动手；与项目既有约束冲突或需要你拍板的地方集中在第 0 节。

## 0. 需要先确认的事项（含我采用的默认假设）

| # | 事项 | 冲突 / 不确定点 | 我采用的假设（确认前不动手改相关部分） |
|---|---|---|---|
| A | **MACD** | 现有项目规则（algorithm-library 与 AGENTS）明确「金融技术指标不进入正式算法」。需求里把 MACD 当作应提供三个参数的算法。 | 放宽该条：实现 `macd_trend`（单变量，不读取协变量），只取 MACD 的计算结构（快 EMA、慢 EMA、信号线 EMA 三个周期参数 `fast_span`/`slow_span`/`signal_span`），用作**资源趋势修正**（动量为正则上调预测，为负则下调，幅度有 `max_adjustment` 上限，结果不低于 0），不产生买卖信号，也不引入价格/OHLCV 概念；仍必须在回测中胜过基线才晋升。若你要求保持原规则，则只做 EMA 的完整参数，不做 MACD。 |
| B | **多指标共同预测** | 现有 `forecast.Input` 只有单条序列，六个算法都是单变量。 | 多指标 = 「目标指标 + 协变量」：目标指标仍是被预测的那条序列；协变量（JVM、OOM 计数、Requests/Limits、其他资源指标）只提供给**支持协变量的算法**（岭回归 AR、GBDT、macd_trend 的修正项）作为滞后特征。基线与 Holt-Winters、EWMA 仍是单变量，不假装使用多指标。 |
| C | **LDAP 配置位置** | 现有 LDAP 是独立页面 + 专用 ldap_setting / ldap_group_mapping 表，挂在授权管理下。需求：并入系统参数，不要单独页面。 | LDAP 作为**系统参数的一个分类**（键前缀 `ldap.`，密码与 CA 加密存储且不回显），页面内提供「测试连接」「立即同步」两个操作；组→角色映射作为一个多行配置项 `ldap.group_role_mappings`（每行 `目录组=角色编码`，保存时校验角色存在）。删除 authorization_ldap 菜单与页面；旧表数据在 `--upgrade` 时迁入系统参数（先打印清单）。登录、同步任务、本地账号永远本地认证等行为不变。 |
| D | **Java/JVM 指标** | 当前指标种类只有 cpu、memory、rps、latency、oom、replicas。 | 新增指标种类（见 4.2），预设按 Micrometer / JMX exporter 的常见名称给出，**全部标「未验证」**，不声称在真实集群有效。 |
| E | **「降级」与现有 Fallback 的关系** | 现有 Fallback 只有「模块固定默认值」一层。 | 沿用并扩展为分层降级链（第 5 节），Fallback 仍是模块级、可单独配置的最后一层。 |

## 1. 现状核对（与需求逐条对照）

| 需求 | 现状 | 差距 |
|---|---|---|
| 算法结构、历史与趋势页面不规范 | 算法页是两个标签页、历史页是图表+质量标签，规范项（列设置、搜索卡片、问号提示、无标签堆叠）只部分满足 | 需按拆分后的菜单重做，参数/输入区按统一布局 |
| 推荐与审计页 | 一页两个标签（快照、扩缩容），导出按钮；推荐详情有 JSON 与副本解释 | 拆成独立菜单；快照要结合多维指标、OOM 保护；审计要有降级记录与可追溯 |
| 算法参数可自调 | 参数 schema 已可编辑（含模板）；EMA 只有 alpha；无 MACD | 补参数（span/偏差修正/初始化方式等）、MACD、协变量选择、历史范围 |
| 读取哪些指标 / 多指标 | 一个模块一个目标指标映射；绑定只有算法+参数 | 绑定要能选择协变量指标与历史天数 |
| OOM / Java / K8s 指标 | OOM 指标种类与预设存在但不参与预测与推荐；无 JVM；K8s requests/limits 无指标 | 新指标种类、预设、采集、特征、推荐影响 |
| 模块管理与 Fallback | 模块已有 fallback_cpu/memory/replicas 与边界；降级只有「无可用预测→fallback→保持原值」 | 不覆盖历史不足阈值、预测异常标记、协变量缺失、OOM 地板；缺少降级可见性 |
| 推荐用固定值 | 推荐由预测峰值×余量/动态求解得出，不是固定值；但 CPU/内存仍只看自身峰值 | 内存应考虑 OOM 历史与 limits；CPU 考虑 requests/limits |
| LDAP 位置 | authorization_ldap 独立页面 | 见假设 C |
| 指标页布局 | 查询卡片用 24 栅格塞多个控件并有预设按钮，下方图表+摘要表 | 按拆分后的页面重做 |
| 菜单名称 | 「预测模块」「算法」「指标」等过短 | 见第 2 节 |

## 2. 菜单方案（拆分后）

一级菜单与二级菜单、路由、内容与旧页面处置。权限码沿用 `<一级>_<二级>` 命名，旧的 BUTTON 权限（operator/admission/audit）不变。

| 一级 | 二级菜单（显示名） | 路由 | 内容 | 来源 / 处置 |
|---|---|---|---|---|
| **数据接入** | 数据源连接 | /datasource/connections | Prometheus 与演示数据源 | 原「连接管理」改名 |
| | 指标映射 | /datasource/metric-mappings | 指标种类（含新增 JVM/K8s 配置类）与 PromQL | 原页，新增指标种类 |
| | 内置指标预设库 | /datasource/presets | 预设、演示数据 | 原「指标预设」改名 |
| **预测管理** | 预测模块管理 | /forecast/modules | 模块基本信息、数据映射、边界、**Fallback 默认值**、副本求解、固定周期、算法绑定入口 | 原「预测模块」，Fallback 区独立成块 |
| | 预测算法与参数模板 | /forecast/algorithms | 算法目录、参数 schema、参数模板、使用情况与追溯 | 原「算法」，拆去模板页签：目录与模板各自独立标签→ 改为两个二级菜单见下 |
| | 算法参数模板 | /forecast/algorithm-templates | 模板列表、编辑、同步 | 从「算法」页拆出 |
| | 预测输入指标集 | /forecast/input-sets | 命名的「输入指标集」：目标指标 + 协变量清单 + 历史天数 + 缺失处理；被模块绑定引用 | 新增（需求 2） |
| **预测结果** | 历史数据与缺样 | /forecast/history | 采集历史曲线、质量标签、缺口列表 | 原「历史与缺样」按规范重做 |
| | 滚动回测记录 | /forecast/backtests | 回测列表（server 分页、可按算法/晋升筛选） | 从「回测与评估」拆出 |
| | 实测评估记录 | /forecast/evaluations | 已回填实测的预测运行 | 从「回测与评估」拆出 |
| | 算法排行榜 | /forecast/leaderboard | 按实测排名，附 OOM/协变量使用情况 | 从「回测与评估」拆出 |
| | 预测趋势对比 | /forecast/trends | 多算法预测 vs 实测曲线（原指标页的「预测 vs 实测」） | 从「指标」拆出 |
| **推荐与执行** | 资源推荐快照 | /forecast/recommendations | 不可变推荐版本、决策说明、来源 | 从「推荐与审计」拆出 |
| | 扩缩容执行记录 | /forecast/executions | operator 上报记录 | 从「推荐与审计」拆出 |
| | 降级与回退记录 | /forecast/fallbacks | 所有用了 Fallback/保持原值/OOM 地板的推荐及原因 | 新增（需求 4，第 5 节） |
| | 审计导出 | /forecast/audit-exports | CSV 导出入口与导出历史（导出本身记入操作日志） | 从「推荐与审计」拆出 |
| **监控指标** | 指标查询与目录 | /forecast/metrics | 指标目录、任意指标曲线查询、CSV 导出、保留期说明 | 原「指标」重做；删去「预测 vs 实测」预设按钮（移至趋势对比） |
| **任务中心** | 不变 | | | |
| **系统管理** | 系统参数 | /system/config | 含新增分类「LDAP 认证」 | 吸收 LDAP |
| | 登录日志 / 操作日志 / API 凭据 | | 不变 | |
| **授权管理** | 用户 / 角色 / 菜单 | | 不含 LDAP | 删除 authorization_ldap |

迁移约束：菜单由迁移种子创建；旧菜单码（forecast_evaluation、forecast_recommendations、authorization_ldap、forecast_algorithms 的模板标签）在 `--upgrade` 时从角色授权中**映射到新码**（拥有旧「回测与评估」的角色自动获得三个新菜单），旧码加入 retiredRouteCodes（请求返回 404）。首页仍为预测模块管理。

## 3. 各功能验收标准

通用：每项都要有真实后端 + 前端，不接受只有静态页面或占位菜单；页面满足 frontend.md（搜索卡片经 list-search 自动提交、TableHeaderOperation、ExpandableDataTable、自适应分页、列设置入库并登记 PageModelMap、默认列数不超过 `ui.table.default_visible_columns`、说明用问号提示、中英文无残留键）；`go test ./...`、vue-tsc、locales 测试、oxfmt、构建通过；浏览器验收脚本覆盖并检查 1440/1024/390 宽度无横向溢出。

### R1 菜单与页面规范
1. 第 2 节每个菜单可访问、有真实数据、权限码生效；旧菜单码返回 404，授权迁移后角色不丢权限（迁移测试：含旧菜单授权的库升级后可访问新菜单）。
2. 菜单与页面标题长度 ≥ 4 个汉字且含对象，不再出现「指标」「算法」这类单词名。
3. 指标查询页：查询区为一张搜索卡片（模块、指标、维度、时间范围、聚合步长），控件不挤压换行错位（浏览器检查在 1440/1024 下查询区高度不超过 2 行）；结果区为图表卡片 + 序列摘要表（标准表格、可导出）；无预设按钮混在查询区。
4. 历史、回测、评估、排行榜、趋势、快照、执行、降级、审计各页：搜索条件服务端生效；列表服务端分页；回测/评估/快照/执行/降级支持服务端排序（至少按时间与关键指标）。

### R2 算法参数与多指标输入
1. 参数完整：EWMA 增加 `span`（与 alpha 二选一，互斥校验）、`bias_correction`；Holt-Winters 保持现有参数；新增 `macd_trend`（`fast_span`<`slow_span`，`signal_span`，另含 `max_adjustment` 上限）；岭回归/GBDT 增加 `covariates`（引用输入指标集）的使用开关。每个参数有中文说明、默认值、取值范围，非法值被拒绝（参数校验测试覆盖）。
2. **输入指标集**：可创建/编辑/删除（被引用不可删）；内容为目标指标 + 协变量（从该模块已映射的指标中选）+ 历史天数（1–180）+ 缺失处理（丢弃协变量 / 判为降级）。绑定算法时选择输入指标集或「仅目标指标」。
3. 协变量真实进入模型：使用协变量与不使用协变量的同一算法在同一数据上结果不同（单测，且协变量不含未来值——滞后特征无泄漏，回测断言）。不支持协变量的算法选择协变量时被拒绝并提示原因。
4. 历史范围由用户自定：输入指标集的历史天数限制训练窗口与回测窗口，超过模块 lookback 时被拒绝。
5. 回测与评估记录保存所使用的输入指标集与参数版本；参数或输入集变化使既有晋升失效（沿用参数哈希，哈希包含输入集）。

### R3 新增指标种类（OOM / Java / Kubernetes）
1. 指标种类与单位：
   - OOM：`oom_events`（窗口内次数）、`oom_last_working_set_bytes`（最近一次 OOM 前的工作集，可选）。
   - Java：`jvm_heap_used_bytes`、`jvm_heap_max_bytes`、`jvm_gc_pause_seconds`（窗口内总暂停）、`jvm_threads_live`、`jvm_non_heap_used_bytes`。
   - Kubernetes：`k8s_cpu_requests_cores`、`k8s_cpu_limits_cores`、`k8s_memory_requests_bytes`、`k8s_memory_limits_bytes`、`k8s_cpu_throttled_seconds`。
2. 每种有内置预设（含数据来源、所需标签、限制，标「未验证」）和演示数据生成器支持；预设单测：通过 ValidateMetricMapping、渲染后无残留变量、演示生成器返回单序列。
3. 指标目录收录这些指标，`/metrics`、查询接口与目录对账测试仍通过。
4. 采集：新增指标随模块的映射一起采集、保留期同样本；协变量缺失不影响目标指标采集。

### R4 OOM 对预测与推荐的影响
1. 模块配置 `oom_lookback_days`（默认 14）与 `oom_memory_uplift`（默认 1.2，范围 1–3）。
2. 窗口内发生 OOM 时：内存推荐 ≥ `max(预测峰值×余量, 最近一次 OOM 前工作集×uplift, 当前 limit×uplift（若已知且 OOM 与 limit 相关）)`，称为「OOM 地板」；地板对**所有来源**生效（算法、固定周期不可低于地板、Fallback 也被抬到地板），决策说明与降级记录写明。
3. OOM 次数作为协变量可进入支持协变量的算法；回测中该特征无泄漏。
4. 测试：无 OOM → 结果不变（回归）；有 OOM → 内存不低于地板；地板受模块上限 `max_memory` 约束并在超限时标「OOM 地板被上限截断」且产生降级记录。
5. 页面：模块管理可配置；快照说明显示 OOM 地板及触发的 OOM 次数。

### R5 推荐计算（结合模块、算法、多指标）
1. CPU 推荐：基于 CPU 预测峰值与该算法实测峰值低估，受 `k8s_cpu_limits` 约束提示（推荐 > limits 时标记「会被限流」，不静默截断）；内存推荐含 R4。
2. 副本推荐沿用动态求解，并把 JVM 堆压力（`heap_used/heap_max` 的预测或最近峰值 > 阈值 `jvm_heap_pressure`，默认 0.85）作为**额外约束**：触发时副本下限上调一档并在说明中写明；无 JVM 指标则不产生该约束并说明。
3. 推荐详情（detail）记录：使用的输入指标集、各协变量是否可用、各约束数值、采用的来源与理由；页面可解释「为什么是这个值」。
4. 不使用固定值：测试用两组不同的指标数据得到不同推荐；固定周期/Fallback 只在其触发条件下出现。

### R6 模块管理与 Fallback
1. 模块可创建、编辑、启停、删除（启用中不可删）、克隆；与数据源映射、算法绑定、输入指标集、推荐关联可在模块页一处查看（关联摘要：映射数、绑定数、最新推荐、最近降级）。
2. Fallback 默认值按模块独立设置：CPU、内存、副本数（现有）+ `fallback_min_history_ratio`（历史覆盖率下限，见第 5 节）；未设置的字段在降级时「保持原值」并如实标注。
3. Fallback 触发见第 5 节；每次触发写入降级记录并可在「降级与回退记录」查询；模块页显示当前是否处于降级及原因（标签，非横幅）。
4. 边界（min/max）对 Fallback 同样生效；Fallback 副本不会导致低于当前副本的缩容（沿用 operator 规则）。

### R7 推荐与审计
1. 「资源推荐快照」：不可变版本、可按模块/来源/是否降级/时间筛选，详情含完整决策说明；服务端分页排序。
2. 「扩缩容执行记录」：operator 上报记录，可按模块/结果/时间筛选；与快照版本关联（decision_version 跳转）。
3. 「降级与回退记录」：来自推荐快照中 source 为 fallback / original、或带 OOM 地板、或协变量被丢弃的记录；列包含原因码、触发时间、恢复时间（下一次成功预测）、持续时长。
4. 「审计导出」：快照、执行、降级三类 CSV，单元格防公式注入；每次导出写操作日志（谁、范围、行数）。
5. 链路可追溯：执行记录 → 快照 → 算法预测行 → 评估；浏览器验收走通一条。

### R8 LDAP 并入系统参数
1. 「系统参数」页出现 LDAP 分类：开关、URL、StartTLS、跳过校验、CA、Bind DN、Bind 密码、超时、用户 Base/Filter、属性、组 Base/Filter/名称属性、默认角色、组角色映射；密码与 CA 加密存储，读取不回显，留空保持不变；保存时按现有规则校验（拒绝明文 ldap://、启用需先测试连接成功）。
2. 分类内有「测试连接」「立即同步」操作；结果有明确成功/失败信息。
3. authorization_ldap 菜单、路由、页面删除；授权管理页面不含 LDAP；旧 LDAP 数据在 `--upgrade` 迁入系统参数并保留登录行为（迁移测试：升级后 LDAP 用户仍可登录、组映射仍有效；升级前打印将迁移与将删除的表/键清单）。
4. 现有 LDAP 测试（本地 TLS 测试服务器）改为读系统参数后仍全部通过。

## 4. 数据与接口影响（实施前的设计点）

### 4.1 新增/变更模型
- `input_set`（名称、目标指标种类、协变量 JSON、历史天数、缺失处理）；`module_algorithm.input_set_id`。
- `forecast_module`：`oom_lookback_days`、`oom_memory_uplift`、`jvm_heap_pressure`、`fallback_min_history_ratio`、`max_missing_ratio`、各新指标映射 ID（或统一 `extra_mapping_ids` JSON，由映射的指标种类决定用途，避免 30 个字段）。
- `recommendation`：`degraded`（bool）、`degrade_reason`（码）、`oom_floor`（bool）；保持版本不可变。
- 预测算法接口：`Input` 增加 `Covariates map[string][]float64`（与 Values 同长、同网格），仅在算法声明支持时填充。
- LDAP：`ldap.*` 系统参数键；迁移后废弃 `ldap_setting`/`ldap_group_mapping`（进入退役表清单，显式 `--upgrade` 才删）。

### 4.2 指标种类（见 R3），写入 `model.MetricKinds` 与预设库，演示生成器补对应序列。

## 5. 预测失败时的降级规则

目标：任何一步失败都**可预期、可见、可恢复**，且不会使资源或副本变得更危险。

### 5.1 触发条件与原因码

| 原因码 | 触发条件 | 检测位置 |
|---|---|---|
| `no_metric_data` | 目标指标没有映射 / 数据源禁用或不可达 / 查询无序列 / 最新样本比 `3×step` 更旧 | Collect、loadInput |
| `insufficient_history` | 有效样本少于算法最小样本；或历史覆盖率（有效点/期望点）< `fallback_min_history_ratio`（默认 0.8）；或缺失率 > `max_missing_ratio`（默认 0.3） | loadInput、Predict |
| `all_algorithms_failed` | 该指标所有已绑定且合格的算法均报错、被跳过或未晋升（基线也失败） | Recommend |
| `prediction_stale` | 最新预测早于模块 TTL，或预测任务（forecast.run）超过 `2×TTL` 没有成功 | Recommend、查询时 |
| `prediction_system_error` | 预测服务内部错误（panic 被恢复、数据库不可读、任务连续失败 ≥ 3 次） | RunPipeline、任务 |
| `covariate_missing` | 输入指标集的协变量缺失 | Predict |
| `oom_floor` | 命中 OOM 地板（R4） | Decide |

### 5.2 降级链（从上到下，第一个可用者生效；与现有优先级兼容）

1. **Bypass**（模块旁路）：保持原值，不降级、不报警（人工决定）。
2. **固定周期**：命中则按周期（仍受 OOM 地板约束）。
3. **算法链**：按绑定优先级依次尝试；某算法失败 → 下一个；全部非基线算法不可用 → **基线算法**（naive/seasonal_naive）；仍不可用 → 进入第 4 步。
   - `covariate_missing`：该算法**丢弃协变量按单变量运行**（若输入集配置为「判为降级」则视为该算法失败），决策说明写明；
   - 回测未晋升的算法不是故障，按现有规则不参与，但不触发 `all_algorithms_failed`（基线可用即可）。
4. **模块 Fallback**：使用模块 Fallback 默认值（CPU/内存/副本，各字段独立；受边界约束；再受 OOM 地板抬高）。标记 `degraded=true`、原因码。
5. **保持原值**：模块没有设置对应 Fallback 字段 → 不修改该字段（`Set=false`），记录原因码。这是安全的最终兜底。

规则细则：
- **按字段独立降级**：CPU 数据缺失不影响副本推荐；某字段降级不拖累其他字段。
- **只向安全方向降级**：Fallback 副本低于当前副本时 operator 不缩容（现有规则）；Fallback 内存/CPU 低于 OOM 地板时被抬到地板。
- **不抖动**：从降级恢复到算法结果需要连续 **2 个**成功周期（`recover_cycles`，可配置），期间继续使用 Fallback，避免数据时断时续造成震荡；恢复时记录恢复时间。
- **新鲜度**：降级快照与正常快照一样带 TTL；过期后 operator 沿用现有「冻结/Fallback 只增不减」规则。
- **可见性**：降级快照 `degraded=true` + 原因码；模块页显示降级标签；「降级与回退记录」可检索；指标 `kubeforecast_fallback_total{module,reason}`（计数）与 `kubeforecast_module_degraded{module}`（0/1），标签集合固定在原因码枚举内。

### 5.3 验收（降级专项）
表驱动测试覆盖每个原因码 × 每个字段 × 有/无 Fallback：
1. 无指标数据 + 有 Fallback → 值为 Fallback（受边界），source=fallback，原因 `no_metric_data`；无 Fallback → 保持原值。
2. 历史不足（样本少于最小样本 / 覆盖率低于阈值）同上；阈值边界值（刚好等于）按「足够」处理。
3. 全部算法失败：基线可用则用基线（不降级）；基线也失败才用 Fallback。
4. 预测任务超 `2×TTL` 未成功 → 新推荐为 `prediction_stale` 降级；任务恢复后需连续 2 个成功周期才回到算法结果（测试用可控时钟）。
5. 协变量缺失：按单变量运行且说明可见；配置为「判为降级」时降级。
6. 降级 + OOM：Fallback 内存低于 OOM 地板时被抬高，且记录两个原因。
7. 字段独立：只有 RPS 缺失 → 副本降级、CPU/内存仍来自算法。
8. Bypass 与固定周期优先于降级且不产生降级记录（固定周期仍受 OOM 地板）。
9. 降级记录页、模块页标签、指标 `kubeforecast_fallback_total` 与快照字段一致（对账测试）；浏览器验收：把演示数据源禁用后，下一周期出现降级记录，恢复后需 2 个周期才恢复。

## 6. 实施顺序与风险

1. **先做** R8（LDAP 并入，风险集中在迁移与登录回归）与菜单拆分/权限迁移（R1），因为后续页面都挂在新菜单下。
2. 再做数据层：新指标种类与预设（R3）、输入指标集与算法协变量（R2）、OOM 地板（R4）、降级链与记录（第 5 节、R6）。
3. 然后推荐计算接入多维度（R5）、推荐与审计各页（R7）。
4. 最后统一页面规范与浏览器验收（R1）。

风险：① MACD 与既有规则冲突（假设 A）；② 协变量与回测泄漏——必须有「协变量只用滞后值」的断言；③ 菜单码迁移不当会让角色丢权限；④ LDAP 迁移失败会锁定目录用户（本地账号始终可用，迁移前先备份、打印清单）；⑤ 所有新指标预设与动态结果在真实环境前保持「未验证」。

## 7. 删除/改名影响范围

- 删除：authorization_ldap 前后端页面与路由、LDAP 专用 handler 路由（`/api/v1/ldap/*` 改为系统参数分类内的测试/同步操作端点）、`ldap_setting`/`ldap_group_mapping` 表（升级后退役）。
- 改名/拆分：forecast_evaluation → 回测/评估/排行榜；forecast_recommendations → 快照/执行/审计导出；forecast_algorithms 的模板标签 → 独立菜单；forecast_metrics 的预设按钮 → 趋势对比。
- 不受影响：已有预测算法的结果（回归测试保证单变量算法在相同输入下输出不变）、operator 与 CRD、任务中心、登录与令牌流程。


## 8. 实施结果与偏差（2026-10-24）

已按本规格实施 R1–R8，验证与未验证项见 [执行进度](../../../../docs/platform-progress.md) 末尾同日记录。与上文设计的差异：

| 项 | 规格 | 实际 |
|---|---|---|
| MACD（假设 A） | `macd_trend` 作趋势修正项 | 单变量算法，计算快/慢/信号 EMA 差分后外推趋势，`max_adjustment` 限幅，结果下限为 0；不读取协变量 |
| 新指标种类名（R3、4.2） | `oom_events`、`jvm_heap_used_bytes` 等带单位后缀 | 沿用现有 `oom` 并以 `oom_working_set`、`jvm_heap_used`、`jvm_heap_max`、`jvm_nonheap_used`、`jvm_gc_pause`、`jvm_threads`、`k8s_cpu_requests`、`k8s_cpu_limits`、`k8s_mem_requests`、`k8s_mem_limits`、`k8s_cpu_throttle` 命名，单位由 `model.MetricKinds` 给出；目录里的 /metrics 名称为 `usage_<kind>` 系列 |
| 降级原因码（5.1） | no_metric_data / insufficient_history / all_algorithms_failed / prediction_stale / prediction_system_error / covariate_missing / oom_floor | no_data / stale_data / insufficient_history / predictor_failed / no_prediction / stale_prediction / no_eligible_model / recovering / oom_floor。`covariate_missing` 不是降级码：丢弃策略下算法按单变量运行并在预测结果（`dropped_covariates`）里写明，degrade 策略下该算法失败并落入 predictor_failed；基线可用时不降级 |
| 指标（5.2） | `kubeforecast_fallback_total{module,reason}`、`kubeforecast_module_degraded{module}` | `kubeforecast_fallback_total{reason,field}`（不带 module，避免高基数）；是否降级走目录指标 `recommendation_degraded`（导出为 `kubeforecast_recommendation_value{resource="degraded"}`），OOM 地板为 `recommendation_oom_floor` |
| 恢复滞后（5.2） | 连续 2 个成功周期 | `recover_cycles`（默认 2、可配）；状态存 `module_run_state`，只由定时流水线推进，手动「生成推荐」不改变它 |
| 历史范围（R2.4） | 输入集历史天数不得超过模块 lookback | 历史天数取 min(输入集, 模块 lookback)，0 表示用模块全部；输入集中不再单独校验上限，避免与模块配置互相约束 |
| 审计（R7.5） | 执行 → 快照 → 预测行 → 评估 | 执行记录链到快照页（按模块与版本过滤）；快照到预测行与评估的追溯沿用算法中心的绑定追溯，没有单页一键贯通 |
| 页面标题 | 标题 ≥ 4 个汉字且含对象 | 菜单按第 2 节命名；页面内表格标题沿用各自含对象的名称 |
