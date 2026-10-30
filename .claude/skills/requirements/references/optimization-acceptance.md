# 预测系统优化：现状核对、交付范围、验收标准与删除影响

本文把 8 条优化需求拆成可独立交付、可逐项验收的模块。现状来自 2026-10-24 对代码的核对（数字为当时统计），不是计划。每个模块的验收分三层，缺一层不能算完成：**自动化测试**（Go/前端单测、集成测试）、**浏览器验收**（scripts/e2e 扩展，真实 Chromium、全新数据库、模拟外部系统）、**真实环境**（真实 Prometheus/集群/LDAP；没有就明确标"未验证"，不用模拟结果替代）。

## 0. 需要先确认的三件事（影响删除范围，确认前不动手删除）

| # | 问题 | 本文采用的假设 | 若假设不成立 |
|---|---|---|---|
| A | "移除 Telegram 相关配置参数"是只删配置，还是整个 Telegram 功能？ | **整体移除 Telegram**（机器人、Agent、Client、转发、Bot API 节点、账号绑定、通知渠道、相关配置）。理由：这些功能离开 telegram.* 配置和 SysConfig 项即无法工作，只删参数会留下不可用的代码和页面。 | 若只删参数，需保留约 8,400 行死代码并标注不可用，不建议 |
| B | "保留登录日志页面及其设置功能"：现在**没有**独立的登录日志页面。 | 保留并**补齐**：后端已有 login_audit_log 表和登录失败锁定/验证码设置；新增"登录日志"页面（系统管理下），与操作日志页面并列，设置沿用 sys.auth.*、sys.operation_log.retention_days，并新增登录日志保留天数。 | 若指的是操作日志页，则只需保留，无新增 |
| C | 删除是否包含**数据库表和已有数据**？ | 代码删除立即做；表和数据**只经显式 `--upgrade` 清理**，先输出库存清单（沿用 cleanupRetiredModules 模式），不在普通启动时删，不自动清生产库。 | 若要保留历史数据，则不清表，仅停用 |

## 1. 八条需求的现状核对

| 需求 | 现状 | 差距 |
|---|---|---|
| 1 数据源与指标预设 | 数据源、映射都是空表单，无预设、无 Demo；映射的 PromQL 需用户自写 | 缺预设库、Demo 一键示例、指标定义文档 |
| 2 算法配置 | 算法参数只能在"模块→算法"弹窗里逐个绑定；无独立入口，看不到某算法被哪些模块使用、历史表现 | 缺算法中心（目录、参数模板、关联关系、表现） |
| 3 页面规范 | 4 个预测页中 history、evaluation、recommendations 用普通 NDataTable：无搜索卡片、无 ExpandableDataTable、无列配置、无自适应分页；modules 页结构合规但把绑定/周期放在裸表格弹窗里；LDAP 页也是自由布局 | 3 页需按规范重做，modules/LDAP 需调整 |
| 4 功能精简 | 工作流（pkg/workflow 1,711 行等）与 Telegram（约 8,400 行）深度耦合；默认首页是 workflow_pipelines；无独立登录日志页 | 见第 3 节 |
| 5 Operator 与 Metrics | Operator 只有 controller-runtime 默认指标；无业务指标（扩缩容次数、期望/当前副本、决策年龄、冲突、对账错误）。Platform 指标只导出"最新值"，无历史序列 | 缺 Operator 业务指标与 Platform 历史指标体系 |
| 6 算法与机器学习 | 六个算法在 pkg/forecast，注册表 registry.go，经 forecastsvc.Predict/Backtest 调用，由 forecast.run/backtest 任务定时运行；模型无状态、每次重新拟合，不持久化产物 | 需文档化位置/调用/运行机制；产物持久化与再训练间隔未实现 |
| 7 模块管理与实例数 | 模块 CRUD、绑定、周期、推荐已有；副本数 = ceil(预测峰值×余量÷**手填的单 Pod 容量**)，容量为 0 则不推荐副本 | 缺"按算法需求动态计算最优实例数" |
| 8 历史与表现指标 | 历史样本 metric_sample；预测 forecast_prediction；表现 backtest_run、prediction_evaluation；推荐 recommendation；执行 scaling_execution——已落库，但只有"最新值"以 Prometheus 指标暴露，没有统一的指标目录和可查询的历史指标 | 缺指标目录、历史查询接口与页面 |

