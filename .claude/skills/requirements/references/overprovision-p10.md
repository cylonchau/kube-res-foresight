# P10 资源超配分析与可观测大盘（2026-11-19 规划）

状态：**A6（成本从产品面移出）已完成；A0～A5 未开始，本文其余是计划，不是实现。** 用户已答复三项：E2（operator 只读：节点；Pod 只统计已配置的模块）、E3（工作负载只统计已配置的模块，不自动发现全部）、E10（成本代码从产品面拿掉，代码保留）；其余 E 项还没答复。来源：用户给出的“资源超配分析与可观测性系统升级”需求和一组参考大盘截图（KubeCostOptimizer）。截图里的金额、成本列不在本阶段范围（见 [成本模块需求表](cost-module-requirements.md)）。动手前先读本文的“待你确认”表，没确认的项不进开发；页面规则见用户记忆：不用页签（含分段切换）、不拆成多页、字段带“?”说明。

## 阶段目标与边界

**目标**：先建一个以真实历史数据为依据、可解释、可追溯的 Kubernetes 资源超配分析系统；再考虑预测、自动优化和成本。

| 边界 | 说明 |
|---|---|
| 不用预测 | 本阶段超配判断**不引入预测模型**，现有预测算法保留，但不参与判断。P9 的 C2、C3 用平台推荐值当“安全值”，与此冲突，要重做（见“与 P9 的关系”） |
| 不算成本 | 机器成本、实例价格、OpenCost、成本预测、费用优化全部移出本阶段；也不把 Requests 降幅当成本节省 |
| 模式 | 保留 Observe / Recommend / Auto，本阶段以 Observe / Recommend 为主，**不根据超配判断自动改资源** |
| 数据 | 使用量来自 Prometheus；配置与元数据来自 Controller 上报；分析结果和历史存在平台数据库，不重复写全量时序 |
| Controller | 不连数据库，只经平台 API 与 Kubernetes API；新增的是**只读**权限 |

## 现状对照（以代码为准）

| 需求 | 现有 | 缺口 |
|---|---|---|
| 系统内全局大盘 | 首页 `views/dashboard`：模块计数、需要关注的模块、扩缩容累计；效果报告 | 集群级总量、可回收量、待优化应用、稳定性风险；Top N 排行；工作负载表（配额、实际使用、建议、状态诊断）；同屏对比 |
| Grafana Dashboard | `deploy/grafana/kube-res-foresight-dashboard.json`，只覆盖平台与 operator 自己的指标（45 个查询） | 集群、节点、工作负载、Pod、Container、JVM 的资源看板；与系统的关联跳转 |
| 集群与节点信息 | 没有持久化。P9 的 C4 能从 kube-state-metrics 读一次，但没存，也属于要移出的成本模块 | 采集（云厂商、Region、可用区、实例规格、容量与可分配、Requests/Limits 汇总、使用率）与持久化 |
| 工作负载配置 | 模块里手填的 Pod 请求；有 `k8s_cpu_requests/limits`、`k8s_mem_requests/limits` 的 Prometheus 来源 | **只对已配置的模块**（用户决定，E3）：它的工作负载、Pod、Container 的副本数、Requests/Limits 及其变化历史；不自动发现全部工作负载 |
| Controller 上报元数据 | operator 只上报扩缩容执行；RBAC 只有 deployments、statefulsets、HPA、KEDA、leases | 新增只读 RBAC：**nodes**（集群级）；**模块的 Pod 与工作负载**只统计已配置的模块（用户决定，E2）；快照上报接口 |
| JVM 指标 | 指标种类有 `jvm_heap_used/max`、`jvm_nonheap_used`、`jvm_gc_pause`、`jvm_threads`，预设均未验证；堆压力已并入副本求解 | Committed、Non-Heap Max/Committed、GC 次数；超配分析里的 JVM 口径 |
| 本地存储与保留 | 样本只存模块的来源；各类记录有保留设置 | 集群级快照、配置变更历史、分析结果的表与保留、降采样 |
| 多周期超配分析 | 效果报告的“浪费”只量副本；P9 C2 用预测推荐当安全值 | 全新：多窗口分位数、移动平均、节流、OOM、可信度、判定公式 |
| 关联跳转 | 无 | Grafana 地址与 dashboard uid（系统参数），深链携带集群、Namespace、工作负载、Pod、时间范围 |
| LLM 周期建议 | AI 助手（只读工具）、AI 定时任务（提示词加计划）、LLM 管理 | 超配分析的只读工具；任务结果落库并在大盘上展示；只给建议，不执行 |

## 参考截图 → 我们的做法

| 截图元素 | 做法 |
|---|---|
| 顶部“负载分析 / 资源大屏”切换 | 不用页签。只做一个页面；大屏的内容并进同一个视图 |
| 卡片：当前资源预留量（全副本总和，CPU 与内存） | 保留：全部工作负载 Requests 总量；“优化后预留量”改为“按真实历史的建议值合计” |
| 卡片：可节省的预留量 -60.16% | 改名“可回收的预留量”，只写资源量；**去掉金额口径** |
| 卡片：待优化超配应用 76 / 185，欠配 / 扩容应用 3 | 保留（超配、欠配按下面的判定） |
| 卡片：稳定性隐患 / OOM 告警，健康良好 | 保留，口径见“风险与可信度” |
| 降本排行榜（Top 10 与命名空间占比切换） | 改成一个图 + 下拉选“Top N / 按命名空间”，不用分段切换；去掉“降本”二字，改“可回收资源排行” |
| 筛选：搜索、命名空间、健康状态；计算口径（单 Pod / 全副本总和） | 保留，计算口径改成下拉 |
| 工作负载表：配额、实际使用 P95/峰值、建议、预留量变动、状态诊断、操作 | 保留列；“预留量变动”去掉金额；“HPA 副本、伸缩行为”只显示读到的外部 HPA 信息，不显示平台的预测副本（本阶段不用预测） |
| 同屏对比（需求第六节 4） | 点一行开弹窗，竖向堆叠：配置、使用分布、JVM、分析结果、历史记录；不用页签 |

## 工作包与顺序

| 包 | 名称 | 依赖 | 复用 |
|---|---|---|---|
| A0 | 设计收敛 | 下面“待你确认”的答复 | 本文、`integration-decisions.md` |
| A1 | 数据采集与持久化 | A0 | operator（只读上报）、平台 API、GORM 迁移、`pkg/collector`、指标预设 |
| A2 | 超配分析引擎 | A1 | 指标映射与模块、`pkg/cost` 里的快照读取和分位数（拆出来复用）、`RoundCPUMilli`/`RoundMemoryBytes` |
| A3 | 系统内大盘与详情 | A2 | 首页、列表与卡片组件、变更预览弹窗的做法 |
| A4 | Grafana Dashboard 与深链 | A1（指标名）、A3（路由） | `deploy/grafana`、Helm chart 的 `files/dashboard.json` |
| A5 | LLM 周期建议模块 | A2 | AI 助手的只读工具、AI 定时任务、LLM 管理 |
| A6 | 移出成本 | 无 | 见“与 P9 的关系” |

建议顺序：A6（先把成本从产品面拿掉）→ A0 → A1 → A2 → A3，A4 在 A1 之后可并行，A5 最后。

## A1 数据采集与持久化

| 项 | 内容 |
|---|---|
| Controller 上报 | 用户决定（E2）：operator 只读 **Node**（集群级的节点信息）；**Pod 与工作负载只统计已配置的模块**，operator 只取模块对应的 Deployment、StatefulSet、DaemonSet 及其 Pod，不上报其他 Pod；按周期和变化上报快照到平台 API（带唯一 ID，离线时进发件箱补报，沿用 R21）。不读 Namespace。**限制**：Kubernetes 的 RBAC 不能把 Pod 的读取限制到“某些模块的 Pod”，只能限制到命名空间；模块以外 Pod 不被统计靠 operator 的代码过滤，权限本身比实际读取的多 |
| 集群与节点 | 集群名称（系统参数）、节点数、总 CPU/内存、可分配 CPU/内存；云厂商（取自 `providerID`）、Region、可用区、实例规格（取自节点标签 `topology.kubernetes.io/*`、`node.kubernetes.io/instance-type`）；节点实际使用率来自 Prometheus；集群与节点的 Requests、Limits 汇总来自上报 |
| 工作负载配置 | Deployment、StatefulSet、DaemonSet、Pod、Container：副本数、每个 Container 的 Requests、Limits；**配置变化时记一条历史**，平时只更新“当前” |
| 使用量 | 由 Prometheus 查询得到（cAdvisor、kube-state-metrics、JVM 指标），按分析窗口聚合；不把全量时序复制进平台库 |
| JVM | Heap 和 Non-Heap 的 Used、Committed、Max；GC 次数与耗时（有指标才有）。GC 缺失时仍用 Used、Committed、Max 分析，**不把 Committed 当物理内存使用量** |
| 存储 | 表（名称暂定）：`cluster_node_snapshot`、`workload_config`（当前）、`workload_config_change`（历史）、`overprovision_analysis`（分析结果）；重启不丢；三库兼容 |
| 采样与保留 | 配置快照：变化时记，另外每小时一次；分析结果：按窗口定期算；保留期和降采样是系统参数，默认值见“待你确认” |