## 2. 模块交付范围与验收标准

编号即交付顺序依赖：M4 先于 M3（先删再统一规范，避免给将删的页面返工）；M1、M2 先于 M7；M6 与 M7 并行，M5 依赖 M6 的指标命名。

### M1 数据源与指标预设

**交付**：内置预设库（只读，随程序发布）：每条预设含 id、名称、适用场景、指标类型、层级、PromQL 模板、单位、步长、**数据来源**（哪个 exporter/组件、需要的标签）、使用说明、已知限制。首批覆盖：cAdvisor CPU/内存（workload、container 层级）、kube-state-metrics 副本数与 Ready 数、ingress-nginx 与 Istio 的 RPS/延迟、OOM 次数。Demo：一键创建"演示数据源 + 预设映射 + 演示模块"，演示数据源指向内置的合成数据生成器（不依赖外部 Prometheus），并明确标记为演示数据。映射页"从预设创建"，创建后可编辑，保留来源预设 id。
**验收**：
1. 每条预设渲染后能通过 `ValidateMetricMapping`；预设单元测试覆盖全部层级与必需变量。
2. 对模拟 Prometheus，每条预设的查询都被正确渲染并返回单序列（测试）。
3. ~~Demo 一键创建后无需手工配置即可跑通全流水线~~：该功能已于 2026-10-30 删除（演示模块与真实模块混在一起容易误解），本条不再验收；要验证流水线请用隔离的真实或模拟 Prometheus。
4. 页面上每个预设都显示数据来源、所需 exporter 与限制；预设说明有中英文。
5. 真实环境：至少一条 cAdvisor 预设在真实 Prometheus 上返回数据（未做前标"未验证"）。

### M2 算法中心

**交付**：独立入口"预测 > 算法"。内容：算法目录（类别、版本、参数 schema、是否基线、复杂度与最小样本说明）；**参数模板**（命名的参数组，如"日周期 1 分钟步长"，可复用、可设为模块默认）；关联视图（哪些模块/指标绑定了该算法、各自参数、最近回测与实测表现）；模块绑定改为"选模板或自定义"，绑定仍落在 module_algorithm。
**验收**：
1. 参数模板的创建/编辑/删除走 schema 校验；被引用的模板不可删除，改模板需选择"仅新绑定生效"或"同步到引用它的绑定"，且被同步的绑定会使既有晋升失效（参数哈希变化）并在页面提示。
2. 算法详情页的"使用情况"与数据库实际绑定一致（集成测试）。
3. 表现区显示该算法近 N 次回测 WAPE、实测 WAPE、峰值低估，数据来自 backtest_run / prediction_evaluation，且可跳转到对应模块。
4. 算法—预测任务的关联可在页面追溯：绑定 → 定时任务 forecast.run → 预测行（algorithm_id、model_version）→ 评估。
5. 页面遵循 M3。

### M3 页面规范统一

**范围**：forecast 下 4 页 + 新增页面（算法中心、指标、登录日志、预设）+ LDAP 页 + 数据源两页复核。规范来源：.agents/skills/constraint/frontend.md。
**验收（逐页清单，浏览器验收脚本自动检查其中可机检项）**：
1. 列表页结构为：搜索卡片（list-search-form，防抖自动提交，重置/搜索按钮）→ 表头操作（TableHeaderOperation）→ ExpandableDataTable + 分页 → 操作列；检索/排序在服务端。
2. 分页含自适应行数与 30/50/100/200；列设置按用户入库（use-table-column-config，注册 PageModelMap）；必要列可点击复制；展开箭头/复选框/批量操作沿用公共实现。
3. 列标题短、说明放问号提示；同类列宽一致；操作横排 NSpace wrap=false 且给 width；状态用标签。
4. 表单：标签在上方；多字段用宽屏弹窗；提示放字段旁 IconTooltip，不用常驻警告横幅（现 LDAP 页的提示横幅、历史页的缺样警告需改为字段旁提示或表头标签，真正的错误仍显眼）。
5. 图表区独立不重叠，在最小高度检查；弹窗图表"打开→关闭→再打开"无资源泄漏（脚本覆盖）。
6. 中英文完整，无残留键；locales 测试、vue-tsc、oxlint、eslint、oxfmt 全过。
7. 各页在 1440、1024、390 宽度截图人工查看，无横向溢出。