## A2 超配分析引擎

**纯函数 + 定时任务 + 存储 + API，只看真实历史。** 下表的口径和默认值都是**我的提议，没有经过你的确认和真实数据验证**。

| 项 | 提议 |
|---|---|
| 窗口 | 15 分钟、30 分钟、1 小时、1 天、7 天、15 天、30 天，另加自定义范围（自定义只在请求时算，不落定期任务） |
| 单 Pod 与总和 | 单 Pod：把窗口内所有 Pod、所有时刻的样本放在一起取分位数，最大值是其中最大；全副本总和：每个时刻把所有 Pod 加起来，再对时间取分位数。两个口径分别算、分别标明 |
| 分位数与最大值 | P50、P95、P99、Max |
| 移动平均 | 写成 `MA(n, 步长)`：最近 n 个采样点的平均，步长是采样间隔。例如 `MA5@1m` 是最近 5 分钟的平均，`MA15@1m` 是最近 15 分钟；**页面一律把步长写在后面**，不让 MA15 和“15 分钟”混为一谈。默认三条线 MA5、MA15、MA25，步长随窗口取 1m、5m、15m |
| 超配比例 | `(当前 Request − 建议 Request) / 当前 Request`，同时给分子分母的数值 |
| CPU 建议值 | `max(P95 × (1 + 余量), P99)`，余量默认 20%，基准窗口默认 7 天；取整沿用 `RoundCPUMilli`。**节流保护**：窗口内节流占比超过 5%（默认）时，使用量低估了需求，不判超配，状态写“受限” |
| 内存建议值 | `max(Working Set P99 × (1 + 余量), Working Set Max × 1.05)`，余量默认 15%；取整沿用 `RoundMemoryBytes`。**OOM 保护**：窗口内出现 OOMKilled，建议值不低于出事前的 Working Set × 1.2，也**不低于当前 Request（不建议降）**，风险写“高” |
| JVM 的用法 | 并排显示 Used、Committed、Max 和 Container 的 Working Set、RSS，不拿 Heap Used 代表 Container，不拿 Max 当消耗；只做提示（例如 Heap Max 远大于 Working Set 提示 `-Xmx` 可能偏大）；JVM 应用缺这些指标时降低可信度 |
| Limits | 只显示 Limit 与 Request 的比例和 Working Set 离 Limit 的距离；本阶段不给 Limit 建议值（沿用“不自动调低内存 Limit”） |
| 分级 | 超配比例 ≥ 50% 严重，30% 到 50% 中度，15% 到 30% 轻度，小于 15% 合理；Request 低于 P95 是欠配 |
| 风险与可信度 | 可信度看数据覆盖率（窗口内有数据的比例，≥ 80% 高，50% 到 80% 中，小于 50% 低且不给建议值）、窗口长度（不到基准窗口降一级）、JVM 指标缺失降一级；OOM、节流、峰值接近 Limit 抬高风险；这些原因都写在结果里 |
| 可解释性 | 每个结论带：为什么超配、依据哪些指标、分析了多久、真实用了多少、建议值怎么算（公式与代入的数）、公式版本 |
| 历史 | 每次分析存一条（窗口、时间、各分位数、建议、判定、可信度、公式版本），可查看、对比、追溯 |

## A3 系统内大盘

| 项 | 内容 |
|---|---|
| 位置 | 复用并扩展首页，或在预测结果下新增一个页面（待你确认）；一个主视图，不用页签 |
| 内容 | 四张汇总卡、可回收资源排行图、筛选栏、工作负载表、详情弹窗 |
| 关联 | 详情弹窗和 Deployment、Pod、Node 详情都有“在 Grafana 打开”，参数自动带上 |
| 数据口径 | 与 Grafana 看板用同一套 PromQL（同一份文档里写明），避免两边数字不同 |