### M4 功能精简与删除（影响范围见第 3 节）

**交付**：删除工作流与 Telegram 的前后端、数据模型、任务、配置、菜单、文档语义；首页改为 forecast 模块页；补登录日志页面。
**验收**：
1. `go build ./...`、`go vet`、`go test ./...`、vue-tsc、构建全过；仓库内 `grep -ri "workflow\|工作流\|telegram"` 在代码、文档、技能、脚本中为 0 命中（迁移里显式的"已退役表清单"和本文第 3 节除外，且需注释说明）。
2. 菜单根目录为：数据源、预测、任务中心、系统管理、授权管理；get-user-routes 的 home 指向存在的页面；旧路由返回 404。
3. 全新库 `--migration` 后无 workflow_*、telegram_*、bot_command_def 表；既有库 `--upgrade` 打印库存清单后才清理，且保留用户、角色、权限、系统设置、任务定义（除退役任务）与登录/操作日志数据（迁移测试：用含旧表的库验证）。
4. 用户模型/DTO/用户列表/个人中心不再出现 Telegram 字段；登录接口、令牌、OTP、LDAP 流程回归通过。
5. 通知引擎保留 webhook、voice 渠道，notify 任务与相关测试仍通过。
6. 登录日志页：按用户名/IP/结果/时间范围检索，分页排序服务端；设置项（锁定阈值与窗口、验证码触发、保留天数）可在页面或系统设置修改并生效（集成测试：改保留天数后清理任务生效）。

### M5 Operator 与 Metrics 补齐

**交付**：
- Operator 业务指标（经 controller-runtime metrics 端点，kube_resource_fcst_operator_ 前缀）：对账次数与耗时、扩缩容动作计数（按 direction/result）、期望/当前/Ready 副本、决策年龄与有效性、冲突（按来源）、平台调用错误；Operator 事件与状态保持现状。
- Operator 决策来源补 **Prometheus 来源**（读取一条已存在的指标作为期望副本）；Adapter 来源仍不做，需在文档说明。
- Platform 指标补全：采集成功/失败/耗时、各数据源状态与延迟、预测任务耗时、回测次数、实测评估延迟（见 M6 指标目录）。
**验收**：
1. Operator 指标有单元测试（fake client 下一次对账后各指标值正确），端点可抓取；清单含 metrics Service/ServiceMonitor 示例。
2. Prometheus 来源有 httptest 测试：多序列、NaN、陈旧、权限失败均走冻结/回退。
3. 指标标签基数有测试断言（仅 module/metric/algorithm/resource/source/direction/result）。
4. 真实集群未验证前，文档保持"未验证"。

### M6 历史数据、表现指标与统一 Metrics 体系

**存储位置与保留（现状，需文档化并成为验收对象）**：

| 数据 | 表 | 写入者 | 保留 |
|---|---|---|---|
| 历史样本（采集到的指标点） | metric_sample、metric_series | forecast.run（Collect） | module.lookback_hours（默认 720h） |
| 预测结果 | forecast_prediction（只追加） | forecast.run（Predict） | 30 天 |
| 回测结果 | backtest_run | forecast.backtest | 180 天 |
| 实测评估（预测准确率） | prediction_evaluation | forecast.evaluate | 180 天 |
| 推荐快照 | recommendation | forecast.run / 手动 | 180 天，每模块最新永远保留 |
| 扩缩容记录 | scaling_execution | operator 上报 | 90 天 |

**交付**：**指标目录**（代码内单一来源）：每个指标含名称、含义、单位、标签、来源表、聚合方式。分三类：资源使用（cpu/memory/rps 原始与聚合）、预测（预测值、预测峰值、置信/分位）、表现（WAPE、MAE、偏差、峰值低估、覆盖率、实测周期数）与执行（推荐值、实际副本、期望副本、执行结果）。提供：(a) /metrics 导出最新值（已有，扩充）；(b) **指标查询接口** `GET /api/v1/forecast-metrics/query`（按指标名、模块、算法、时间范围、聚合步长返回时间序列，来自上表）；(c) **指标页面**：选择指标/模块/算法，画曲线并对比，可导出 CSV。
**验收**：
1. 指标目录单元测试：每个指标的来源表存在、标签集合已声明、单位与映射一致；目录与 /metrics 输出、查询接口三者一致（测试对比）。
2. 查询接口对 1 万行以上数据按步长聚合返回，响应有点数上限；跨算法对比同一时间窗口时对齐 target_time。
3. 页面可画"预测 vs 实测"和"各算法 WAPE 随时间"，浏览器验收截图人工查看。
4. 算法评估可只用该接口完成（不直接读表）：提供示例查询并在测试中跑通。
5. 保留策略有测试，并在页面说明每类数据的保留期。

### M7 模块管理与动态最优实例数

**交付**：模块管理页整合：配置、算法关联、资源推荐、实例数推荐及其推导依据。**实例数计算改为多约束动态求解**，取代"固定容量÷余量"的单一公式：

输入：各指标预测序列（含分位）、模块的资源 requests、目标利用率、容量来源、边界、算法不确定度（该算法近期实测/回测误差分位）。
对预测范围内每一步 t 计算每个约束下所需副本：
- 吞吐约束：ceil(RPS_pred(t)·(1+u) ÷ 单 Pod 容量)，其中 u 取该算法近期实测的"低估误差分位"（P90，无数据时用模块余量并标注"无实测"）。
- CPU 约束：ceil(CPU_pred(t)·(1+u) ÷ (每 Pod CPU requests × 目标利用率))。
- 内存约束：ceil(Memory_pred(t)·(1+u) ÷ (每 Pod 内存 requests × 目标利用率))。
- 最低 Ready 与上下界：按模块边界裁剪。
结果：取各约束的最大值；对序列再做**平滑与防抖**（扩容取峰值，缩容需连续多步低于当前才降，沿用冷却/限幅由 Operator 执行）。**单 Pod 容量**来源三选一并显示来源：手填（已验证）、**从历史估计**（需要副本数指标：用历史 RPS ÷ 副本数在 CPU 近饱和时段的分位估计，样本不足时拒绝给出）、不可用（则不产生吞吐约束）。不同算法产生不同序列，因而对**每个启用且合格的算法分别计算实例数**，按绑定优先级选用，页面并排展示各算法结果、哪个约束起决定作用。
**验收**：
1. 纯函数 `decision` 新增求解器，表驱动测试：单约束主导、多约束取最大、u 随算法误差变化、无实测时回退、容量估计样本不足被拒绝、平滑不振荡、边界裁剪、全零流量、峰值在范围末端。
2. 对合成数据：两个误差不同的算法在同一流量下得到不同实例数，且高误差算法更保守（集成测试）。
3. 推荐详情（detail）记录每个约束的数值、采用的约束、u 的来源；页面可解释"为什么是 N 个"。
4. 不再要求必须手填容量：配置了副本数映射且样本充足时可从历史估计，否则明确提示缺什么。
5. 向后兼容：已配置固定容量的模块结果不变（回归测试）。
6. 真实环境：估计出的容量须与压测值对照；未对照前标"未验证"。

## 3. 删除影响范围

### 3.1 工作流引擎（全部删除）