## A4 Grafana Dashboard

| 项 | 内容 |
|---|---|
| 覆盖 | 集群、节点、工作负载、Pod、Container、JVM；变量：数据源、集群、Namespace、工作负载、Pod、时间范围 |
| 数据源 | cAdvisor、kube-state-metrics、JVM 指标（Micrometer 或 JMX exporter，指标名要按你们实际使用的核实） |
| 关联 | 系统参数里配置 Grafana 地址和 dashboard uid；系统跳转携带 `var-*` 和 `from`/`to`；Grafana 跳回系统用 dashboard 链接 |
| 交付 | 一个 JSON 文件，放 `deploy/grafana`，Helm chart 里同步一份 |
| 限制 | 我无法在真实 Grafana 里加载它；只能校验 JSON 结构，**不能保证在你的 Grafana 版本里能导入** |

## A5 LLM 周期建议（已并入 P11 的 C6，2026-11-19）

下面的内容保留作为 C6 的起点；AI 的会话、权限与审计、工具注册表、异步任务见 [ai-and-performance-p11.md](ai-and-performance-p11.md)。

| 项 | 内容 |
|---|---|
| 做法 | 给 AI 助手加只读工具：读超配分析结果；用现有 AI 定时任务周期运行，把回答（Markdown）保存下来，在大盘上有一个“AI 建议”区块显示最近一次 |
| 约束 | 只给建议，**不执行任何改动**；提示词里写明数据口径和可信度，低可信度的结论不能被写成确定的；任务运行失败要在大盘上可见 |
| 复用 | 沿用 AI 定时任务的计划、重试、邮件；回答落库是新增 |

## 与 P9 的关系

| 项 | 处理 |
|---|---|
| 菜单“成本与浪费”（`forecast-results_cost`）、页面、`/api/v1/cost/*`、`cost_price_rule` 表、四个 `cost.*` 系统参数 | 移出产品面（A6）。代码怎么处理要你定（删除、保留但不暴露，或其他） |
| `pkg/cost` 里的价格、分摊、装箱模拟 | 属于成本，随之暂缓 |
| `pkg/cost` 里的 kube-state-metrics 快照读取、分位数 `UsageOf` | 与成本无关，A2 复用；要拆出成本包 |
| C2、C3 的“安全值 = 平台推荐”和三档候选 | 违背“不用预测”，不再使用；A2 用上面的公式替代 |
| 四个财务口径、基线等设计 | 写进 [成本模块需求表](cost-module-requirements.md)，待确认后再做 |

## 待你确认（A0 的输入；没确认的不进开发）

| # | 决定 | 我的建议 | 理由 |
|---|---|---|---|
| E1 | 集群维度 | 仍是单集群，加一个“集群名称”系统参数，数据里带这个字段，页面和 Grafana 变量里有 `cluster` | AGENTS 规定单集群；留字段，将来多集群不用改表。若你要真正的多集群，是另一个大改动 |
| E2 | operator 新增只读 RBAC | **已答复**：只读；节点相关信息（nodes）上报，Pod 信息只统计已配置的模块。我的落实：nodes 集群级只读；模块的 Deployment、StatefulSet、DaemonSet 与 Pod 只读，operator 代码只取模块的；不读 namespaces | 用户决定；RBAC 对 Pod 只能按命名空间限制，见 A1 的限制 |
| E3 | 工作负载范围 | **已答复**：只统计已配置的模块，不自动发现全部工作负载 | 用户决定；所以大盘里的“应用”就是模块，数量取决于建了多少模块（模块有批量创建，要不要加“从集群选工作负载批量建模块”可以以后再说） |
| E4 | 公式与阈值 | 接受“A2”表里的默认值，全部做成系统参数 | 它们没有经过真实数据验证，必须可调 |
| E5 | 使用量怎么来 | 用 Prometheus 的 `quantile_over_time` 和子查询在查询时聚合，只存聚合结果 | 需求要求避免重复写全量时序；代价是查询慢，要限并发和超时 |
| E6 | 存储保留 | 配置快照历史保留 30 天，分析结果保留 90 天，配置变化历史保留 180 天，都可调 | 没有依据，只是起点 |
| E7 | 大盘放哪 | 新增页面“资源大盘”放在预测结果下，首页不动 | 首页已有模块状态，混在一起太杂；以后你想换再换 |
| E8 | Grafana 地址与 uid | 系统参数 `grafana.base_url`、`grafana.dashboard_uid` | 不写死 |
| E9 | LLM 周期 | 每天一次，分析可回收量最大的前 20 个工作负载 | 控制成本和噪声 |
| E10 | P9 成本代码 | **已答复并已做**：从产品面拿掉（菜单、页面、接口、系统参数、数据表），代码保留，文档标“暂缓” | 代码没提交过，删了就找不回；拆出可复用的部分也需要它 |