| 层 | 内容 |
|---|---|
| 后端包 | pkg/workflow（10 文件/1,711 行）、pkg/app/workflow.go、pkg/app/server/api/handle_workflow.go 及集成测试 |
| 数据层 | model/workflow.go；dao/workflow.go、workflow_pending_reply.go；表 workflow_def、workflow_instance、workflow_pending_reply |
| 接入点 | router.go（WorkflowRouter 注册、engine 注入）、pkg/cmd/server/server.go（NewWorkflowEngine）、app.go（NewHTTPServer 形参）、telegram 包对 workflow 的调用 |
| 前端 | views/workflow（pipelines 1,155 行、instances 433 行、flowchart 498 行）、components/business/workflow-instance-drawer.vue（885 行）、utils/workflow.ts、use-stats-stream 中的工作流事件、路由与类型、locale 中 workflow 段 |
| 菜单/路由 | 目录 workflow、workflow_pipelines、workflow_instances；**默认首页 workflow_pipelines**（handle_route.go、migration、前端 routes、e2e 脚本）需改为 forecast_modules |
| 权限种子 | 迁移 seedPlatformPermissions 中对应项；IsPlatformRoute 白名单；旧路由 404；retiredRouteCodes 清单 |
| 文档语义 | AGENTS.md、CLAUDE.md、skills（requirements、kube-resource-fcst-development、repository-map）、platform-progress、docs 中的"工作流引擎/Pipeline/流程工单"表述（约 17 处）；"四个管理模块"改为"数据源/预测/任务中心/系统/授权" |
| 测试与脚本 | handle_workflow_integration_test、workflow 包测试、scripts/e2e/platform.mjs（期望 workflow 菜单与首页）需改写 |

### 3.2 Telegram（按假设 A 整体删除）

| 层 | 内容 |
|---|---|
| 后端包 | pkg/telegram（2,101）、pkg/telegramagent（3,136）、pkg/telegramclient（1,524）、pkg/telegrambotapi（565）、pkg/app/telegrambotapi（305）、pkg/notify/telegram（741），合计约 8,400 行；pkg/message 中的 Telegram 消息体 |
| API | handle_telegram*.go、handle_bot_api_node*.go（共 13 个文件含测试）；apis/telegram*.go；路由组 /telegram 与公开登录/绑定接口；dev_mode 的 mock-login |
| 数据层 | 10 个 DAO、8 个模型；表 telegram_bot、telegram_agent_request、telegram_forward_rule、telegram_agent_status、telegram_bot_api_node、telegram_client_account、telegram_client_subscription、telegram_client_message、telegram_forward_channel、telegram_binding（及 bot_command_def、bot_scope 相关） |
| 用户模型耦合 | model.User.TelegramBinding 关联、UserDTO 的 telegramBinding、用户列表"Telegram 绑定"列、个人中心 Telegram 标签页、ActorName 的 Telegram 回退 |
| 配置 | config.yaml 与 config.Telegram 结构；EnsureDefaultConfigs 中 16 个 telegram.* 种子项；系统参数页 config-catalogue 中的 Telegram 分类；环境变量前缀项 |
| 通知 | notify 引擎注册 telegram 渠道的代码、notifyjob 中对 Telegram 的假设；**保留** webhook、voice 渠道与 notify 任务；notify_template 表是否保留取决于模板是否仍被 webhook/voice 使用（实施时核对，未被使用则一并退役） |
| 前端 | service/api/telegram.ts（522 行）、utils/forward-channel.ts、template-code-editor（仅 Telegram 模板用则删）、system/config 的 Telegram 设置、user-center 标签页、locale 段、类型 |
| 中间件 | operation_log.go 与 rbac.go 中对 /telegram 路径与 Mini App 客户端类型的特判；login_guard、token_session 中的 Telegram 客户端会话类型（保留 web/cli，删除 tma） |
| 测试 | 约 20 个 Telegram 相关测试文件随包删除；handle_ldap_test 等对 TelegramBinding 表的迁移引用需同步 |

### 3.3 保留与补齐
保留：登录/令牌/OTP/LDAP、用户-角色-权限-菜单、系统设置、操作日志、任务中心、通知引擎（webhook/voice）、数据源与全部预测功能。补齐：登录日志页面。

### 3.4 数据与迁移策略
`--migration`（新库）不再创建退役表；`--upgrade`（既有库）先打印退役表清单与行数，再删除，沿用 cleanupRetiredModules 的事务与幂等写法；任务定义中退役任务一并清理；不自动清生产库；清理前提示备份。**不可逆**，故在 3.1/3.2 的代码删除合并前，先单独提交"迁移与库存清单"并经确认。

### 3.5 风险
1. 首页与菜单种子变更影响所有已有用户的登录落地页，需在迁移与前端同时改。
2. User 模型去掉 Telegram 关联后，所有 Preload("TelegramBinding") 调用要清除，否则运行期报错。
3. dev_mode 的 mock-login 属 Telegram，删除后本地开发登录方式不变（仍用 admin 密码）。
4. 删除量约 1.2 万行后端 + 约 4 千行前端，必须按"先迁移后删代码、每步构建与测试"分步提交，避免一次性大改难以回滚。

## 4. 实施顺序与依赖

1. 确认第 0 节三件事。
2. M4 删除（含登录日志页）→ 回归全量测试与 e2e（改写 platform.mjs）。
3. M3 规范改造框架（共享的列表页骨架/组合式函数）→ 重做 history、evaluation、recommendations、LDAP，复核 modules、数据源。
4. M1 预设与 Demo → M2 算法中心。
5. M6 指标目录与查询 → M5 Operator/Platform 指标 → M7 实例数求解器与页面。
6. 每个模块完成时：更新本文验收勾选、progress 文档、技能；浏览器验收脚本随之扩展。

## 5. 总验收原则
- 每个功能必须同时有后端接口、前端入口、测试；不允许只有菜单、静态页或占位配置（浏览器验收脚本逐页检查"页面有数据来自后端、操作能落库"）。
- 未在真实环境验证的，状态写"未验证"，不写"完成"。
- 删除类变更以"构建/测试/迁移测试全过 + 关键词清零"为完成标准。

## 6. 完成情况（2026-10-24）

「已满足」指有自动化测试或浏览器验收证据；真实环境相关项一律标「未验证」。

| 模块 | 验收项 | 状态 | 证据 / 余项 |
|---|---|---|---|
| M1 | 1–2 预设可渲染、返回单序列 | 已满足 | pkg/preset 测试（全部层级、必需变量、渲染、演示生成器单序列） |
| M1 | 3 Demo 一键跑通并可见 | 已满足 | forecastsvc/demo_test.go、handle_preset_test.go、optimization.mjs |
| M1 | 4 来源/exporter/限制与中英文 | 已满足 | 预设页与测试（双语文本非空、文档指明的序列确在查询内） |
| M1 | 5 真实 Prometheus 返回数据 | **未验证** | 每条预设保持 verified=false |
| M2 | 1–5 模板、使用情况、表现、追溯、页面规范 | 已满足 | handle_algorithm_center_test.go、optimization.mjs |
| M3 | 1–7 页面规范逐项 | 已满足（有保留） | 搜索经 list-search 插件自动提交；服务端排序仅登录日志支持；弹窗内无图表所以重开项不适用；浏览器验收覆盖 1440/1024/390 与小窗口 |
| M4 | 1 构建/测试/grep | 已满足 | go build/vet/test、vue-tsc、构建；grep 仅余带注释的退役清单与测试 |
| M4 | 2 菜单根与 home | 已满足 | platform.mjs（home=forecast_modules，旧路由被拒） |
| M4 | 3 迁移与 --upgrade | 已满足 | 迁移测试；对旧库结构副本实跑，先打印 15 张表再删除 |
| M4 | 4–5 用户/登录/OTP/LDAP 回归、通知引擎保留 | 已满足 | 既有测试全部通过 |
| M4 | 6 登录日志页与设置 | 已满足 | handle_login_log_test.go、options_test.go（保留天数生效）、optimization.mjs |
| M5 | 1–3 指标、Prometheus 来源、基数 | 已满足 | pkg/operator/controller/metrics_test.go（fake client、httptest） |
| M5 | 4 真实集群 | **未验证** | 文档保持「未验证」 |
| M6 | 1–5 目录对账、查询上限与对齐、页面、仅用接口评估、保留期 | 已满足 | catalog_test.go、optimization.mjs |
| M7 | 1–5 求解器、误差差异、可解释、容量估计、向后兼容 | 已满足 | solver_test.go、replicas_test.go |
| M7 | 6 估计容量与压测对照 | **未验证** | 动态结果只在合成数据上验证 |