## 验收要求（来自需求，附我的验证方式）

| 需求 | 验证方式 | 能验证到什么程度 |
|---|---|---|
| 系统内大盘与 Grafana Dashboard | 合成数据加浏览器；JSON 结构校验 | 页面可以看；Grafana 无法在真实实例里加载 |
| 集群、Node、Pod、Container、JVM 采集与持久化 | fake client、假 Prometheus、重启后读库 | 不是真实集群 |
| 多周期 CPU/内存真实超配分析 | 公式的单测（含代入的数）、边界（OOM、节流、缺数据、短历史） | 公式正确；对真实负载是否合理要你的数据 |
| 同屏对比、双向关联 | 浏览器 | 链接参数可看；Grafana 端要你来点 |
| 判定公式、阈值、数据来源、可信度说明 | 写进页面“?”和文档 | 可以 |
| 独立成本模块需求表 | 本次已出 | 已完成 |
| 数据完整性、持久化、指标准确性、异常与缺失 | 单测加合成数据 | 指标准确性只能对照 PromQL 逻辑，不能对照真实 Prometheus |

## 状态

| 包 | 状态 | 证据 |
|---|---|---|
| A0 设计收敛 | E2、E3、E10 已答复；E1、E4～E9 还没答复 | |
| A1 数据采集与持久化 | 未开始 | |
| A2 超配分析引擎 | 未开始 | |
| A3 系统内大盘与详情 | 未开始 | |
| A4 Grafana Dashboard 与深链 | 未开始 | |
| A5 LLM 周期建议 | 未开始；**并入 P11 的 C6**（见 ai-and-performance-p11.md） | |
| A6 移出成本 | **已完成**（2026-11-19） | 见下面“A6 实际做了什么” |
| 独立成本需求表 | 已出（[cost-module-requirements.md](cost-module-requirements.md)） | |

## A6 实际做了什么（2026-11-19）

| 项 | 做法 | 结果 |
|---|---|---|
| 菜单 | 种子里去掉 `forecast-results_cost`，并把它放进 `retiredMenuCodes`：已经种过它的库在升级时会把它连授权一起删掉，它也不再路由 | 产品里没有“成本与浪费”入口 |
| 接口 | `router.go` 不再注册 `/api/v1/cost/*`；`handle_cost.go` 留着，没人挂它 | 请求这些路径得到 404 |
| 数据表 | `CostPriceRule` 不再注册进 `model.Models`，普通迁移不会建 `cost_price_rule` 表；自己的测试里手动建 | 新库没有这张表 |
| 系统参数 | 四个 `cost.*` 不再进 `timing.All()`，不会播种到系统参数页；定义留在 `pkg/timing/cost.go` | 参数页看不到 |
| 页面 | `web/src/views/forecast-results/cost/index.vue` 移到 `web/src/parked/cost/index.vue`（不在 `views` 下，路由生成器不会再扫到它），去掉路由条目和菜单标题 | 没有路由；仍参与类型检查 |
| 代码 | `pkg/cost`、`pkg/forecastsvc/cost.go`、DAO、模型、API 客户端和类型都保留，它们的测试照常运行 | 没删 |
| 验证 | `go test` 通过 `pkg/migration`、`pkg/timing`、`pkg/model`、`pkg/forecastsvc`、`pkg/app/...`、`pkg/cost`；前端 `vue-tsc`、eslint、locale 测试、`pnpm run build` 通过；重新构建没有把路由生成回来 | 没有在浏览器里确认菜单确实消失（没跑实例） |
| 注意 | 如果某个库已经跑过带成本菜单的迁移，要再跑一次 `--migration`（或 `--upgrade`）才会删掉菜单行；成本代码想重新启用时，要从 `retiredMenuCodes` 去掉这个码、恢复种子和路由 | |
